package store

import (
	"database/sql/driver"
	"testing"
	"time"
)

func TestZeroOptionalIsUnknown(t *testing.T) {
	var o Optional[string]
	if o.IsKnown() {
		t.Fatal("the zero Optional reports a value")
	}
	if o != Unknown[string]() {
		t.Fatal("the zero Optional is not Unknown")
	}
	if value, known := o.Get(); known || value != "" {
		t.Fatalf("Get on the zero Optional returned %q, %v", value, known)
	}
	if o.Or("fallback") != "fallback" {
		t.Fatal("Or on an unknown value did not return the fallback")
	}
	if o.String() != "unknown" {
		t.Fatalf("an unknown Optional renders as %q", o.String())
	}
}

// The distinction this type exists for: a known zero is not an unknown.
func TestKnownZeroIsNotUnknown(t *testing.T) {
	zero := Known(0)
	if !zero.IsKnown() {
		t.Fatal("a known zero reports as unknown")
	}
	if value, known := zero.Get(); !known || value != 0 {
		t.Fatalf("Get on a known zero returned %v, %v", value, known)
	}
	if zero.Or(99) != 0 {
		t.Fatal("Or on a known zero returned the fallback")
	}
	if zero.String() != "0" {
		t.Fatalf("a known zero renders as %q", zero.String())
	}
	if zero == Unknown[int]() {
		t.Fatal("a known zero compares equal to unknown")
	}
}

func TestOptionalRoundTripsThroughTheDriver(t *testing.T) {
	cases := []struct {
		name string
		in   Optional[string]
		want driver.Value
	}{
		{"unknown", Unknown[string](), nil},
		{"known empty", Known(""), ""},
		{"known value", Known("x"), "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.Value()
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Value returned %#v, want %#v", got, tc.want)
			}
			var back Optional[string]
			if err := back.Scan(got); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if back != tc.in {
				t.Fatalf("the round trip gave %#v, want %#v", back, tc.in)
			}
		})
	}
}

func TestOptionalTimeConversionsRefuseCorruptText(t *testing.T) {
	at := time.Date(2026, 9, 2, 12, 0, 0, 7, time.UTC)
	back, err := optionalTimeValue(optionalTimeText(Known(at)), "column")
	if err != nil {
		t.Fatalf("optionalTimeValue: %v", err)
	}
	got, known := back.Get()
	if !known || !got.Equal(at) {
		t.Fatalf("the round trip gave %v (known=%v), want %v", got, known, at)
	}

	if unknown, err := optionalTimeValue(Unknown[string](), "column"); err != nil || unknown.IsKnown() {
		t.Fatalf("an absent timestamp converted to %v, %v", unknown, err)
	}

	// Text that is present but unparsable is an error. Reporting it as "no
	// value was recorded" would be exactly the fabrication this type prevents.
	if _, err := optionalTimeValue(Known("not a timestamp"), "column"); err == nil {
		t.Fatal("optionalTimeValue accepted unparsable text as an absent value")
	}
}
