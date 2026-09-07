package store

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Optional is a value that may be unknown, and it is how this package keeps the
// promise PRD section 8 makes about additive schema changes: a column added by
// a later migration reads back as unknown for the rows written before it
// existed, never as a fabricated zero.
//
// The distinction is not decoration. A run recorded before a field existed and
// a run where the field was genuinely zero are different facts, and a report
// that cannot tell them apart invents history. Optional makes the caller ask,
// because the value is only reachable through Get, which also returns whether
// it is there.
//
// The zero Optional is unknown, so a struct built by a caller who has not
// thought about a field carries "unknown" rather than a value nobody chose.
type Optional[T any] struct {
	value T
	known bool
}

// Known returns an Optional holding v.
func Known[T any](v T) Optional[T] { return Optional[T]{value: v, known: true} }

// Unknown returns an Optional holding nothing. It is the zero Optional, and is
// spelled out where a reader would otherwise have to recognize the zero value.
func Unknown[T any]() Optional[T] { return Optional[T]{} }

// Get returns the value and whether it is known. A caller that ignores the
// second result is reading a zero value it was never told to trust, so the
// value is reachable no other way.
func (o Optional[T]) Get() (T, bool) { return o.value, o.known }

// IsKnown reports whether a value is present.
func (o Optional[T]) IsKnown() bool { return o.known }

// Or returns the value when it is known and fallback when it is not. It is for
// a caller that has a defensible substitute, and it is deliberately explicit so
// that substituting one is a decision in the source rather than an accident.
func (o Optional[T]) Or(fallback T) T {
	if !o.known {
		return fallback
	}
	return o.value
}

// String renders the value, or "unknown" when there is none, so a formatted
// record cannot silently print a zero for an absent field.
func (o Optional[T]) String() string {
	if !o.known {
		return "unknown"
	}
	return fmt.Sprint(o.value)
}

// Value implements [driver.Valuer]. An unknown value is SQL NULL.
func (o Optional[T]) Value() (driver.Value, error) {
	if !o.known {
		return nil, nil
	}
	return sql.Null[T]{V: o.value, Valid: true}.Value()
}

// Scan implements [sql.Scanner]. SQL NULL is unknown, which is what a column
// added by a later migration holds for every row written before it existed.
func (o *Optional[T]) Scan(src any) error {
	if src == nil {
		*o = Optional[T]{}
		return nil
	}
	var n sql.Null[T]
	if err := n.Scan(src); err != nil {
		return err
	}
	*o = Optional[T]{value: n.V, known: n.Valid}
	return nil
}

// MarshalJSON writes a known value as the value itself and an unknown one as
// null.
//
// This type exists because a recorded zero and a value nobody recorded are
// different facts, so the encoding it crosses a boundary through has to keep
// them apart. Without this an Optional encodes as an empty object, because its
// fields are unexported, which loses both the value and the fact that there was
// one - the same defect agents.Count names for a count an agent did not report.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if !o.known {
		return []byte("null"), nil
	}
	return json.Marshal(o.value)
}

// UnmarshalJSON reads back what MarshalJSON wrote: null is unknown, and
// anything else is a known value of the underlying type. A value that does not
// decode is an error rather than an unknown, because a field that could not be
// read is not the same fact as a field nothing was recorded for.
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*o = Optional[T]{}
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*o = Optional[T]{value: v, known: true}
	return nil
}

// optionalTimeText converts an optional instant to the text form this package
// stores. Times are held as text rather than as a driver time value so that
// what a column holds does not depend on a driver's type mapping.
func optionalTimeText(o Optional[time.Time]) Optional[string] {
	t, ok := o.Get()
	if !ok {
		return Unknown[string]()
	}
	return Known(encodeTime(t))
}

// optionalTimeValue converts stored text back to an optional instant. Text that
// is present but unparsable is an error rather than an unknown, because
// reporting a corrupt row as "no value was recorded" is the fabrication this
// type exists to prevent.
func optionalTimeValue(o Optional[string], column string) (Optional[time.Time], error) {
	s, ok := o.Get()
	if !ok {
		return Unknown[time.Time](), nil
	}
	t, err := decodeTime(s)
	if err != nil {
		return Unknown[time.Time](), fmt.Errorf("store: column %s: %w", column, err)
	}
	return Known(t), nil
}

// optionalDurationValue converts stored nanoseconds back to a duration.
func optionalDurationValue(o Optional[int64]) Optional[time.Duration] {
	n, ok := o.Get()
	if !ok {
		return Unknown[time.Duration]()
	}
	return Known(time.Duration(n))
}

// optionalDurationNanos converts an optional duration to stored nanoseconds.
func optionalDurationNanos(o Optional[time.Duration]) Optional[int64] {
	d, ok := o.Get()
	if !ok {
		return Unknown[int64]()
	}
	return Known(int64(d))
}

// optionalIntValue narrows a stored 64-bit integer to an int.
func optionalIntValue(o Optional[int64]) Optional[int] {
	n, ok := o.Get()
	if !ok {
		return Unknown[int]()
	}
	return Known(int(n))
}

// optionalInt64 widens an optional int for storage.
func optionalInt64(o Optional[int]) Optional[int64] {
	n, ok := o.Get()
	if !ok {
		return Unknown[int64]()
	}
	return Known(int64(n))
}
