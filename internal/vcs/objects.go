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
// the sets of names git itself walks from, each read by git at the moment
// of the walk rather than listed by the caller first.
type Reach struct {
	// Objects is object identifiers, each walked once however often named.
	Objects []string
	// Refs is every reference and every worktree's HEAD (git's --all).
	Refs bool
	// Reflogs is every object any reflog entry records, the value it moved
	// from as well as the value it moved to (git's --reflog), so a commit
	// only an entry's previous value records is reached: one git keeps
	// recoverable as ref@{n} when older entries are gone, or one an entry
	// expired from the middle of a log leaves recorded only there.
	Reflogs bool
	// ThisWorktree narrows Refs and Reflogs to this working copy: its own
	// HEAD and per-worktree references and their reflogs, with every
	// reference shared by all worktrees and the reflog each keeps, and no
	// other worktree's HEAD (git's --single-worktree).
	ThisWorktree bool
}

// args is reach as rev-list's arguments, the sets first: --single-worktree
// narrows only the sets named after it.
func (reach Reach) args() ([]string, error) {
	var args []string
	if reach.ThisWorktree {
		args = append(args, "--single-worktree")
	}
	if reach.Refs {
		args = append(args, "--all")
	}
	if reach.Reflogs {
		args = append(args, "--reflog")
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

// NotReachedFrom returns what reach holds that incorporated does not,
// decided by git from this repository's own objects: the commits reach
// reaches that incorporated does not, from Objects and from every set it
// asks for, as git rev-list lists them, most recent first, a tag followed to
// the commit it points at; and every object in Objects that is not a
// commit, an annotated tag, a tree or a blob, in Objects' order, whatever
// incorporated holds, so a tree or blob named is listed even when a commit
// of incorporated holds the same one: the answer fails toward listing.
// incorporated is anything that names a commit. It is git rev-list <reach>
// ^incorporated for the commits and git rev-list --no-walk <Objects> for
// which of Objects are commits.
//
// The sets reach commits only: an object that is not a commit and that a
// set alone names, a reflog entry's previous value that is a tree, a blob
// or a tag, is in neither answer. git writes a reflog for HEAD, branches,
// remote-tracking references, notes and the stash, none of which it lets
// point at anything but a commit, so such an entry is one a reference git
// was told to keep a reflog of wrote (core.logAllRefUpdates=always, or
// update-ref --create-reflog). An object named that the repository does not
// hold is an error, never an empty answer.
func (r *Repository) NotReachedFrom(ctx context.Context, reach Reach, incorporated string) (commits, others []string, err error) {
	i, err := r.ResolveCommit(ctx, incorporated)
	if err != nil {
		return nil, nil, err
	}
	from, err := reach.args()
	if err != nil {
		return nil, nil, err
	}
	if !reach.Refs && !reach.Reflogs && len(reach.Objects) == 0 {
		return nil, nil, nil
	}
	out, err := r.run(ctx, "commits-not-reached", append(append([]string{"rev-list"}, from...), "^"+i)...)
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
		if !slices.Contains(areCommits, object) && !slices.Contains(others, object) {
			others = append(others, object)
		}
	}
	return commits, others, nil
}

// ObjectType is the type git records object under: commit, tag, tree or
// blob. An object the repository does not hold is an error.
func (r *Repository) ObjectType(ctx context.Context, object string) (string, error) {
	if err := checkArg("object", object); err != nil {
		return "", err
	}
	out, err := r.run(ctx, "object-type", "cat-file", "-t", "--end-of-options", object)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
