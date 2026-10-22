package vcs

import (
	"cmp"
	"context"
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
