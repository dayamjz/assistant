package config

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// notifications is one row: global-only and off by default, so the gateway
// stays off until the operator's own file turns it on. A repository file
// carrying it is refused in either copy (P7).
func TestNotificationsAreGlobalOnlyAndOffByDefault(t *testing.T) {
	principles.Cite(t, principles.P7)
	trust, ok := TrustOf(KeyNotifications)
	if !ok || trust != TrustGlobal {
		t.Fatalf("TrustOf(notifications) = %v, %v, want global-only", trust, ok)
	}
	if Defaults().Notifications {
		t.Fatal("Notifications default is on")
	}
	doc := `{"notifications": true}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		_, err := Parse(origin, []byte(doc))
		if err == nil {
			t.Fatalf("a %s repository file turned the gateway on", origin)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Parse(%s) = %v, want ErrInvalid", origin, err)
		}
	}
	c := resolve(t, mustParse(t, OriginGlobal, doc), Absent(OriginTrusted)).Config
	if !c.Notifications {
		t.Fatal("the operator's file did not turn the gateway on")
	}
	off := resolve(t, mustParse(t, OriginGlobal, `{"notifications": false}`), Absent(OriginTrusted)).Config
	if off.Notifications {
		t.Fatal("the operator's file could not leave the gateway off")
	}
}
