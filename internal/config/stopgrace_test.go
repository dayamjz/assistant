package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// PRD principle P7 for stop_grace: the bound on how long giving up a home may
// take is the operator's, so a repository copy, trusted or pushed, naming it
// is refused when it is parsed, naming the key and saying it is global-only.
// The operator's own file sets it, and a run resolved over a pushed copy that
// sets other keys keeps the operator's value, so no branch can lengthen or
// shorten how long the service waits before it ends what it started by force.
func TestARepositoryCopyCannotNameTheStopGrace(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	doc := `{"stop_grace": "10m"}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		_, err := Parse(origin, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyStopGrace || !strings.Contains(err.Error(), "global-only") {
			t.Errorf("Parse(%v, %s) = %v, want a global-only refusal of %s", origin, doc, err, KeyStopGrace)
		}
	}
	global := mustParse(t, OriginGlobal, `{"stop_grace": "5s"}`)
	pushed := mustParse(t, OriginPushed, `{"fix_rounds": {"test": 1}}`)
	if got := resolveRun(t, global, Absent(OriginTrusted), pushed).Config.StopGrace; got != 5*time.Second {
		t.Errorf("a run resolved the stop grace to %v, want the operator's 5s", got)
	}
}
