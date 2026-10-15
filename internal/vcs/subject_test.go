package vcs_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// writeCommitObject writes a commit object with the given header lines after
// tree and the given message, with raw git, and returns its identifier. It is
// how a test reaches the shapes an ordinary git commit invocation will not
// write, such as a message opening with blank lines, without the package
// under test building them.
func writeCommitObject(t *testing.T, dir, tree string, header []string, message string) string {
	t.Helper()
	object := "tree " + tree + "\n" +
		"author Test <test@example.invalid> 1700000000 +0000\n" +
		"committer Test <test@example.invalid> 1700000000 +0000\n"
	for _, line := range header {
		object += line + "\n"
	}
	object += "\n" + message
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(object)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git hash-object: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCommitSubjectIsTheMessagesFirstLineThatIsNotBlank is what a pull
// request opened for a push is titled with, so each shape a message can take
// is checked for the line it yields: the ordinary subject and body, a message
// opening with blank lines, a header field running over several lines with a
// blank continuation in it, an empty message, and bytes that are not UTF-8.
func TestCommitSubjectIsTheMessagesFirstLineThatIsNotBlank(t *testing.T) {
	repo, dir := newCommitSubject(t)
	tree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))

	rawGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", "Title the pull request\n\nA body that is not the subject.")
	ordinary := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "commit", "--quiet", "--allow-empty", "--allow-empty-message", "-m", "")
	empty := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	cases := []struct {
		name string
		rev  string
		want string
	}{
		{"subject and body", ordinary, "Title the pull request"},
		{"a revision resolves to its commit", "main~1", "Title the pull request"},
		{"an empty message", empty, ""},
		{"leading blank lines", writeCommitObject(t, dir, tree, nil, "\n  \n  Indented after blanks  \nsecond line\n"),
			"Indented after blanks"},
		{"a header field over several lines", writeCommitObject(t, dir, tree, []string{
			"gpgsig -----BEGIN SIGNATURE-----",
			" not the subject",
			" ",
			" -----END SIGNATURE-----",
		}, "The message's own first line\n"), "The message's own first line"},
		{"bytes that are not UTF-8", writeCommitObject(t, dir, tree, nil, "caf\xe9 au lait\n"), "caf� au lait"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repo.CommitSubject(ctx(t), c.rev)
			if err != nil {
				t.Fatalf("CommitSubject(%s): %v", c.rev, err)
			}
			if got != c.want {
				t.Fatalf("CommitSubject(%s) = %q, want %q", c.rev, got, c.want)
			}
		})
	}
}

// TestCommitSubjectRefusesWhatIsNotACommit keeps a revision that names
// nothing, and one git would read as an option, from reaching cat-file.
func TestCommitSubjectRefusesWhatIsNotACommit(t *testing.T) {
	repo, dir := newCommitSubject(t)
	if _, err := repo.CommitSubject(ctx(t), "no-such-branch"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("an unknown revision returned %v, want ErrRefNotFound", err)
	}
	tree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))
	if _, err := repo.CommitSubject(ctx(t), tree); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("a tree returned %v, want ErrRefNotFound", err)
	}
	if _, err := repo.CommitSubject(ctx(t), "--all"); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("an option-shaped revision returned %v, want ErrInvalidArgument", err)
	}
}
