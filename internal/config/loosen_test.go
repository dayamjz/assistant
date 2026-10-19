package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// The delivery key is one row: global-only to loosen (#304), defaulting to
// gated, and its two values are the closed set. The default is read through
// Defaults and through a resolution nobody set the key in, so a row whose
// default drifted from the constant fails here rather than at a task's
// intake; every value the operator's own file names is taken.
func TestDeliveryIsAGlobalOnlyToLoosenRowDefaultingToGated(t *testing.T) {
	trust, ok := TrustOf(KeyDelivery)
	if !ok || trust != TrustGlobalLoosens {
		t.Fatalf("TrustOf(delivery) = %v, %v; the owner's decision on #304 has it global-only to loosen", trust, ok)
	}
	if TrustGlobalLoosens.String() != "global-only-to-loosen" {
		t.Errorf("TrustGlobalLoosens.String() = %q", TrustGlobalLoosens.String())
	}
	if got := Defaults().Delivery; got != DeliveryGated {
		t.Fatalf("the default delivery posture is %q, PRD section 10 says gated", got)
	}
	if got := resolve(t, Absent(OriginGlobal), mustParse(t, OriginTrusted, `{}`)).Config.Delivery; got != DeliveryGated {
		t.Fatalf("a trusted copy that sets nothing resolves delivery to %q", got)
	}
	for _, d := range Deliveries() {
		global := mustParse(t, OriginGlobal, `{"delivery": "`+string(d)+`"}`)
		if got := resolve(t, global, Absent(OriginTrusted)).Config.Delivery; got != d {
			t.Errorf("the operator's file set %q and the resolution holds %q", d, got)
		}
	}
	if got := Deliveries(); len(got) != 2 || got[0] != DeliveryGated || got[1] != DeliveryLocal {
		t.Fatalf("Deliveries() = %v", got)
	}
}

// Every TrustGlobalLoosens row's value type carries the order ResolveRun
// compares, and no row of another class does, so a key moved into the class
// without an order fails here rather than panicking in a resolution. The
// delivery row is the positive control: the class has a member, so the walk
// looked at something.
func TestEveryGlobalOnlyToLoosenRowIsOrdered(t *testing.T) {
	members := 0
	for _, s := range specs {
		_, isOrdered := s.def.(ordered)
		switch {
		case s.trust == TrustGlobalLoosens && !isOrdered:
			t.Errorf("%s is %s and its default %T carries no order", s.key, s.trust, s.def)
		case s.trust != TrustGlobalLoosens && isOrdered:
			t.Errorf("%s is %s and its default %T carries an order nothing compares", s.key, s.trust, s.def)
		case s.trust == TrustGlobalLoosens:
			members++
		}
	}
	if members == 0 {
		t.Fatal("no row is TrustGlobalLoosens, so this walk checked nothing")
	}
}

// loosened asserts err is the typed refusal naming the delivery key, the
// layer that loosened, both postures, and where the one beneath came from,
// and that the resolution carried nothing.
func loosened(t *testing.T, res Resolution, err error, origin, beneathOrigin Origin, beneathIsDefault bool) {
	t.Helper()
	var le *LoosenError
	if !errors.As(err, &le) || !errors.Is(err, ErrLoosened) {
		t.Fatalf("the resolution answered %q (%v), want a *LoosenError", res.Config.Delivery, err)
	}
	if errors.Is(err, ErrInvalid) {
		t.Errorf("a loosening is a refused composition and not an invalid value: %v", err)
	}
	want := LoosenError{Key: KeyDelivery, Origin: origin, Value: "local", Beneath: "gated",
		BeneathOrigin: beneathOrigin, BeneathIsDefault: beneathIsDefault}
	if *le != want {
		t.Errorf("the refusal is %+v, want %+v", *le, want)
	}
	for _, says := range []string{"delivery", `"local"`, `"gated"`, "the " + origin.String() + " layer"} {
		if !strings.Contains(err.Error(), says) {
			t.Errorf("the refusal %q does not say %s", err, says)
		}
	}
	if res.Config.Delivery != "" || res.Rejected != nil {
		t.Errorf("a refused resolution carried %+v", res)
	}
}

// A commit on the default branch cannot turn a lighter path on: a trusted
// copy naming local over the operator's gated, or over the default when the
// operator's file says nothing, refuses the resolution naming the trusted
// layer, and no posture is assumed in its place (P7, #304). The positive
// controls are the same trusted copy over an operator who chose local, which
// is taken, and a trusted gated over the operator's local, which tightens.
func TestATrustedCopyMayTightenDeliveryAndNeverLoosenIt(t *testing.T) {
	principles.Cite(t, principles.P7)
	gated := mustParse(t, OriginGlobal, `{"delivery": "gated"}`)
	local := mustParse(t, OriginGlobal, `{"delivery": "local"}`)
	trustedLocal := mustParse(t, OriginTrusted, `{"delivery": "local"}`)
	trustedGated := mustParse(t, OriginTrusted, `{"delivery": "gated"}`)

	res, err := ResolveRun(gated, trustedLocal, Absent(OriginPushed))
	loosened(t, res, err, OriginTrusted, OriginGlobal, false)
	res, err = ResolveRun(Absent(OriginGlobal), trustedLocal, Absent(OriginPushed))
	loosened(t, res, err, OriginTrusted, OriginGlobal, true)
	res, err = Resolve(gated, trustedLocal)
	loosened(t, res, err, OriginTrusted, OriginGlobal, false)

	if got := resolveRun(t, local, trustedGated, Absent(OriginPushed)).Config.Delivery; got != DeliveryGated {
		t.Errorf("a trusted gated over the operator's local resolves %q, want the tightened gated", got)
	}
	if got := resolveRun(t, local, trustedLocal, Absent(OriginPushed)).Config.Delivery; got != DeliveryLocal {
		t.Errorf("a trusted local over the operator's local resolves %q, want local", got)
	}
	if got := resolveRun(t, local, Absent(OriginTrusted), Absent(OriginPushed)).Config.Delivery; got != DeliveryLocal {
		t.Errorf("the operator's local alone resolves %q, want local", got)
	}
}

// A pushed branch may tighten delivery for its own run and never loosen it:
// over the operator's gated, over the default, or over a trusted gated that
// tightened the operator's local, a pushed local refuses the resolution
// naming the pushed layer and what it would have loosened. A pushed gated
// over the operator's local is taken, and so is a pushed local over an
// operator and a trusted copy that are both local, so the refusal is about
// the order and not about the origin. Nothing is dropped as a rejection.
func TestAPushedCopyMayTightenDeliveryAndNeverLoosenIt(t *testing.T) {
	principles.Cite(t, principles.P7)
	gated := mustParse(t, OriginGlobal, `{"delivery": "gated"}`)
	local := mustParse(t, OriginGlobal, `{"delivery": "local"}`)
	pushedLocal := mustParse(t, OriginPushed, `{"delivery": "local"}`)
	pushedGated := mustParse(t, OriginPushed, `{"delivery": "gated"}`)

	res, err := ResolveRun(gated, Absent(OriginTrusted), pushedLocal)
	loosened(t, res, err, OriginPushed, OriginGlobal, false)
	res, err = ResolveRun(local, mustParse(t, OriginTrusted, `{"delivery": "gated"}`), pushedLocal)
	loosened(t, res, err, OriginPushed, OriginTrusted, false)
	res, err = Resolve(Absent(OriginGlobal), pushedLocal)
	loosened(t, res, err, OriginPushed, OriginGlobal, true)

	tightened := resolveRun(t, local, Absent(OriginTrusted), pushedGated)
	if tightened.Config.Delivery != DeliveryGated || len(tightened.Rejected) != 0 {
		t.Errorf("a pushed gated over the operator's local resolves %q with %v, want gated and nothing dropped",
			tightened.Config.Delivery, tightened.Rejected)
	}
	held := resolveRun(t, local, mustParse(t, OriginTrusted, `{"delivery": "local"}`), pushedLocal)
	if held.Config.Delivery != DeliveryLocal || len(held.Rejected) != 0 {
		t.Errorf("a pushed local over a local operator and trusted copy resolves %q with %v",
			held.Config.Delivery, held.Rejected)
	}
}
