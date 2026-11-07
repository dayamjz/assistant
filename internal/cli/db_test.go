package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// TestTheDatabasePathIsWrittenAloneSoACallerCanSubstituteIt is PRD section
// 9's db path: the answer is the database file of the home the command acted
// on, and what a person reads is that path and nothing else.
//
// Writing it alone is the behaviour and not a detail of the rendering. The
// command exists so a reader outside this binary can open the records, which
// is done by composing the answer into another command line
// (sqlite3 -readonly "$(assistant db path)"), and a line beside the value -
// a phrase pointing at it, a label before it - becomes part of what the
// caller substitutes. P13 is what makes that a decision rather than an
// omission: the path is a mechanic, so it is written as the evidence
// document section 9 names and the sentence pointing a person at it lives in
// the specification and the README rather than on standard output.
func TestTheDatabasePathIsWrittenAloneSoACallerCanSubstituteIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	h := newHome(t)

	got := run(t, h, h.Root(), "db", "path")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant db path exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if got.stdout != h.Database()+"\n" {
		t.Errorf("assistant db path wrote %q, and the whole answer is %q", got.stdout, h.Database())
	}
	if got.stderr != "" {
		t.Errorf("assistant db path wrote %q on standard error, and a caller substituting the answer reads neither stream twice", got.stderr)
	}

	document := run(t, h, h.Root(), "--json", "db", "path")
	if document.code != machine.ExitOK {
		t.Fatalf("assistant --json db path exited %s:\n%s%s", document.code, document.stdout, document.stderr)
	}
	var answer machine.Database
	if err := json.Unmarshal([]byte(document.stdout), &answer); err != nil {
		t.Fatalf("the answer is not one database document: %v\n%s", err, document.stdout)
	}
	if answer.Path != h.Database() {
		t.Errorf("the document names %q as the database, and the home's is %q", answer.Path, h.Database())
	}
}

// TestTheDatabaseThePathNamesIsReadableWhileTheStoreIsOpen is the point of
// the verb: a reader that is not this binary opens the file the path names,
// read-only, while the records are open for writing, and reads the task the
// store wrote.
//
// It reads through internal/store's read-only open, which is a second
// connection to the file and the only reader this module can drive. That
// open is not a sqlite shell's: it sets this build's pragmas and refuses a
// schema this build did not migrate to, so what the test establishes is the
// path and not how an outside reader behaves. The path names the file the
// records are in, a second read-only connection to it succeeds while the
// store holds it open for writing, and it reads the records back; whether
// any particular outside reader can do the same is that reader's.
func TestTheDatabaseThePathNamesIsReadableWhileTheStoreIsOpen(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: "readablerecs", Shape: store.TaskInvestigation, Project: "the parser drops a trailing comma",
		Mode: "headless", WorktreePath: "/worktrees/readable",
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording a task: %v", err)
	}

	got := run(t, h, h.Root(), "db", "path")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant db path exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	path := strings.TrimSuffix(got.stdout, "\n")

	reader, err := store.OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatalf("opening %s read-only while the store holds it: %v", path, err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	snapshots, err := reader.TaskSnapshots(t.Context())
	if err != nil {
		t.Fatalf("reading the records of every task through the path: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Task.ID != "readablerecs" {
		t.Fatalf("the read-only reader found %d task(s) at the path the verb printed, and the store wrote one", len(snapshots))
	}
}

// TestTheDatabaseVerbServesItsSubcommandAndNothingElse holds the verb to the
// one subcommand PRD section 9 names: path reaches a verb, the verb with no
// subcommand and the verb with a word that is not one are incorrect usage,
// and the help lists what it takes.
func TestTheDatabaseVerbServesItsSubcommandAndNothingElse(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	for _, args := range [][]string{
		{"db"},
		{"db", "paths"},
		{"db", "path", "state.db"},
		{"db", "dump"},
	} {
		if got := run(t, h, h.Root(), args...); got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s",
				strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	help := run(t, h, h.Root(), "db", "--help")
	if help.code != machine.ExitOK {
		t.Fatalf("assistant db --help exited %s:\n%s%s", help.code, help.stdout, help.stderr)
	}
	// The row, not the word: a help text that said "path" anywhere would
	// satisfy a substring check for the name alone, so this asks for the
	// summary beside it, as the table holds the two.
	rows := cli.Subcommands()["db"]
	if len(rows) == 0 {
		t.Fatal("the db verb has no subcommand row to look for in its help")
	}
	for _, row := range rows {
		if !strings.Contains(help.stdout, row[0]) || !strings.Contains(help.stdout, row[1]) {
			t.Errorf("the help does not list %q with its summary %q:\n%s", row[0], row[1], help.stdout)
		}
	}
}
