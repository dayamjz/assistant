package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Kind is the type of value a state key holds. The set is closed on purpose: a
// checkpoint must deserialize as data and nothing else, so every kind decodes
// into one fixed Go type and no input can select another. Adding a kind is a
// deliberate change to the trust boundary, not a convenience.
type Kind uint8

const (
	// KindInvalid is the zero Kind. It is never a valid declaration.
	KindInvalid Kind = iota
	// KindText is a string.
	KindText
	// KindInt is a signed 64-bit integer.
	KindInt
	// KindBool is a boolean.
	KindBool
	// KindList is an ordered list of strings.
	KindList
)

// String returns the wire name of the kind, which is also what appears in
// construction and validation errors.
func (k Kind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindInt:
		return "int"
	case KindBool:
		return "bool"
	case KindList:
		return "list"
	case KindInvalid:
		return "invalid"
	default:
		return "kind(" + strconv.Itoa(int(k)) + ")"
	}
}

// parseKind maps a wire name back to a Kind. An unrecognized name is refused
// rather than defaulted, because a checkpoint is untrusted input.
func parseKind(s string) (Kind, error) {
	switch s {
	case "text":
		return KindText, nil
	case "int":
		return KindInt, nil
	case "bool":
		return KindBool, nil
	case "list":
		return KindList, nil
	default:
		return KindInvalid, fmt.Errorf("unrecognized value kind %q", s)
	}
}

// Value is one typed state value. The zero Value has KindInvalid and is not a
// legal state value; use TextValue, IntValue, BoolValue, ListValue, or the
// zero value of a declared kind produced by Graph.NewState.
type Value struct {
	kind Kind
	text string
	num  int64
	flag bool
	list []string
}

// TextValue returns a Value of KindText holding s.
func TextValue(s string) Value { return Value{kind: KindText, text: s} }

// IntValue returns a Value of KindInt holding n.
func IntValue(n int64) Value { return Value{kind: KindInt, num: n} }

// BoolValue returns a Value of KindBool holding b.
func BoolValue(b bool) Value { return Value{kind: KindBool, flag: b} }

// ListValue returns a Value of KindList holding a copy of items. The copy is
// what makes a Value safe to hand to a node without it aliasing run state.
func ListValue(items ...string) Value {
	out := make([]string, len(items))
	copy(out, items)
	return Value{kind: KindList, list: out}
}

// zeroValue returns the empty value of kind k. Every declared key holds this
// until a node writes it, so a read of a declared key is always defined.
func zeroValue(k Kind) Value {
	switch k {
	case KindText:
		return TextValue("")
	case KindInt:
		return IntValue(0)
	case KindBool:
		return BoolValue(false)
	case KindList:
		return ListValue()
	case KindInvalid:
		return Value{}
	default:
		return Value{}
	}
}

// Kind reports the type of the value.
func (v Value) Kind() Kind { return v.kind }

// Text returns the string this value holds, and false if it is not KindText.
func (v Value) Text() (string, bool) {
	if v.kind != KindText {
		return "", false
	}
	return v.text, true
}

// Int returns the integer this value holds, and false if it is not KindInt.
func (v Value) Int() (int64, bool) {
	if v.kind != KindInt {
		return 0, false
	}
	return v.num, true
}

// Bool returns the boolean this value holds, and false if it is not KindBool.
func (v Value) Bool() (bool, bool) {
	if v.kind != KindBool {
		return false, false
	}
	return v.flag, true
}

// List returns a copy of the list this value holds, and false if it is not
// KindList. The copy prevents a caller from mutating run state through it.
func (v Value) List() ([]string, bool) {
	if v.kind != KindList {
		return nil, false
	}
	out := make([]string, len(v.list))
	copy(out, v.list)
	return out, true
}

// Equal reports whether two values have the same kind and the same contents.
// It is the comparison the convergence check is built on.
func (v Value) Equal(other Value) bool {
	if v.kind != other.kind {
		return false
	}
	switch v.kind {
	case KindText:
		return v.text == other.text
	case KindInt:
		return v.num == other.num
	case KindBool:
		return v.flag == other.flag
	case KindList:
		if len(v.list) != len(other.list) {
			return false
		}
		for i := range v.list {
			if v.list[i] != other.list[i] {
				return false
			}
		}
		return true
	case KindInvalid:
		return true
	default:
		return false
	}
}

// String renders the value for diagnostics. It is not the wire format.
func (v Value) String() string {
	switch v.kind {
	case KindText:
		return strconv.Quote(v.text)
	case KindInt:
		return strconv.FormatInt(v.num, 10)
	case KindBool:
		return strconv.FormatBool(v.flag)
	case KindList:
		return "[" + strings.Join(v.list, " ") + "]"
	case KindInvalid:
		return "<invalid>"
	default:
		return "<invalid>"
	}
}

// valueJSON is the wire shape of a Value: a kind tag and a payload decoded
// into the one Go type that tag names.
type valueJSON struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

// MarshalJSON writes the value as a tagged scalar. Encoding is deterministic
// so a state fingerprint can be compared byte for byte.
func (v Value) MarshalJSON() ([]byte, error) {
	var payload any
	switch v.kind {
	case KindText:
		payload = v.text
	case KindInt:
		payload = v.num
	case KindBool:
		payload = v.flag
	case KindList:
		if v.list == nil {
			payload = []string{}
		} else {
			payload = v.list
		}
	case KindInvalid:
		return nil, fmt.Errorf("cannot encode a value of kind %s", v.kind)
	default:
		return nil, fmt.Errorf("cannot encode a value of kind %s", v.kind)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode value payload: %w", err)
	}
	return json.Marshal(valueJSON{Kind: v.kind.String(), Value: raw})
}

// UnmarshalJSON reads a tagged scalar. An unknown kind, an unknown field, or a
// payload that does not match the tag is refused, so a hand-edited checkpoint
// cannot smuggle a value the graph never declared.
func (v *Value) UnmarshalJSON(b []byte) error {
	var raw valueJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("decode value: %w", err)
	}
	kind, err := parseKind(raw.Kind)
	if err != nil {
		return err
	}
	payload := json.NewDecoder(bytes.NewReader(raw.Value))
	payload.DisallowUnknownFields()
	switch kind {
	case KindText:
		var s string
		if err := payload.Decode(&s); err != nil {
			return fmt.Errorf("decode text value: %w", err)
		}
		*v = TextValue(s)
	case KindInt:
		var n int64
		if err := payload.Decode(&n); err != nil {
			return fmt.Errorf("decode int value: %w", err)
		}
		*v = IntValue(n)
	case KindBool:
		var f bool
		if err := payload.Decode(&f); err != nil {
			return fmt.Errorf("decode bool value: %w", err)
		}
		*v = BoolValue(f)
	case KindList:
		var items []string
		if err := payload.Decode(&items); err != nil {
			return fmt.Errorf("decode list value: %w", err)
		}
		*v = ListValue(items...)
	case KindInvalid:
		return fmt.Errorf("unrecognized value kind %q", raw.Kind)
	default:
		return fmt.Errorf("unrecognized value kind %q", raw.Kind)
	}
	return nil
}
