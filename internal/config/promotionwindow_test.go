package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// PRD principle P7 for promotion_window: how long a reported investigation's
// worker is kept idle on this machine is the operator's, so a repository
// copy, trusted or pushed, naming it is refused when it is parsed, naming
// the key and saying it is global-only. The operator's own file sets it, and
// a run resolved over a pushed copy that sets other keys keeps the
// operator's value, so no branch can keep a worker alive longer or end one
// sooner.
func TestARepositoryCopyCannotNameThePromotionWindow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	doc := `{"promotion_window": "24h"}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		_, err := Parse(origin, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyPromotionWindow || !strings.Contains(err.Error(), "global-only") {
			t.Errorf("Parse(%v, %s) = %v, want a global-only refusal of %s", origin, doc, err, KeyPromotionWindow)
		}
	}
	global := mustParse(t, OriginGlobal, `{"promotion_window": "5m"}`)
	pushed := mustParse(t, OriginPushed, `{"fix_rounds": {"test": 1}}`)
	if got := resolveRun(t, global, Absent(OriginTrusted), pushed).Config.PromotionWindow; got != 5*time.Minute {
		t.Errorf("a run resolved the promotion window to %v, want the operator's 5m", got)
	}
}
