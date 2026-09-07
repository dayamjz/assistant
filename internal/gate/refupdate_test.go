package gate_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestParseRefUpdatesReadsWhatGitPutsInFrontOfTheHook is the test the rest of
// this file rests on, and it is the reason none of them state a reference
// update line for themselves.
//
// A push is made through a real gate, the stand-in the hooks invoke writes the
// bytes git handed it to a file, and those bytes are what is parsed. A table
// of lines somebody wrote by hand would establish that the parser reads that
// table; this establishes that it reads what the mechanism actually produces,
// which is the difference between a parser and a parser for a shape nothing
// emits.
func TestParseRefUpdatesReadsWhatGitPutsInFrontOfTheHook(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, log := recorderCommand(t, 0)

	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	written := standardInput(t, log)
	if len(written) == 0 {
		t.Fatal("the push put nothing in front of the hook, so this test would prove nothing")
	}
	updates, err := gate.ParseRefUpdates(strings.NewReader(string(written)))
	if err != nil {
		t.Fatalf("ParseRefUpdates over what git wrote (%q): %v", written, err)
	}
	if len(updates) != 1 {
		t.Fatalf("ParseRefUpdates read %d updates from %q, want the one branch that was pushed", len(updates), written)
	}
	got := updates[0]
	if got.Ref != "refs/heads/main" {
		t.Errorf("update names %q, want refs/heads/main", got.Ref)
	}
	if got.New != wc.commit {
		t.Errorf("update moves the branch to %q, want the commit that was pushed, %q", got.New, wc.commit)
	}
	if !got.Created() {
		t.Errorf("update over %q does not read as creating the branch, and the gate had no main before the push", written)
	}
	if got.Deleted() {
		t.Errorf("update over %q reads as deleting the branch", written)
	}
	if got.Branch() != "main" {
		t.Errorf("update is about branch %q, want main", got.Branch())
	}
}

// TestParseRefUpdatesReadsADeletionAsOne is the other half of the same
// reading, and it is derived the same way: git is asked to delete a branch and
// what it writes is what is parsed. A deletion is the update a notification
// must start no run for, so reading it as one is load-bearing rather than
// cosmetic.
func TestParseRefUpdatesReadsADeletionAsOne(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, log := recorderCommand(t, 0)

	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// A branch other than the gate's own HEAD, because git refuses to delete
	// the branch a bare repository's HEAD names and this test is about the
	// update line, not about that refusal.
	rawGit(t, wc.path, "branch", "work")
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "work")
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, ":work")

	written := standardInput(t, log)
	updates, err := gate.ParseRefUpdates(strings.NewReader(string(written)))
	if err != nil {
		t.Fatalf("ParseRefUpdates over what git wrote (%q): %v", written, err)
	}
	if len(updates) != 1 {
		t.Fatalf("ParseRefUpdates read %d updates from %q, want one", len(updates), written)
	}
	got := updates[0]
	if !got.Deleted() {
		t.Errorf("update over %q does not read as deleting the branch", written)
	}
	if got.Created() {
		t.Errorf("update over %q reads as creating the branch it deletes", written)
	}
	if got.Old != wc.commit {
		t.Errorf("the deletion says the branch stood at %q, want %q", got.Old, wc.commit)
	}
}

// TestParseRefUpdatesRefusesALineGitWouldNotHaveWritten is the refusal half.
//
// Admission decides whether a push proceeds, so a line it cannot read must
// stop it rather than be skipped: a push described by four lines of which one
// was dropped is a push nobody decided about. Each case here is a line that
// differs from the shape the test above read off a real push in exactly one
// way.
func TestParseRefUpdatesRefusesALineGitWouldNotHaveWritten(t *testing.T) {
	const object = "41aee5103622d689068e4316ecfa8e574292bd4c"
	for _, c := range []struct {
		name string
		line string
	}{
		{"two fields", object + " refs/heads/main"},
		{"four fields", object + " " + object + " refs/heads/main extra"},
		{"an old object that is not one", "not-an-object " + object + " refs/heads/main"},
		{"a new object that is not one", object + " NOT-AN-OBJECT refs/heads/main"},
		{"an uppercase object name", strings.ToUpper(object) + " " + object + " refs/heads/main"},
		{"an empty old object", " " + object + " refs/heads/main"},
		{"an empty reference name", object + " " + object + " "},
		{"a blank line", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			updates, err := gate.ParseRefUpdates(strings.NewReader(c.line + "\n"))
			if !errors.Is(err, gate.ErrMalformedRefUpdate) {
				t.Fatalf("ParseRefUpdates(%q) = %v, %v; want ErrMalformedRefUpdate", c.line, updates, err)
			}
			if updates != nil {
				t.Errorf("ParseRefUpdates(%q) returned %v alongside the refusal; a refused input describes no push",
					c.line, updates)
			}
			if !strings.Contains(err.Error(), c.line) {
				t.Errorf("the refusal does not quote the line it refused: %v", err)
			}
		})
	}
}

// TestValidateAndTheParserAgreeOnAReferenceName is the claim Validate's
// documentation makes, checked rather than asserted: what a well-formed update
// is has to be one rule asked twice, not a strict reading at the parser and a
// weaker one behind it.
//
// The weaker one is what an update reaching the service as a value would be
// read by, because nothing parses it there. A reference name carrying a space
// is the case that separated them: the parser splits a line on single spaces
// and so could never yield one, while Validate asked only that the name was
// not empty, and a name git could not have written would have had a run
// recorded for it.
//
// Each name is put to both, and what is checked is that they answer the same
// way. A test of Validate alone would say nothing about the two agreeing.
func TestValidateAndTheParserAgreeOnAReferenceName(t *testing.T) {
	const object = "41aee5103622d689068e4316ecfa8e574292bd4c"
	for _, c := range []struct {
		name    string
		ref     string
		refused bool
	}{
		{"an ordinary branch", "refs/heads/work", false},
		{"a branch with a slash in its name", "refs/heads/fm/gate-hooks", false},
		{"a tag", "refs/tags/v1", false},
		{"a name carrying a space", "refs/heads/a b", true},
		{"a name carrying a tab", "refs/heads/a\tb", true},
		{"a name carrying a newline", "refs/heads/a\nb", true},
		{"a name that is only a space", " ", true},
		{"an empty name", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			update := gate.RefUpdate{Old: strings.Repeat("0", 40), New: object, Ref: c.ref}
			valueErr := update.Validate()
			if refused := errors.Is(valueErr, gate.ErrMalformedRefUpdate); refused != c.refused {
				t.Fatalf("RefUpdate{Ref: %q}.Validate() = %v, so refused is %t; want %t",
					c.ref, valueErr, refused, c.refused)
			}
			_, parseErr := gate.ParseRefUpdates(strings.NewReader(update.String() + "\n"))
			parserRefused := errors.Is(parseErr, gate.ErrMalformedRefUpdate)
			if parserRefused != c.refused {
				t.Fatalf("the parser answered %v for the line %q, so refused is %t; want %t, the same "+
					"answer Validate gives", parseErr, update.String(), parserRefused, c.refused)
			}
		})
	}
}

// TestOneMalformedLineRefusesTheWholePush is the property the case above only
// implies: a push whose other lines are perfectly good is still refused whole.
// Returning the readable ones would let admission decide about part of a push.
func TestOneMalformedLineRefusesTheWholePush(t *testing.T) {
	const object = "41aee5103622d689068e4316ecfa8e574292bd4c"
	good := strings.Repeat("0", 40) + " " + object + " refs/heads/work\n"
	updates, err := gate.ParseRefUpdates(strings.NewReader(good + "rubbish\n" + good))
	if !errors.Is(err, gate.ErrMalformedRefUpdate) {
		t.Fatalf("ParseRefUpdates = %v, %v; want ErrMalformedRefUpdate", updates, err)
	}
	if updates != nil {
		t.Fatalf("ParseRefUpdates returned %v, and two of those three lines were readable; "+
			"a push is described whole or not at all", updates)
	}
}

// TestParseRefUpdatesReadsNoInputAsNoUpdates keeps the empty case out of the
// refusal. Nothing was described, which is a state the caller answers for -
// admission admits a push that changes nothing, and a notification starts
// nothing for it - and is not a description this could not read.
//
// A nil reader is the same answer, because the command surface hands over
// whatever the process was given and a verb reached without standard input
// must not fail differently from one given none.
func TestParseRefUpdatesReadsNoInputAsNoUpdates(t *testing.T) {
	for name, input := range map[string]io.Reader{
		"an empty reader": strings.NewReader(""),
		"no reader":       nil,
	} {
		updates, err := gate.ParseRefUpdates(input)
		if err != nil {
			t.Errorf("ParseRefUpdates over %s: %v", name, err)
		}
		if len(updates) != 0 {
			t.Errorf("ParseRefUpdates over %s = %v, want no updates", name, updates)
		}
	}
}

// TestBranchIsOnlyARefUnderRefsHeads keeps the one distinction a notification
// acts on. A tag and a note are references a push can carry, and a run started
// for either would be a run about something the gate does not validate.
func TestBranchIsOnlyARefUnderRefsHeads(t *testing.T) {
	for ref, want := range map[string]string{
		"refs/heads/main":          "main",
		"refs/heads/feature/work":  "feature/work",
		"refs/tags/v1":             "",
		"refs/notes/commits":       "",
		"refs/remotes/origin/main": "",
		"HEAD":                     "",
		"":                         "",
	} {
		if got := (gate.RefUpdate{Ref: ref}).Branch(); got != want {
			t.Errorf("RefUpdate{Ref: %q}.Branch() = %q, want %q", ref, got, want)
		}
	}
}
