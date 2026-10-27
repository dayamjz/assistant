package vcs

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's sha1 object format names objects with it; it is an identity here, not a protection
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// AllTreeEntries returns every entry of rev's tree, keyed by its path from
// the tree's root: every file's, every directory's own entry as a tree, and
// a submodule's as a commit, recursing into every directory. rev is anything
// that names a commit.
func (r *Repository) AllTreeEntries(ctx context.Context, rev string) (map[string]TreeEntry, error) {
	id, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "tree-all", "ls-tree", "-r", "-t", "-z", "--full-tree", id)
	if err != nil {
		return nil, err
	}
	entries := map[string]TreeEntry{}
	for _, record := range strings.Split(string(out), "\x00") {
		if record == "" {
			continue
		}
		// <mode> SP <type> SP <object> TAB <path>
		meta, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || path == "" {
			return nil, &outputError{op: "tree-all", detail: "unexpected ls-tree record " + record}
		}
		entries[path] = TreeEntry{Mode: fields[0], Type: fields[1], Object: fields[2]}
	}
	return entries, nil
}

// ObjectFormat is the hash the repository names its objects with, sha1 or
// sha256, as git reports it; anything else is refused as an output git was
// not expected to give.
func (r *Repository) ObjectFormat(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "object-format", "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	format := strings.TrimSpace(string(out))
	if _, err := objectHash(format); err != nil {
		return "", &outputError{op: "object-format", detail: "unrecognized object format " + format}
	}
	return format, nil
}

// BlobObject is the identifier of a blob holding what the working-tree entry
// at path holds, in the object format named (ObjectFormat): a regular file's
// bytes as they stand, with no filter, line-ending conversion or attribute
// applied, and a symbolic link's target, never what it points at. Equal to a
// tree entry's object, it says the entry holds exactly that blob's content;
// TestBlobObjectIsGitsIdentityOfTheEntry holds it to git's own for a file,
// an executable and a link, in both formats. Anything else, a directory
// among it, is refused, and so is a file whose size changed while it was
// read. It reads nothing of a repository and runs no git.
func BlobObject(format, path string) (string, error) {
	h, err := objectHash(format)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(h, "blob "+strconv.Itoa(len(target))+"\x00"+target)
	case info.Mode().IsRegular():
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		_, _ = io.WriteString(h, "blob "+strconv.FormatInt(info.Size(), 10)+"\x00")
		n, err := io.Copy(h, f)
		if err != nil {
			return "", err
		}
		if n != info.Size() {
			return "", fmt.Errorf("vcs: %s changed size while it was read", path)
		}
	default:
		return "", &argumentError{what: "path", value: path, reason: "is neither a regular file nor a symbolic link, so no blob holds it"}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// objectHash is a new hash of the object format named.
func objectHash(format string) (hash.Hash, error) {
	switch format {
	case "sha1":
		return sha1.New(), nil //nolint:gosec // see the import
	case "sha256":
		return sha256.New(), nil
	}
	return nil, &argumentError{what: "object format", value: format, reason: "is neither sha1 nor sha256"}
}

// Reach is what NotReachedFrom walks from: objects named by identifier, and
// the references git itself walks from, read by git at the moment of the
// walk rather than listed by the caller first.
type Reach struct {
	// Objects is object identifiers, each walked once however often named.
	Objects []string
	// Refs is every reference and every worktree's HEAD (git's --all).
	Refs bool
}

// args is reach as rev-list's arguments.
func (reach Reach) args() ([]string, error) {
	var args []string
	if reach.Refs {
		args = append(args, "--all")
	}
	args = append(args, "--end-of-options")
	var unique []string
	for _, object := range reach.Objects {
		if err := checkArg("object", object); err != nil {
			return nil, err
		}
		if !slices.Contains(unique, object) {
			unique = append(unique, object)
		}
	}
	return append(args, unique...), nil
}

// ReflogValues returns every object the reflogs record, by the value each
// entry moved from as well as the one it moved to, whatever its type, as
// git's own walk of the reflogs lists them (git rev-list --objects --no-walk
// --filter=tree:0 --reflog), each once, in that listing's order. It is the
// one read of a reflog entry's previous value: git log --walk-reflogs names
// each entry by the value it moved to alone, and no ref@{n} resolves to a
// previous value an entry expired from the middle of a log left. Every
// log is read, every worktree's HEAD's among them; ReflogValuesOf reads
// chosen ones. The filter leaves out a tree or blob that a commit or tree
// in the same listing holds; what holds it is listed. A repository whose
// reflogs record nothing returns nothing.
func (r *Repository) ReflogValues(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, "reflog-values", "rev-list", "--objects", "--no-walk", "--filter=tree:0", "--reflog")
	if err != nil {
		return nil, err
	}
	var values []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if object, _, _ := strings.Cut(line, " "); object != "" && !slices.Contains(values, object) {
			values = append(values, object)
		}
	}
	return values, nil
}

// NotReachedFrom returns what reach holds that incorporated and held do
// not, decided by git from this repository's own objects: the commits reach
// reaches that neither incorporated nor any of held reaches, as git
// rev-list lists them, most recent first, a tag followed to the commit it
// points at; and every object in Objects that is not a commit, an annotated
// tag, a tree or a blob, in Objects' order, that is not itself one of held,
// whatever else holds it, so a tree or blob named is listed even when a
// commit of incorporated holds the same one: the answer fails toward
// listing. incorporated is anything that names a commit; held is object
// identifiers this repository holds, of any type, and one that is not a
// commit and does not peel to one holds nothing but itself. It is git
// rev-list <reach> ^incorporated ^<each of held> for the commits and git
// rev-list --no-walk <Objects> for which of Objects are commits. Refs
// reaches commits alone: a reference pointing at anything else is named in
// Objects to be listed. An object named, in reach or in held, that the
// repository does not hold is an error, never an empty answer.
func (r *Repository) NotReachedFrom(ctx context.Context, reach Reach, incorporated string, held ...string) (commits, others []string, err error) {
	i, err := r.ResolveCommit(ctx, incorporated)
	if err != nil {
		return nil, nil, err
	}
	from, err := reach.args()
	if err != nil {
		return nil, nil, err
	}
	for _, object := range held {
		if err := checkArg("object", object); err != nil {
			return nil, nil, err
		}
	}
	if !reach.Refs && len(reach.Objects) == 0 {
		return nil, nil, nil
	}
	walk := append(append([]string{"rev-list"}, from...), "^"+i)
	for _, object := range held {
		walk = append(walk, "^"+object)
	}
	out, err := r.run(ctx, "commits-not-reached", walk...)
	if err != nil {
		return nil, nil, err
	}
	commits = strings.Fields(string(out))
	objects := Reach{Objects: reach.Objects}
	named, err := objects.args()
	if err != nil || len(reach.Objects) == 0 {
		return commits, nil, err
	}
	out, err = r.run(ctx, "named-commits", append([]string{"rev-list", "--no-walk"}, named...)...)
	if err != nil {
		return nil, nil, err
	}
	areCommits := strings.Fields(string(out))
	for _, object := range reach.Objects {
		if !slices.Contains(areCommits, object) && !slices.Contains(held, object) && !slices.Contains(others, object) {
			others = append(others, object)
		}
	}
	return commits, others, nil
}

// StoredObjects returns, for each of paths in this working copy, relative
// to its root, the identifier of the blob git would store for the file
// there now, in paths' order: its content as git hash-object computes it
// without writing, with the clean filters, the line-ending conversion and
// the working-tree encoding its attributes and configuration name applied,
// so a file git checked out converted reads as the blob it was checked out
// from. That runs any clean filter a configuration names, as git status
// does when it rehashes a file. A path holding no regular file, and one
// whose filter fails, is an error. The paths go to git on its command
// line, a bounded number at a time.
func (r *Repository) StoredObjects(ctx context.Context, paths []string) ([]string, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	const batch = 256
	var objects []string
	for start := 0; start < len(paths); start += batch {
		chunk := paths[start:min(start+batch, len(paths))]
		for _, path := range chunk {
			if path == "" || strings.ContainsAny(path, "\x00\n") {
				return nil, &argumentError{what: "path", value: path, reason: "must be non-empty and hold no NUL byte or newline"}
			}
		}
		out, err := r.run(ctx, "stored-objects", append([]string{"hash-object", "--"}, chunk...)...)
		if err != nil {
			return nil, err
		}
		got := strings.Fields(string(out))
		if len(got) != len(chunk) {
			return nil, &outputError{op: "stored-objects", detail: fmt.Sprintf("%d identifiers for %d paths", len(got), len(chunk))}
		}
		objects = append(objects, got...)
	}
	return objects, nil
}

// NotHeldBy returns, of objects, each once and in their order, the ones the
// history of rev does not hold: everything git's walk from rev lists, every
// commit rev reaches and every tree and blob those commits record (git
// rev-list --objects rev), is held, and nothing else is. An annotated tag
// is never in that walk, so a tag is always returned, and so is an object
// the repository does not hold at all. rev is anything that names a
// commit. The walk is of rev's whole history, so it costs that history's
// size; nothing is walked when objects is empty.
func (r *Repository) NotHeldBy(ctx context.Context, rev string, objects []string) ([]string, error) {
	for _, object := range objects {
		if err := checkArg("object", object); err != nil {
			return nil, err
		}
	}
	if len(objects) == 0 {
		return nil, nil
	}
	from, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "objects-held", "rev-list", "--objects", "--end-of-options", from)
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if object, _, _ := strings.Cut(line, " "); object != "" {
			held[object] = true
		}
	}
	var missing []string
	for _, object := range objects {
		if !held[object] && !slices.Contains(missing, object) {
			missing = append(missing, object)
		}
	}
	return missing, nil
}

// ObjectType is the type git records object under: commit, tag, tree or
// blob. An object the repository does not have is ErrObjectNotFound; one
// it has but could not read is an error that does not match it, so a
// caller that treats absence as an answer cannot take a failed read for
// it (round 46, P6).
//
// It asks cat-file --batch-check for the one object. On standard output
// git states the type and size of an object it read, or "missing" for one
// it did not (both documented forms), and the "missing" line reads the
// same whether git found nothing or found an object it could not read; on
// standard error it says nothing in the first case and reports the failed
// read in the second, which is what an empty, a corrupt and an unreadable
// loose object each put there when this was written (git 2.54), and what
// the package's test checks against the installed git. So "missing" with
// nothing on standard error is absence, and anything else that is not a
// type line is a failed read. What this does not cover: a git that fails
// a read without a word on standard error would be read as absence.
func (r *Repository) ObjectType(ctx context.Context, object string) (string, error) {
	if err := checkArg("object", object); err != nil {
		return "", err
	}
	out, stderr, err := r.runCollecting(ctx, "object-type", nil, []byte(object+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	fields := strings.Fields(lines[0])
	switch {
	case len(lines) != 1:
	case len(fields) == 3:
		return fields[1], nil
	case len(fields) == 2 && fields[0] == object && fields[1] == "missing" && stderr == "":
		return "", &objectError{object: object, repo: r.path}
	}
	return "", &outputError{op: "object-type", detail: "git answered " + strconv.Quote(strings.TrimSpace(string(out))) + " for " + object + " with " + strconv.Quote(strings.TrimSpace(stderr)) + " on standard error"}
}

// objectError reports an object the repository does not have.
type objectError struct {
	object string
	repo   string
}

func (e *objectError) Error() string {
	return "vcs: " + e.object + " is not an object in " + e.repo
}

// Unwrap makes every absent object match ErrObjectNotFound.
func (e *objectError) Unwrap() error { return ErrObjectNotFound }
