package vcs

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
)

// The operations below are what a caller needs to take named state out of
// a working copy and put it back exactly: each is anchored to what the
// caller read and forces nothing, so git refuses rather than overwrites
// when the copy is not as the caller read it. Those that write a reference
// create no reflog for it: git still appends to a reflog that exists, so a
// caller that wants none written sets the reflogs aside first.

// DeleteRefAt removes the reference name only while it points at object,
// git's anchored update-ref -d, and refuses with a *CommandError when it
// points anywhere else or is gone. name is given in full: under refs/, or
// a root reference such as ORIG_HEAD, which RootRefs lists.
func (r *Repository) DeleteRefAt(ctx context.Context, name, object string) error {
	if err := checkFullRefName(name); err != nil {
		return err
	}
	if err := checkArg("object", object); err != nil {
		return err
	}
	_, err := r.run(ctx, "update-ref-delete-at", "update-ref", "--no-deref", "-d", "--end-of-options", name, object)
	return err
}

// CreateRefAt writes the reference name at object, as it is and never
// peeled, so an annotated tag's reference is put back naming the tag,
// only when name does not exist, git's create-only update-ref, creating no
// reflog for it, so a log the caller set aside is the one the reference has
// once it puts that back too. A symbolic reference under name is never
// followed: one that points at a reference refuses, and one that points at
// nothing refuses under the git on this project's CI and was seen replaced
// under git 2.36, so a caller may lose such a dangling symbolic reference,
// never what it points at. name is given in full, on DeleteRefAt's terms.
func (r *Repository) CreateRefAt(ctx context.Context, name, object string) error {
	if err := checkFullRefName(name); err != nil {
		return err
	}
	if err := checkArg("object", object); err != nil {
		return err
	}
	_, err := r.run(ctx, "update-ref-create-at", "-c", "core.logAllRefUpdates=false",
		"update-ref", "--no-deref", "--end-of-options", name, object, "")
	return err
}

// MoveHEAD detaches HEAD onto the commit to only while HEAD resolves to
// from, git's anchored update-ref --no-deref, touching neither the index nor
// any file of the working tree and creating no reflog for HEAD, and refuses
// with a *CommandError when HEAD resolves anywhere else. to and from are
// object names in full.
func (r *Repository) MoveHEAD(ctx context.Context, to, from string) error {
	if err := checkArg("to", to); err != nil {
		return err
	}
	if err := checkArg("from", from); err != nil {
		return err
	}
	_, err := r.run(ctx, "update-ref-head", "-c", "core.logAllRefUpdates=false",
		"update-ref", "--no-deref", "--end-of-options", "HEAD", to, from)
	return err
}

// PointHEADAt makes HEAD the symbolic reference to ref, a branch given in
// full, creating no reflog for HEAD and touching no file: what puts HEAD
// back on a branch.
func (r *Repository) PointHEADAt(ctx context.Context, ref string) error {
	if err := checkArg("ref", ref); err != nil {
		return err
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		return &argumentError{what: "ref", value: ref, reason: "is not a branch named in full, which begins refs/heads/"}
	}
	_, err := r.run(ctx, "symbolic-ref", "-c", "core.logAllRefUpdates=false", "symbolic-ref", "HEAD", ref)
	return err
}

// HEADTarget is the branch HEAD is the symbolic reference to, in full, or
// empty when HEAD is detached.
func (r *Repository) HEADTarget(ctx context.Context) (string, error) {
	out, code, err := r.runExpecting(ctx, "symbolic-ref-read", []int{1}, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || code == 1 {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ReplaceIndexEntries makes the index hold, at each of paths, exactly the
// entries given for it and no others, every stage included, through git's
// update-index --index-info, and touches no file of the working tree: a
// path given no entry is taken out of the index, and one given entries of
// stages 1 to 3 is put back conflicted. Every entry's path is one of
// paths.
//
// It takes out no entry at a path it was not given. --index-info takes
// out every entry a path it writes is a file or a directory of (an entry
// written at a takes out a/child, one at b/x takes out b; round 30), so
// the index is read first and every entry given is held against what
// would stand beside it: the entries read at the paths not given, and the
// other entries given. One whose path is a file or a directory of any of
// theirs, at any stage, refuses with an *IndexDisplaceError naming each,
// before git runs. A
// path that is one of paths is replaced as asked, so an entry written at
// a while a/child is among paths given nothing is not refused.
//
// It is not anchored, and it reads nothing at paths: whatever stands at
// one of them when git writes is replaced, whenever it was staged. The
// caller reads the entries at paths first and holds them to what it read,
// so an entry staged at one of paths after the caller's read is replaced
// unread. An entry staged after this function's own read, at a path an
// entry given is a file or a directory of, is taken out unread.
func (r *Repository) ReplaceIndexEntries(ctx context.Context, paths []string, entries []IndexEntry) error {
	if r.kind != KindWorktree {
		return &openError{path: r.path, kind: KindWorktree}
	}
	if len(paths) == 0 {
		return nil
	}
	format, err := r.ObjectFormat(ctx)
	if err != nil {
		return err
	}
	now, err := r.IndexEntries(ctx)
	if err != nil {
		return err
	}
	zero := strings.Repeat("0", 40)
	if format == "sha256" {
		zero = strings.Repeat("0", 64)
	}
	var in bytes.Buffer
	named := map[string]bool{}
	for _, p := range paths {
		if err := checkIndexPath(p); err != nil {
			return err
		}
		named[p] = true
		in.WriteString("0 " + zero + "\t" + p + "\x00")
	}
	for _, e := range entries {
		if !named[e.Path] {
			return &argumentError{what: "index entry", value: e.Path, reason: "is at a path not among those replaced"}
		}
		if e.Stage < 0 || e.Stage > 3 || !isOctal(e.Mode) || !isHex(e.Object) || len(e.Object) != len(zero) {
			return &argumentError{what: "index entry", value: e.Mode + " " + e.Object + " " + strconv.Itoa(e.Stage) + " " + e.Path,
				reason: "is not a mode, an object of this repository's format and a stage from 0 to 3"}
		}
		in.WriteString(e.Mode + " " + e.Object + " " + strconv.Itoa(e.Stage) + "\t" + e.Path + "\x00")
	}
	if displacing := displacements(now, named, entries); len(displacing) > 0 {
		return &IndexDisplaceError{Displacing: displacing}
	}
	_, err = r.runInput(ctx, "update-index-info", in.Bytes(), "update-index", "-z", "--index-info")
	return err
}

// IndexDisplaceError is ReplaceIndexEntries' refusal of entries that would
// take out an entry it was not given to replace, or another entry given:
// each path written and a path it would take out. Nothing was written.
type IndexDisplaceError struct {
	Displacing []IndexDisplacement
}

// IndexDisplacement is an entry's path written (Path) that is a file or a
// directory of an entry's path that would stand beside it (Displaced).
type IndexDisplacement struct {
	Path      string
	Displaced string
}

// Error names every path written and what it would take out.
func (e *IndexDisplaceError) Error() string {
	var each []string
	for _, d := range e.Displacing {
		each = append(each, d.Path+" would take out "+d.Displaced)
	}
	return "vcs: the index entries were not replaced, since git takes out an entry a path it writes is a file or a directory of: " + strings.Join(each, ", ")
}

// displacements is every entry of entries whose path is a file or a
// directory of the path of an entry that would stand beside it: one of now
// at a path not named, or another of entries. Stages are not told apart,
// so an entry beside one at another stage is named too, whether or not
// git would take it out.
func displacements(now []IndexEntry, named map[string]bool, entries []IndexEntry) []IndexDisplacement {
	beside := map[string]bool{}
	for _, e := range now {
		if !named[e.Path] {
			beside[e.Path] = true
		}
	}
	written := map[string]bool{}
	for _, e := range entries {
		written[e.Path] = true
		beside[e.Path] = true
	}
	// under is, for every directory of a path standing, one path under it.
	under := map[string]string{}
	for p := range beside {
		for d := p; strings.Contains(d, "/"); {
			d = d[:strings.LastIndexByte(d, '/')]
			if q, ok := under[d]; !ok || p < q {
				under[d] = p
			}
		}
	}
	var out []IndexDisplacement
	for p := range written {
		if q, ok := under[p]; ok {
			out = append(out, IndexDisplacement{Path: p, Displaced: q})
		}
		for d := p; strings.Contains(d, "/"); {
			d = d[:strings.LastIndexByte(d, '/')]
			if beside[d] {
				out = append(out, IndexDisplacement{Path: p, Displaced: d})
			}
		}
	}
	slices.SortFunc(out, func(a, b IndexDisplacement) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Displaced, b.Displaced))
	})
	return out
}

// IndexPathsMissing is every path the index holds an entry for that the
// working tree holds nothing at, as git ls-files --deleted reports it,
// each once and in git's order: what a checkout of the index would write.
// A path holding something else, a directory where the index holds a
// file among it, is not missing.
func (r *Repository) IndexPathsMissing(ctx context.Context) ([]string, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	out, err := r.run(ctx, "ls-files-deleted", "ls-files", "--deleted", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && (len(paths) == 0 || paths[len(paths)-1] != p) {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// checkIndexPath refuses a path git's -z input could not carry or that
// names no path of the working tree.
func checkIndexPath(p string) error {
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") {
		return &argumentError{what: "path", value: p, reason: "is empty, holds a NUL or is absolute"}
	}
	return nil
}

func isOctal(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CheckoutMissing writes the index's version of each path into the working
// tree, git checkout-index without force, and refuses with a *CommandError
// when any of them already stands there, leaving that file as it is: what
// fills paths a caller emptied, never one somebody wrote meanwhile.
func (r *Repository) CheckoutMissing(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"checkout-index", "--quiet", "--"}
	for _, p := range paths {
		if err := checkArg("path", p); err != nil {
			return err
		}
		args = append(args, p)
	}
	_, err := r.run(ctx, "checkout-index", args...)
	return err
}

// GitPath is the absolute path git uses for name in this working copy's
// repository directory, git rev-parse --git-path, which for a linked
// worktree is its own directory for what it keeps of its own (HEAD, the
// index, logs/HEAD) and the common directory for the rest.
func (r *Repository) GitPath(ctx context.Context, name string) (string, error) {
	if err := checkArg("path", name); err != nil {
		return "", err
	}
	out, err := r.run(ctx, "git-path", "rev-parse", "--path-format=absolute", "--git-path", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ConfigValue is the value git reads for the configuration key, the last
// one set when several are, and whether it is set at all, as git config
// -z --get puts it on the wire: the value ended by a NUL, or exit status 1
// for a key not set. An explicitly empty value, and a key given with no
// value, are set and empty.
func (r *Repository) ConfigValue(ctx context.Context, key string) (string, bool, error) {
	if err := checkArg("key", key); err != nil {
		return "", false, err
	}
	out, code, err := r.runExpecting(ctx, "config-get", []int{1}, "config", "-z", "--get", key)
	if err != nil || code == 1 {
		return "", false, err
	}
	value, ok := strings.CutSuffix(string(out), "\x00")
	if !ok {
		return "", false, &outputError{op: "config-get", detail: "the value of " + key + " is not ended by a NUL"}
	}
	return value, true, nil
}

// ConfigBool is the boolean git reads for the configuration key, by git's
// own reading of a boolean (git config --type=bool), and whether it is set
// at all; a value git does not read as a boolean is a *CommandError.
func (r *Repository) ConfigBool(ctx context.Context, key string) (value, set bool, err error) {
	if err := checkArg("key", key); err != nil {
		return false, false, err
	}
	out, code, err := r.runExpecting(ctx, "config-get-bool", []int{1}, "config", "--type=bool", "--get", key)
	if err != nil || code == 1 {
		return false, false, err
	}
	return strings.TrimSpace(string(out)) == "true", true, nil
}

// checkFullRefName refuses a reference name that is neither under refs/
// nor a root reference by git's rule (isRootRefName), so a short name git
// would disambiguate is never acted on.
func checkFullRefName(name string) error {
	if err := checkArg("ref", name); err != nil {
		return err
	}
	if !strings.HasPrefix(name, "refs/") && !isRootRefName(name) {
		return &argumentError{what: "ref", value: name, reason: "is neither a full reference name under refs/ nor a root reference"}
	}
	return nil
}
