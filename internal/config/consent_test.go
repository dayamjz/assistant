package config

import (
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// allow_standing_consent is one row: trusted-only and false by default, so a
// repository allows standing consent only by putting the key on its default
// branch, and nothing allows it until somebody does.
func TestAllowStandingConsentIsATrustedOnlyRowDefaultingToFalse(t *testing.T) {
	trust, ok := TrustOf(KeyAllowStandingConsent)
	if !ok || trust != TrustTrusted {
		t.Fatalf("TrustOf(allow_standing_consent) = %v, %v; it must be one a pushed branch cannot set", trust, ok)
	}
	if Defaults().AllowStandingConsent {
		t.Fatal("standing consent is allowed by default, so a repository nobody configured allows it")
	}
	trusted := mustParse(t, OriginTrusted, `{"allow_standing_consent": true}`)
	if !resolve(t, Absent(OriginGlobal), trusted).Config.AllowStandingConsent {
		t.Fatal("the trusted copy allowed standing consent and the resolution does not")
	}
}

// A pushed branch can never allow standing consent for itself: the pushed
// copy's key is dropped at the merge and reported as a rejection, with the
// trusted copy's absence standing. The positive control is the same key
// from the trusted copy, which is taken, so the drop is about the origin and
// not about the key.
func TestAPushedBranchCannotAllowStandingConsent(t *testing.T) {
	principles.Cite(t, principles.P7)
	pushed := mustParse(t, OriginPushed, `{"allow_standing_consent": true}`)
	res, err := ResolveRun(Absent(OriginGlobal), Absent(OriginTrusted), pushed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.AllowStandingConsent {
		t.Fatal("a pushed copy allowed standing consent for its own run")
	}
	rejected := false
	for _, r := range res.Rejected {
		if r.Key == KeyAllowStandingConsent && r.Origin == OriginPushed {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the pushed key was dropped without a rejection: %v", res.Rejected)
	}
	// Opting the branch into commands does not reach this key either: the
	// opt-out covers the command class, and this key is not one.
	opted := mustParse(t, OriginTrusted, `{"allow_pushed_commands": true}`)
	if res, err := ResolveRun(Absent(OriginGlobal), opted, pushed); err != nil || res.Config.AllowStandingConsent {
		t.Fatalf("with pushed commands allowed, the pushed key resolved to %v (%v)", res.Config.AllowStandingConsent, err)
	}
	trusted := mustParse(t, OriginTrusted, `{"allow_standing_consent": true}`)
	if res, err := ResolveRun(Absent(OriginGlobal), trusted, pushed); err != nil || !res.Config.AllowStandingConsent {
		t.Fatalf("the trusted copy's key was not taken: %v, %v", res.Config.AllowStandingConsent, err)
	}
}

// The operator's own file allows standing consent too, and in every
// repository the home serves, because the key's trust class admits it. That
// is kept on purpose, by the owner's decision on #253: an operator setting it
// there has chosen it for all of them at once. A repository's trusted copy
// cannot take the global allowance away by leaving the key out, and the
// control is a global file that does not set it, which allows nothing.
func TestTheOperatorsFileAllowsStandingConsentInEveryRepository(t *testing.T) {
	global := mustParse(t, OriginGlobal, `{"allow_standing_consent": true}`)
	for name, trusted := range map[string]Layer{
		"no trusted copy":           Absent(OriginTrusted),
		"a trusted copy without it": mustParse(t, OriginTrusted, `{"no_ci": true}`),
		"a trusted copy setting it": mustParse(t, OriginTrusted, `{"allow_standing_consent": true}`),
	} {
		res, err := ResolveRun(global, trusted, Absent(OriginPushed))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !res.Config.AllowStandingConsent {
			t.Errorf("%s: the operator's file allowed standing consent and the resolution does not", name)
		}
	}
	silent := mustParse(t, OriginGlobal, `{"checks_timeout": "1h"}`)
	if res, err := ResolveRun(silent, Absent(OriginTrusted), Absent(OriginPushed)); err != nil || res.Config.AllowStandingConsent {
		t.Fatalf("a global file that does not set the key resolved to %v (%v)", res.Config.AllowStandingConsent, err)
	}
}
