package agents_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
)

// realLsof is the real output of the lsof run the listing makes, with this
// process's record found in it: the line opening it and the name line
// carrying its working directory.
func realLsof(t *testing.T) (out []byte, self, name string) {
	t.Helper()
	out, err := agents.LsofOutput(t.Context())
	if err != nil {
		t.Fatalf("running lsof as the listing does: %v", err)
	}
	self = "p" + strconv.Itoa(os.Getpid())
	lines := bytes.Split(out, []byte("\n"))
	for i, line := range lines {
		if string(line) == self && i+2 < len(lines) && string(lines[i+1]) == "fcwd" {
			return out, self, string(lines[i+2])
		}
	}
	t.Fatalf("lsof's output reports no working directory record for this process (%s): %q", self, out)
	return nil, "", ""
}

// TestTheListingReadsLsofsRealOutputAndRefusesWhatItCannotRead holds the
// parse of the working directories against what lsof really prints, rather
// than against output written here: the real output parses, and the
// working directory it reports for this process resolves to the one this
// process has. Every refusal is then a mutation of that real output, so
// each shape is one lsof's output is a step away from: an empty line, a
// descriptor that was not asked for, a second working directory for one
// process, and a field the run did not ask for each fail the parse; a name
// that is not an absolute path is left out, which the listing reads as a
// working directory it could not read rather than as one outside the copy.
func TestTheListingReadsLsofsRealOutputAndRefusesWhatItCannotRead(t *testing.T) {
	out, self, name := realLsof(t)
	cwds, err := agents.ParseLsof(out)
	if err != nil {
		t.Fatalf("parsing lsof's real output: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(cwds[os.Getpid()])
	if err != nil || got != want {
		t.Fatalf("lsof reports this process's working directory as %q (resolving to %q, %v), want %q", cwds[os.Getpid()], got, err, want)
	}

	record := self + "\nfcwd\n" + name + "\n"
	for _, c := range []struct {
		what   string
		mutate func(string) string
	}{
		{"an empty line", func(s string) string { return "\n" + s }},
		{"a descriptor other than cwd", func(s string) string { return replaceOnce(s, record, self+"\nftxt\n"+name+"\n") }},
		{"a second working directory for one process", func(s string) string { return s + self + "\nfcwd\n" + name + "\n" }},
		{"a field not asked for", func(s string) string { return replaceOnce(s, record, self+"\nccommand\nfcwd\n"+name+"\n") }},
		{"a name before any process", func(s string) string { return name + "\n" + s }},
	} {
		mutated := c.mutate(string(out))
		if mutated == string(out) {
			t.Fatalf("the mutation %q did not change the real output", c.what)
		}
		if _, err := agents.ParseLsof([]byte(mutated)); err == nil {
			t.Fatalf("lsof output with %s parsed", c.what)
		}
	}

	relative := replaceOnce(string(out), record, self+"\nfcwd\nnrelative/name\n")
	if relative == string(out) {
		t.Fatal("the relative-name mutation did not change the real output")
	}
	cwds, err = agents.ParseLsof([]byte(relative))
	if err != nil {
		t.Fatalf("parsing lsof output whose name for this process is not absolute: %v", err)
	}
	if got, reported := cwds[os.Getpid()]; reported {
		t.Fatalf("a name that is not an absolute path was reported as the working directory %q", got)
	}
}

// replaceOnce replaces the first old in s with replacement.
func replaceOnce(s, old, replacement string) string {
	return strings.Replace(s, old, replacement, 1)
}

// TestASweepOfACopyWhoseNameLsofCouldRenderDifferentlyFails holds the one
// refusal the listing makes before it reads anything: a copy whose path
// holds a backslash, lsof's escape byte, or a tab, which lsof prints as an
// escape, is one whose name lsof's output might not carry byte for byte, so
// the sweep fails rather than comparing, and its zero result is not clear.
// A space is not among them, and
// TestASweepOfACopyWithASpaceInItsPathEndsWhatRunsThereAndThenClears holds
// that a copy with one is swept.
func TestASweepOfACopyWhoseNameLsofCouldRenderDifferentlyFails(t *testing.T) {
	for _, name := range []string{`with\backslash`, "with\ttab"} {
		h, _ := sweepHome(t, sweepBase(t), name)
		dir := makeTaskCopy(t, h)
		r, err := agents.NewSweeper(h, sweepGrace, nil).Sweep(t.Context(), dir)
		if err == nil || errors.Is(err, agents.ErrSweepUnsupported) || errors.Is(err, home.ErrNotACopy) || r.Clear() {
			t.Fatalf("sweeping %q answered %s, %v, want a failed listing and the zero result", dir, r, err)
		}
	}
}
