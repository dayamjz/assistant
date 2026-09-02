package config

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Layer is one parsed configuration document together with the origin it was
// read from. Both halves are required: a value's admissibility depends on
// where its document came from, so this package will not accept a document
// without being told.
//
// A Layer records which keys the document actually set, which is what makes a
// repository layer override the global one key by key rather than section by
// section, and what distinguishes a key set to an empty value from a key
// nobody wrote. The zero Layer has no origin and is refused by Resolve; build
// one with Parse or Absent.
type Layer struct {
	origin  Origin
	present bool
	values  map[Key]any
}

// Absent returns the layer for a file that is not there. It is a valid layer
// that sets nothing, so every key falls through to the layer below or to its
// default.
//
// A missing file and an unreadable one are different cases and this package
// only models the first: a caller that cannot read or decode a trusted
// document must stop the run, per PRD section 10, rather than call this.
func Absent(origin Origin) Layer {
	return Layer{origin: origin}
}

// Parse decodes a configuration document read from origin. The format is JSON,
// with the dotted keys of the PRD section 10 schema written as nested objects,
// so "fix_rounds.review" is a "review" field inside a "fix_rounds" object.
//
// Every key is validated here, including keys this origin will not be allowed
// to set. A broken rule surfaces at parse time, naming the key and the value,
// rather than at the merge that would have discarded it.
//
// An empty document, meaning empty or whitespace-only input, is valid and sets
// nothing; it is present, unlike the layer Absent returns, and the two differ
// in what Present reports.
func Parse(origin Origin, data []byte) (Layer, error) {
	switch origin {
	case OriginTrusted, OriginPushed:
	default:
		return Layer{}, ErrUnknownOrigin
	}
	layer := Layer{origin: origin, present: true, values: map[Key]any{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return layer, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return Layer{}, &DocumentError{Detail: "the document is not valid JSON", Err: err}
	}
	if dec.More() {
		return Layer{}, &DocumentError{Detail: "the document has more than one top-level value"}
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return Layer{}, &DocumentError{Detail: "the document must be a JSON object"}
	}
	if err := walk("", obj, layer.values); err != nil {
		return Layer{}, err
	}
	return layer, nil
}

// walk flattens nested objects into dotted keys and decodes each leaf. Keys
// are visited in sorted order so a document with more than one problem always
// reports the same one.
func walk(prefix string, obj map[string]any, out map[Key]any) error {
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := obj[name]
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		if prefix == "" && sections[name] {
			sub, ok := value.(map[string]any)
			if !ok {
				return &KeyError{Key: Key(name), Value: jsonText(value), Detail: "expected an object"}
			}
			if err := walk(name, sub, out); err != nil {
				return err
			}
			continue
		}
		key := Key(full)
		s, known := specByKey[key]
		if !known {
			return &KeyError{Key: key, Detail: "unrecognized key"}
		}
		if value == nil {
			return &KeyError{Key: key, Value: "null", Detail: "a value is required; remove the key to inherit or use the default"}
		}
		decoded, err := s.decode(key, value)
		if err != nil {
			return err
		}
		out[key] = decoded
	}
	return nil
}

// Origin returns where this layer's document was read from.
func (l Layer) Origin() Origin { return l.origin }

// Present reports whether there was a document at all. A present document that
// sets nothing and an absent one resolve to the same configuration; they are
// distinguished so a caller can say which happened.
func (l Layer) Present() bool { return l.present }

// IsSet reports whether this document wrote the key. A key set to an empty
// value, such as an empty ignore list or an empty command, is set: it
// overrides the layer below rather than inheriting from it.
func (l Layer) IsSet(k Key) bool {
	_, ok := l.values[k]
	return ok
}

// SetKeys returns the keys this document wrote, in schema order.
func (l Layer) SetKeys() []Key {
	out := make([]Key, 0, len(l.values))
	for _, s := range specs {
		if _, ok := l.values[s.key]; ok {
			out = append(out, s.key)
		}
	}
	return out
}

// String describes the layer for a log line: its origin and how many keys it
// set.
func (l Layer) String() string {
	var b strings.Builder
	b.WriteString(l.origin.String())
	if !l.present {
		b.WriteString(" (no document)")
		return b.String()
	}
	b.WriteString(" (" + itoa(len(l.values)) + " keys set)")
	return b.String()
}
