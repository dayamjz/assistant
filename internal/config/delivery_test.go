package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// The delivery key is one row: trusted-only, defaulting to gated, and its two
// values are the closed set. The default is read through Defaults and through
// a resolution nobody set the key in, so a row whose default drifted from the
// constant fails here rather than at a task's intake.
func TestDeliveryIsATrustedOnlyRowDefaultingToGated(t *testing.T) {
	trust, ok := TrustOf(KeyDelivery)
	if !ok || trust != TrustTrusted {
		t.Fatalf("TrustOf(delivery) = %v, %v; PRD section 10 has the key trusted-only", trust, ok)
	}
	if got := Defaults().Delivery; got != DeliveryGated {
		t.Fatalf("the default delivery posture is %q, PRD section 10 says gated", got)
	}
	if got := resolve(t, Absent(OriginGlobal), mustParse(t, OriginTrusted, `{}`)).Config.Delivery; got != DeliveryGated {
		t.Fatalf("a trusted copy that sets nothing resolves delivery to %q", got)
	}
	for _, d := range Deliveries() {
		got := resolve(t, Absent(OriginGlobal), mustParse(t, OriginTrusted, `{"delivery": "`+string(d)+`"}`)).Config.Delivery
		if got != d {
			t.Errorf("the trusted copy set %q and the resolution holds %q", d, got)
		}
	}
	if got := Deliveries(); len(got) != 2 || got[0] != DeliveryGated || got[1] != DeliveryLocal {
		t.Fatalf("Deliveries() = %v", got)
	}
}

// A pushed branch cannot choose its own path: the pushed copy's delivery is
// dropped at the merge and reported as a rejection, and the trusted copy's
// value stands. The positive control is the same key from the trusted copy,
// which is taken, so the drop is about the origin and not about the key.
func TestAPushedBranchCannotChooseItsDeliveryPosture(t *testing.T) {
	principles.Cite(t, principles.P7)
	pushed := mustParse(t, OriginPushed, `{"delivery": "local"}`)
	res, err := ResolveRun(Absent(OriginGlobal), Absent(OriginTrusted), pushed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Delivery != DeliveryGated {
		t.Fatalf("a pushed copy set delivery to local and the resolution holds %q", res.Config.Delivery)
	}
	rejected := false
	for _, r := range res.Rejected {
		if r.Key == KeyDelivery && r.Origin == OriginPushed {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the pushed delivery was dropped without a rejection: %v", res.Rejected)
	}
	trusted := mustParse(t, OriginTrusted, `{"delivery": "local"}`)
	if res, err := ResolveRun(Absent(OriginGlobal), trusted, pushed); err != nil || res.Config.Delivery != DeliveryLocal {
		t.Fatalf("the trusted copy's local was not taken: %v, %v", res.Config.Delivery, err)
	}
}

// direct is refused at parse time, by name and on every origin, a pushed
// copy included, because section 10 has invalid configuration fail at parse
// even where the key would be dropped; so is any other value and any value
// that is not a string. ParseDelivery, the decoder an intake request is read
// with, refuses the same values for the same reasons.
func TestDirectAndUnknownPosturesAreRefusedAtParse(t *testing.T) {
	cases := []struct {
		value string
		says  string
	}{
		{`"direct"`, "deferred"},
		{`"fast"`, `expected "gated" or "local"`},
		{`""`, `expected "gated" or "local"`},
		{`"Gated"`, `expected "gated" or "local"`},
		{`true`, ""},
	}
	for _, c := range cases {
		for _, origin := range []Origin{OriginGlobal, OriginTrusted, OriginPushed} {
			_, err := Parse(origin, []byte(`{"delivery": `+c.value+`}`))
			var ke *KeyError
			if !errors.As(err, &ke) || ke.Key != KeyDelivery {
				t.Fatalf("delivery %s from %s: %v, want a refusal naming the key", c.value, origin, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("delivery %s from %s was refused as %q, which does not say %q", c.value, origin, err, c.says)
			}
		}
	}
	if _, err := ParseDelivery("direct"); err == nil || !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("ParseDelivery(direct) = %v", err)
	}
	for _, d := range Deliveries() {
		if got, err := ParseDelivery(string(d)); err != nil || got != d {
			t.Fatalf("ParseDelivery(%q) = %q, %v", d, got, err)
		}
	}
}

// Lighter is the order intake's tighten-only rule compares: local is lighter
// than gated and nothing is lighter than itself, and a posture outside the
// set is ordered against nothing.
func TestLighterOrdersLocalBelowGated(t *testing.T) {
	cases := []struct {
		a, b Delivery
		want bool
	}{
		{DeliveryLocal, DeliveryGated, true},
		{DeliveryGated, DeliveryLocal, false},
		{DeliveryGated, DeliveryGated, false},
		{DeliveryLocal, DeliveryLocal, false},
		{"direct", DeliveryGated, false},
		{DeliveryLocal, "direct", false},
	}
	for _, c := range cases {
		if got := c.a.Lighter(c.b); got != c.want {
			t.Errorf("%q.Lighter(%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
