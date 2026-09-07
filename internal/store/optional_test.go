package store

import (
	"database/sql/driver"
	"encoding/json"
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

// A record that crosses a boundary as JSON has to carry the difference between
// a recorded value and nothing recorded, which is the whole reason this type
// exists. An Optional whose fields are unexported would otherwise encode as an
// empty object and lose both.
func TestAnOptionalKeepsTheDifferenceAcrossJSON(t *testing.T) {
	t.Parallel()
	known, err := json.Marshal(Known("a value"))
	if err != nil {
		t.Fatalf("encoding a known value: %v", err)
	}
	if string(known) != `"a value"` {
		t.Fatalf("a known value encoded as %s", known)
	}
	unknown, err := json.Marshal(Unknown[string]())
	if err != nil {
		t.Fatalf("encoding an unknown value: %v", err)
	}
	if string(unknown) != "null" {
		t.Fatalf("an unknown value encoded as %s", unknown)
	}

	var back Optional[string]
	if err := json.Unmarshal(known, &back); err != nil {
		t.Fatalf("decoding a known value: %v", err)
	}
	if v, ok := back.Get(); !ok || v != "a value" {
		t.Fatalf("a known value decoded as (%q, %v)", v, ok)
	}
	if err := json.Unmarshal(unknown, &back); err != nil {
		t.Fatalf("decoding an unknown value: %v", err)
	}
	if _, ok := back.Get(); ok {
		t.Fatal("an unknown value decoded as known")
	}
}

// A recorded zero is a value, and it must not come back as unknown.
func TestARecordedZeroIsNotAnUnknown(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(Known(0))
	if err != nil {
		t.Fatalf("encoding a recorded zero: %v", err)
	}
	var back Optional[int]
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("decoding a recorded zero: %v", err)
	}
	if v, ok := back.Get(); !ok || v != 0 {
		t.Fatalf("a recorded zero decoded as (%d, %v)", v, ok)
	}
}

// A value that is present but does not decode is a failure rather than an
// unknown, on the same terms as a stored time this package cannot parse.
func TestAValueThatDoesNotDecodeIsRefusedRatherThanReadAsUnknown(t *testing.T) {
	t.Parallel()
	var back Optional[time.Time]
	if err := json.Unmarshal([]byte(`"not a time"`), &back); err == nil {
		t.Fatal("a value that does not decode was accepted")
	}
}
