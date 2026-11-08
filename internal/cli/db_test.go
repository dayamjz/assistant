package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
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
// the verb: the file the path names holds the records, and a second
// read-only connection to it reads the task the store wrote while the store
// still holds it open for writing.
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

// TestTheDatabasePathCreatesNoHomeAndOpensNoDatabase holds the stuck-home
// promise: the verb names the file and neither creates the home nor opens
// the database. A --home that has never been opened stays absent, and under
// an existing home a missing state.db stays missing, so inserting Create or
// an Open of the database would fail this.
func TestTheDatabasePathCreatesNoHomeAndOpensNoDatabase(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "never-created")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("%s should be absent before the command: %v", root, err)
	}
	got := runArgs(t, t.TempDir(), "--home", root, "db", "path")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant --home %s db path exited %s:\n%s%s", root, got.code, got.stdout, got.stderr)
	}
	want := filepath.Join(root, "state.db") + "\n"
	if got.stdout != want {
		t.Errorf("assistant db path wrote %q, want %q", got.stdout, want)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("db path created %s; the verb creates no home", root)
	}

	h := newHome(t)
	path := h.Database()
	_ = os.Remove(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should be absent before the command: %v", path, err)
	}
	under := run(t, h, h.Root(), "db", "path")
	if under.code != machine.ExitOK {
		t.Fatalf("assistant db path exited %s:\n%s%s", under.code, under.stdout, under.stderr)
	}
	if under.stdout != path+"\n" {
		t.Errorf("assistant db path wrote %q, want %q", under.stdout, path+"\n")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("db path created or opened %s; the verb opens no database", path)
	}
}

// TestTheDatabasePathWithAControlCharacterIsRecoveredFromJSON holds the
// fidelity claim for machine.Database: a control character in the home root
// is escaped on the printed line and kept in the structured answer, so a
// caller that reads --json recovers the path.
func TestTheDatabasePathWithAControlCharacterIsRecoveredFromJSON(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	root := filepath.Join(t.TempDir(), "a\tb")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	path := h.Database()

	got := run(t, h, filepath.Dir(root), "db", "path")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant db path exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stdout, "\t") {
		t.Errorf("the printed path carries a raw tab: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, `\x09`) {
		t.Errorf("the printed path does not escape the tab as \\x09: %q", got.stdout)
	}

	document := run(t, h, filepath.Dir(root), "--json", "db", "path")
	if document.code != machine.ExitOK {
		t.Fatalf("assistant --json db path exited %s:\n%s%s", document.code, document.stdout, document.stderr)
	}
	var answer machine.Database
	if err := json.Unmarshal([]byte(document.stdout), &answer); err != nil {
		t.Fatalf("the answer is not one database document: %v\n%s", err, document.stdout)
	}
	if answer.Path != path {
		t.Errorf("--json recovered %q, want the home's database %q", answer.Path, path)
	}
}
