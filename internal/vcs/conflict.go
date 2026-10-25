package vcs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// ConflictMarkers maps each path the index holds an unresolved conflict for
// to the marker lines in its worktree file that none of the path's conflict
// stages holds, each once and in order.
//
// A marker line is one that starts with seven '<', seven '|', or seven '>'
// characters: the lines git writes to open a conflict, to open the common
// ancestor's section, and to close a conflict, which carry a label after the
// run of characters. A line one of the conflict's own sides holds is part of
// that side's content and is left out, so what remains is a line the merge
// added. The separator line of seven '=' is never one: it carries no label,
// and the same line is a Markdown heading underline. A path whose worktree
// entry is missing or not a regular file has no markers, and neither does
// one whose remaining set is empty; neither is in the map.
//
// It is a reading of the conflict as it stands, so a caller takes it before
// anything edits the file and compares it with MarkersLeft afterwards. What
// it does not recognize: a marker run shorter than seven characters, which a
// conflict-marker-size attribute can ask git for; a marker line identical to
// a line one of the sides holds; and a conflict git wrote no marker lines
// for, such as a deleted or a binary file.
//
// The worktree file is read on UncommittedDigest's terms, including how the
// context ends it and the wait it cannot end. Each stage's blob is read
// through this package's output cap, and one over it fails the call rather
// than going unread.
//
// It answers for the whole working copy, from its root, whichever directory
// the handle was opened on, and every path it reads or takes is
// repository-relative.
func (r *Repository) ConflictMarkers(ctx context.Context) (map[string][]string, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	r, err := r.atRoot(ctx)
	if err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "unmerged-stages", "ls-files", "--unmerged", "-z")
	if err != nil {
		return nil, err
	}
	stages, err := parseUnmerged(out)
	if err != nil {
		return nil, err
	}
	markers := make(map[string][]string)
	for path, entries := range stages {
		lines, err := worktreeMarkerLines(ctx, r.path, path)
		if err != nil {
			return nil, err
		}
		if len(lines) == 0 {
			continue
		}
		for _, entry := range entries {
			if !entry.regularBlob() {
				continue
			}
			blob, err := r.run(ctx, "unmerged-blob", "cat-file", "blob", entry.object)
			if err != nil {
				return nil, err
			}
			held, err := markerLines(ctx, bytes.NewReader(blob))
			if err != nil {
				return nil, err
			}
			lines = slices.DeleteFunc(lines, func(line string) bool { return slices.Contains(held, line) })
		}
		if len(lines) > 0 {
			markers[path] = lines
		}
	}
	return markers, nil
}

// MarkersLeft reports, in order, each path of markers whose worktree file
// still holds one of the marker lines ConflictMarkers recorded for it,
// whether or not the path is still unmerged in the index. A path whose
// worktree entry is now missing or not a regular file holds none. The file
// is read on ConflictMarkers' terms.
//
// It answers for the whole working copy, from its root, whichever directory
// the handle was opened on, and every path it reads or takes is
// repository-relative.
func (r *Repository) MarkersLeft(ctx context.Context, markers map[string][]string) ([]string, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	r, err := r.atRoot(ctx)
	if err != nil {
		return nil, err
	}
	var left []string
	for path, recorded := range markers {
		lines, err := worktreeMarkerLines(ctx, r.path, path)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(lines, func(line string) bool { return slices.Contains(recorded, line) }) {
			left = append(left, path)
		}
	}
	slices.Sort(left)
	return left, nil
}

// UnresolvePaths puts each of paths back into the index as the unresolved
// conflict it was before it was staged, from the record git keeps when a
// conflict is resolved in the index. The worktree is not touched, so what
// the file holds stays. Every path is then read back from the index, and one
// that is not unmerged is an error naming it: git restores nothing for a
// path it has no record for and still exits zero, so the read is what
// establishes the result. Paths are passed literally and nothing else is
// changed in the index.
//
// It answers for the whole working copy, from its root, whichever directory
// the handle was opened on, and every path it reads or takes is
// repository-relative.
func (r *Repository) UnresolvePaths(ctx context.Context, paths []string) error {
	if r.kind != KindWorktree {
		return ErrNotARepository
	}
	r, err := r.atRoot(ctx)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	const chunk = 256
	for start := 0; start < len(paths); start += chunk {
		args := append([]string{"update-index", "--unresolve", "--"}, paths[start:min(start+chunk, len(paths))]...)
		if _, err := r.run(ctx, "unresolve-paths", args...); err != nil {
			return err
		}
	}
	out, err := r.run(ctx, "unmerged-stages", "ls-files", "--unmerged", "-z")
	if err != nil {
		return err
	}
	stages, err := parseUnmerged(out)
	if err != nil {
		return err
	}
	var missing []string
	for _, path := range paths {
		if _, ok := stages[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("vcs: the index holds no unresolved conflict for %s after unresolving it",
			strings.Join(missing, ", "))
	}
	return nil
}

// unmergedEntry is one conflict stage of a path, as ls-files --unmerged
// prints it.
type unmergedEntry struct {
	meta   string
	mode   string
	object string
}

// regularBlob reports whether the stage records a regular file, whose blob
// is the file's bytes.
func (e unmergedEntry) regularBlob() bool {
	return e.mode == "100644" || e.mode == "100755"
}

// parseUnmerged reads ls-files --unmerged -z output into each path's stages,
// in the order printed.
func parseUnmerged(out []byte) (map[string][]unmergedEntry, error) {
	stages := make(map[string][]unmergedEntry)
	for entry := range strings.SplitSeq(string(out), "\x00") {
		if entry == "" {
			continue
		}
		meta, path, found := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !found || path == "" || len(fields) != 3 {
			return nil, &outputError{op: "ls-files", detail: "unrecognized entry " + entry}
		}
		stages[path] = append(stages[path], unmergedEntry{meta: meta, mode: fields[0], object: fields[1]})
	}
	return stages, nil
}

// worktreeMarkerLines is markerLines of the worktree file at path. A path
// that is missing or not a regular file has none, and a descriptor that is
// not a regular file is not read, as in the digest.
func worktreeMarkerLines(ctx context.Context, root, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := worktreePath(root, path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	f, err := openForDigest(full)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		closeErr := f.Close()
		return nil, errors.Join(err, closeErr)
	}
	lines, err := markerLines(ctx, f)
	closeErr := f.Close()
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	return lines, closeErr
}

// markerLines returns each distinct marker line in content, in order of
// first appearance, without its line ending. A line ends at '\n', and a
// '\r' before it is not part of the line. Only a marker line is kept in
// memory, so a long line that is not one is passed over. The context is
// checked before each read, and an ended context returns its error.
func markerLines(ctx context.Context, content io.Reader) ([]string, error) {
	br := bufio.NewReaderSize(contextReader{ctx: ctx, r: content}, 64<<10)
	var lines []string
	var line []byte
	start, keep := true, false
	for {
		chunk, err := br.ReadSlice('\n')
		if start {
			keep = markerShaped(chunk)
			line = line[:0]
		}
		if keep {
			line = append(line, chunk...)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			start = false
			continue
		case err != nil && !errors.Is(err, io.EOF):
			return nil, err
		}
		if keep {
			text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
			if !slices.Contains(lines, text) {
				lines = append(lines, text)
			}
		}
		if err != nil {
			return lines, nil
		}
		start = true
	}
}

// markerShaped reports whether a line starts with a run of seven '<', '|',
// or '>' characters.
func markerShaped(line []byte) bool {
	if len(line) < 7 {
		return false
	}
	c := line[0]
	if c != '<' && c != '|' && c != '>' {
		return false
	}
	for _, b := range line[1:7] {
		if b != c {
			return false
		}
	}
	return true
}
