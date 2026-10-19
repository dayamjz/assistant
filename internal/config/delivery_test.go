package config

import (
	"errors"
	"strings"
	"testing"
)

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
