package vcs

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
)

// ReflogEntry is one entry of a reflog: the reference the log is kept for,
// as git names it in a reflog selector (HEAD, refs/stash, or
// worktrees/<name>/HEAD for another worktree's HEAD), the entry's position
// in that log, zero for the newest, and the object the entry records the
// reference moving to.
type ReflogEntry struct {
	Ref    string
	Index  int
	Object string
}

// AllReflogs reads every entry of every reflog git walks for --all from
// this working copy: HEAD's, every reference's that keeps one, and every
// other worktree's HEAD's. The entries are ordered by reference name and
// then by position, newest first, so two reads of unchanged logs return the
// same slice. It reads and writes nothing, and refuses with
// ErrNotARepository on a bare repository.
//
// Each entry is the object it records, so a log naming the same object
// twice has two entries, and a log that gains an entry gains one here
// whatever object it names, which is what lets a caller tell the stash
// list a stash was stored back onto from the list before it.
func (r *Repository) AllReflogs(ctx context.Context) ([]ReflogEntry, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	return r.reflogs(ctx, "--all")
}

// Reflogs reads every entry of the named references' reflogs, on
// AllReflogs' terms and in its order. Each name is a full reference name
// beginning refs/, or HEAD, which is this working copy's own, and carries
// no @{ selector of its own, which no reference name does: a name that is
// neither, or carries one, is refused with ErrInvalidArgument before git
// runs. A reference with no reflog contributes nothing, and one that does
// not exist is refused by git as a *CommandError, so a caller names only
// what it has read standing.
func (r *Repository) Reflogs(ctx context.Context, refs ...string) ([]ReflogEntry, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	args := []string{"--end-of-options"}
	for _, ref := range refs {
		if err := checkArg("ref", ref); err != nil {
			return nil, err
		}
		if ref != "HEAD" && !strings.HasPrefix(ref, "refs/") {
			return nil, &argumentError{what: "ref", value: ref, reason: "is neither HEAD nor a full reference name, which begins refs/"}
		}
		if strings.Contains(ref, "@{") {
			return nil, &argumentError{what: "ref", value: ref, reason: "carries a reflog selector, which no reference name does"}
		}
		args = append(args, ref)
	}
	return r.reflogs(ctx, append(args, "--")...)
}

// ErrRefStorageUnsupported is returned by ReflogValuesOf for a repository
// whose references are not kept as files, which it cannot read.
var ErrRefStorageUnsupported = errors.New("vcs: the repository does not keep its references as files, so its reflogs cannot be read entry by entry")

// ReflogValuesOf returns every object the named references' own reflogs
// record, by the value each entry moved from as well as the one it moved
// to, each once, in the order the logs record them (each log oldest first,
// the previous value of an entry before its new one), leaving out the null
// identifier an entry that created a reference moved from. Names are
// checked on Reflogs' terms. It reads only those logs: unlike
// ReflogValues, whose single-worktree read takes in the logs of every
// reference a repository's worktrees share, a working copy's own HEAD here
// is that HEAD's log alone, so a caller can tell what this working copy's
// own logs record from what a shared one does.
//
// git log --walk-reflogs names an entry by the value it moved to alone, and
// no ref@{n} resolves to a previous value an entry expired from the middle
// of a log left, so the logs are read from the files git keeps them in, at
// the path git rev-parse --git-path gives, line by line in the format
// git-update-ref(1) documents for them (the previous value, a space, the
// new value, a space, and the rest). A repository whose references are not
// files, which sets extensions.refStorage to anything else, is refused with
// ErrRefStorageUnsupported, and a line of any other shape with
// ErrMalformedOutput, never skipped. A reference with no reflog contributes
// nothing. It reads and writes nothing else, and refuses with
// ErrNotARepository on a bare repository.
func (r *Repository) ReflogValuesOf(ctx context.Context, refs ...string) ([]string, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	args := []string{"rev-parse", "--path-format=absolute"}
	for _, ref := range refs {
		if err := checkArg("ref", ref); err != nil {
			return nil, err
		}
		if ref != "HEAD" && !strings.HasPrefix(ref, "refs/") {
			return nil, &argumentError{what: "ref", value: ref, reason: "is neither HEAD nor a full reference name, which begins refs/"}
		}
		if strings.Contains(ref, "@{") {
			return nil, &argumentError{what: "ref", value: ref, reason: "carries a reflog selector, which no reference name does"}
		}
		args = append(args, "--git-path", "logs/"+ref)
	}
	storage, err := r.configValues(ctx, "extensions.refStorage")
	if err != nil {
		return nil, err
	}
	for _, s := range storage {
		if !strings.EqualFold(s, "files") {
			return nil, ErrRefStorageUnsupported
		}
	}
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return nil, err
	}
	width := 40
	if format == "sha256" {
		width = 64
	}
	null := strings.Repeat("0", width)
	out, err := r.run(ctx, "reflog-paths", args...)
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(paths) != len(refs) {
		return nil, &outputError{op: "reflog-paths", detail: "git gave " + strconv.Itoa(len(paths)) + " paths for " + strconv.Itoa(len(refs)) + " logs"}
	}
	var values []string
	for i, path := range paths {
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if line == "" {
				continue
			}
			fields := strings.SplitN(line, " ", 3)
			if len(fields) < 3 || !isObjectID(fields[0], width) || !isObjectID(fields[1], width) {
				return nil, &outputError{op: "reflog-values-of", detail: "the reflog of " + refs[i] + " holds a line that is not an entry"}
			}
			for _, v := range fields[:2] {
				if v != null && !slices.Contains(values, v) {
					values = append(values, v)
				}
			}
		}
	}
	return values, nil
}

// isObjectID reports whether s is an object identifier of width lowercase
// hexadecimal digits.
func isObjectID(s string, width int) bool {
	if len(s) != width {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// reflogs is the one walk both reads make. The selector, %gD, is the
// reference as git names it and the position in braces, and the position
// is taken only as a number: a selector in any other form is refused as an
// unrecognized entry, never guessed at, so a read whose form changed fails
// the caller loudly rather than reading something else as the same log.
// The walk passes no date option and Reflogs refuses a name carrying a
// selector of its own; TestReflogsReadPositionsWhateverTheOperatorsLogDate
// shows that the operator's log.date, set in the global configuration and
// in the copy's, does not change what the read returns.
func (r *Repository) reflogs(ctx context.Context, which ...string) ([]ReflogEntry, error) {
	args := append([]string{"log", "--walk-reflogs", "--no-show-signature", "--format=%gD%x00%H"}, which...)
	out, err := r.run(ctx, "reflogs", args...)
	if err != nil {
		return nil, err
	}
	var entries []ReflogEntry
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		selector, object, ok := strings.Cut(line, "\x00")
		at := strings.LastIndex(selector, "@{")
		if !ok || object == "" || at <= 0 || !strings.HasSuffix(selector, "}") {
			return nil, &outputError{op: "reflogs", detail: "unrecognized entry " + line}
		}
		index, err := strconv.Atoi(selector[at+2 : len(selector)-1])
		if err != nil || index < 0 {
			return nil, &outputError{op: "reflogs", detail: "unrecognized position in " + line}
		}
		entries = append(entries, ReflogEntry{Ref: selector[:at], Index: index, Object: object})
	}
	slices.SortFunc(entries, func(a, b ReflogEntry) int {
		return cmp.Or(strings.Compare(a.Ref, b.Ref), cmp.Compare(a.Index, b.Index))
	})
	return entries, nil
}
