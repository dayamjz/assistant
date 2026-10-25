package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// stoppedOnConflict builds a copy whose rebase has stopped on a conflict in
// clash.txt. The upstream side documents a conflict, so it holds lines
// shaped like markers that are content, not markers git wrote. It returns
// the copy, its directory, and the abbreviated name of the replayed commit
// git puts in its closing marker.
func stoppedOnConflict(t *testing.T, eol string, config ...string) (*vcs.Repository, string, string) {
	t.Helper()
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	for _, setting := range config {
		key, value, _ := strings.Cut(setting, "=")
		rawGit(t, dir, "config", key, value)
	}
	lines := func(text ...string) string { return strings.Join(text, eol) + eol }
	commitFile(t, dir, "clash.txt", lines("title", "base"), "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	topic := commitFile(t, dir, "clash.txt", lines("title", "topic"), "topic edit")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt",
		lines("title", "main", "<<<<<<< HEAD", "=======", ">>>>>>> example"), "main edit")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	return repo, dir, topic[:7]
}

// TestMarkerBaselineIsWhatTheSidesHold reads a real conflict whose upstream
// side documents conflict markers. The baseline is that side's two
// marker-shaped lines, "<<<<<<< HEAD" and ">>>>>>> example", which are
// content; the separator is never one. The file as git left it still holds
// a marker no side holds, the closing marker naming the replayed commit, so
// MarkersLeft names it. Under the diff3 style the common ancestor's marker is
// another, and CRLF line endings are not part of a line.
func TestMarkerBaselineIsWhatTheSidesHold(t *testing.T) {
	for _, c := range []struct {
		name   string
		eol    string
		config []string
	}{
		{name: "merge style", eol: "\n"},
		{name: "diff3 style", eol: "\n", config: []string{"merge.conflictStyle=diff3"}},
		{name: "crlf", eol: "\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, _, _ := stoppedOnConflict(t, c.eol, c.config...)
			baseline, err := repo.MarkerBaseline(ctx(t))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"<<<<<<< HEAD", ">>>>>>> example"}
			if got := baseline["clash.txt"]; !slices.Equal(got, want) || len(baseline) != 1 {
				t.Fatalf("baseline = %q, want clash.txt with %q", baseline, want)
			}
			left, err := repo.MarkersLeft(ctx(t), baseline)
			if err != nil || !slices.Equal(left, []string{"clash.txt"}) {
				t.Fatalf("markers left in the file as git left it: %q, %v; want clash.txt", left, err)
			}
		})
	}
}

// TestMarkersLeftReadsTheWorktreeFile follows the markers of a conflict
// through what a fixer can do with the file. Editing around them, and
// staging the file with them, leave them. Removing the lines git wrote while
// keeping the content shaped like markers, and deleting the file, leave
// none.
func TestMarkersLeftReadsTheWorktreeFile(t *testing.T) {
	repo, dir, _ := stoppedOnConflict(t, "\n")
	markers, err := repo.MarkerBaseline(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "clash.txt")
	conflicted := rawGit(t, dir, "show", ":3:clash.txt")
	steps := []struct {
		name string
		do   func()
		want []string
	}{
		{"as the conflict left it", func() {}, []string{"clash.txt"}},
		{"edited around the markers", func() { writeFile(t, path, "edited\n"+readText(t, path)) }, []string{"clash.txt"}},
		{"staged with the markers", func() { rawGit(t, dir, "add", "clash.txt") }, []string{"clash.txt"}},
		{"resolved with marker-shaped content", func() {
			writeFile(t, path, "title\nmain and topic\n<<<<<<< HEAD\n=======\n>>>>>>> example\n")
		}, nil},
		{"resolved to one side", func() { writeFile(t, path, conflicted) }, nil},
		{"deleted", func() {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, nil},
	}
	for _, step := range steps {
		step.do()
		left, err := repo.MarkersLeft(ctx(t), markers)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if !slices.Equal(left, step.want) {
			t.Fatalf("%s: markers left in %q, want %q", step.name, left, step.want)
		}
	}
}

// TestUnresolvePathsPutsAStagedConflictBack stages a conflicted file with
// its markers, which takes it out of the index's unmerged entries, and puts
// it back. The path is unmerged again with the conflict's own stages, and
// the worktree file keeps what it held.
func TestUnresolvePathsPutsAStagedConflictBack(t *testing.T) {
	repo, dir, _ := stoppedOnConflict(t, "\n")
	stages := rawGit(t, dir, "ls-files", "--unmerged", "--", "clash.txt")
	writeFile(t, filepath.Join(dir, "clash.txt"), "edited\n"+readText(t, filepath.Join(dir, "clash.txt")))
	held := readText(t, filepath.Join(dir, "clash.txt"))
	rawGit(t, dir, "add", "clash.txt")
	if got := rawGit(t, dir, "ls-files", "--unmerged", "--", "clash.txt"); got != "" {
		t.Fatalf("staging left the path unmerged:\n%s", got)
	}
	if err := repo.UnresolvePaths(ctx(t), []string{"clash.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := rawGit(t, dir, "ls-files", "--unmerged", "--", "clash.txt"); got != stages {
		t.Fatalf("after unresolving, the stages are\n%s\nwant\n%s", got, stages)
	}
	if got := readText(t, filepath.Join(dir, "clash.txt")); got != held {
		t.Fatalf("the worktree file changed:\n%s\nwant\n%s", got, held)
	}
}

// TestUnresolvePathsRefusesAPathItCannotPutBack names a path that was never
// conflicted. git restores nothing for it and still exits zero, so the
// refusal comes from reading the index back, and it names the path.
func TestUnresolvePathsRefusesAPathItCannotPutBack(t *testing.T) {
	repo, dir, _ := stoppedOnConflict(t, "\n")
	writeFile(t, filepath.Join(dir, "other.txt"), "other\n")
	rawGit(t, dir, "add", "other.txt")
	err := repo.UnresolvePaths(ctx(t), []string{"other.txt"})
	if err == nil || !strings.Contains(err.Error(), "other.txt") {
		t.Fatalf("unresolving a path with no conflict: %v, want a refusal naming it", err)
	}
}

// readText returns a file's content.
func readText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}

// TestARelabeledMarkerIsStillLeft rewrites the label of the closing marker
// git wrote, so the exact line git wrote is gone while a
// line shaped like a marker that neither side holds remains. That file
// still holds a conflict marker, and MarkersLeft names it.
func TestARelabeledMarkerIsStillLeft(t *testing.T) {
	repo, dir, topic := stoppedOnConflict(t, "\n")
	markers, err := repo.MarkerBaseline(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "clash.txt")
	relabeled := strings.ReplaceAll(readText(t, path), ">>>>>>> "+topic+" (topic edit)", ">>>>>>> theirs")
	writeFile(t, path, "edited\n"+relabeled)
	left, err := repo.MarkersLeft(ctx(t), markers)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(left, []string{"clash.txt"}) {
		t.Fatalf("markers left in %q after relabeling, want clash.txt:\n%s", left, readText(t, path))
	}
}
