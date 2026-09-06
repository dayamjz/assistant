package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// controls is the one table both renderings are held to. It is one table on
// purpose: the two paths drifted apart once because each had its own idea of
// what a control character is, and a rune checked against only one of them is
// a rune the other can still emit raw.
//
// U+001B is ESC, which encoding/json escapes on its own. U+007F and U+009B are
// the two encoding/json leaves as they stand, and U+009B is the single-byte
// control sequence introducer, which a terminal decoding UTF-8 acts on exactly
// as it acts on ESC-[.
var controls = []struct {
	name string
	rune rune
}{
	{"escape", 0x1b},
	{"bell", 0x07},
	{"delete", 0x7f},
	{"next line", 0x85},
	{"control sequence introducer", 0x9b},
}

func TestNeitherRenderingEmitsAControlCharacterRaw(t *testing.T) {
	t.Parallel()
	for _, c := range controls {
		if !unicode.IsControl(c.rune) {
			t.Fatalf("%s (U+%04X) is not a control character, so this table is testing the wrong thing", c.name, c.rune)
		}
		answer := runCarrying(string(c.rune))

		var human bytes.Buffer
		readOut(&human, answer)
		if strings.ContainsRune(human.String(), c.rune) {
			t.Errorf("the rendering a person reads emits %s (U+%04X) raw:\n%q", c.name, c.rune, human.String())
		}

		// The event stream is the third path text this build did not write
		// reaches a terminal on, and it carries a run's intent.
		var event bytes.Buffer
		readOut(&event, ipc.Event{
			Type:     ipc.TypeRunState,
			Revision: 1,
			Payload:  payloadCarrying(t, string(c.rune)),
		})
		if strings.ContainsRune(event.String(), c.rune) {
			t.Errorf("an event's payload emits %s (U+%04X) raw:\n%q", c.name, c.rune, event.String())
		}

		var document bytes.Buffer
		if err := machine.NewEncoder(&document).Encode(answer); err != nil {
			t.Fatalf("encoding an answer carrying %s: %v", c.name, err)
		}
		if strings.ContainsRune(document.String(), c.rune) {
			t.Errorf("the structured rendering emits %s (U+%04X) raw:\n%q", c.name, c.rune, document.String())
		}

		// The document is still one JSON document, and it decodes to the
		// string that went in: escaping is what a reader sees, not a change to
		// what a consumer parses.
		var decoded machine.Run
		if err := json.Unmarshal(document.Bytes(), &decoded); err != nil {
			t.Fatalf("the document carrying %s does not decode: %v\n%s", c.name, err, document.String())
		}
		if decoded.Decision == nil || len(decoded.Decision.Findings) != 1 {
			t.Fatalf("the document carrying %s lost the finding: %s", c.name, document.String())
		}
		if got := decoded.Decision.Findings[0].Description; got != carrying(string(c.rune)) {
			t.Errorf("the document carrying %s decodes to %q, want %q", c.name, got, carrying(string(c.rune)))
		}
	}
}

// carrying is the description a finding is given, with the rune under test in
// the middle of ordinary words so a rendering that dropped it rather than
// escaping it is caught too.
func carrying(control string) string {
	return "before " + control + " after"
}

// runCarrying is a run holding a decision whose finding carries the rune under
// test, which is the shape both renderings are given.
func runCarrying(control string) machine.Run {
	return machine.Run{
		Record:  store.Run{ID: "abc", Branch: "work", Status: store.RunHeld},
		Outcome: machine.OutcomeDecision,
		Decision: &machine.Decision{
			Decision: graph.Decision{Node: "intent.hold", Question: "decide", Options: []string{"approved"}},
			Stage:    "intent",
			Findings: []findings.Finding{{
				ID:          "finding",
				Severity:    findings.SeverityWarning,
				Action:      findings.ActionAsk,
				Description: carrying(control),
			}},
		},
	}
}

// payloadCarrying is the record an event on the run stream carries, encoded
// the way internal/service encodes it, with the rune under test in the intent
// a caller supplied.
func payloadCarrying(t *testing.T, control string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(store.Run{ID: "abc", Branch: "work", Intent: carrying(control)})
	if err != nil {
		t.Fatalf("encoding the record an event carries: %v", err)
	}
	return payload
}
