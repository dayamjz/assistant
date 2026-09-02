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
// Nesting is the only accepted spelling: a member name written in the flat
// dotted form, "fix_rounds.review", is refused, and so is a member name
// repeated within one object.
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
	if err := checkNoRepeatedNames(data); err != nil {
		return Layer{}, err
	}
	if err := walk("", obj, layer.values); err != nil {
		return Layer{}, err
	}
	return layer, nil
}

// checkNoRepeatedNames refuses a member name that appears more than once in
// the same object, at any depth of the document. JSON decoding keeps the last
// of a repeated member and discards the rest, so a document that reads one way
// to whoever reviews the diff would take effect another way. This
// configuration decides which commands run with the repository owner's
// credentials and can arrive from a contributor's branch, so a file that means
// two things at once is refused rather than resolved to one of them.
//
// It rescans the raw bytes at the token level, because the decoded value has
// already lost the repetition. Parse decoded the same bytes successfully
// first, so a scanning error here is not reachable and is reported as no
// repetition rather than as a second, differently worded refusal for a
// document that was already accepted as well formed.
func checkNoRepeatedNames(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var stack []*jsonFrame
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		var top *jsonFrame
		if n := len(stack); n > 0 {
			top = stack[n-1]
		}
		if top != nil && top.object && top.wantName {
			if _, isDelim := tok.(json.Delim); isDelim {
				stack = stack[:len(stack)-1]
				valueDone(stack)
				continue
			}
			name, _ := tok.(string)
			if top.seen[name] {
				return &KeyError{
					Key:    Key(namePath(stack, name)),
					Detail: "the key appears more than once in this object; a repeated key is refused rather than resolved to one of its values",
				}
			}
			top.seen[name] = true
			top.name = name
			top.wantName = false
			continue
		}
		d, isDelim := tok.(json.Delim)
		switch {
		case isDelim && d == '{':
			stack = append(stack, &jsonFrame{object: true, wantName: true, seen: map[string]bool{}})
		case isDelim && d == '[':
			stack = append(stack, &jsonFrame{})
		case isDelim:
			stack = stack[:len(stack)-1]
			valueDone(stack)
		default:
			valueDone(stack)
		}
	}
}

// jsonFrame is one open JSON container during that scan. An object frame
// collects the names it has seen and remembers the one whose value is being
// read; an array frame counts the elements it has finished, so a refusal can
// say which element it is about.
type jsonFrame struct {
	object   bool
	wantName bool
	seen     map[string]bool
	name     string
	index    int
}

// valueDone records that one complete value was read inside the innermost
// container, so an object expects a name next and an array moves to its next
// element.
func valueDone(stack []*jsonFrame) {
	n := len(stack)
	if n == 0 {
		return
	}
	if top := stack[n-1]; top.object {
		top.wantName = true
	} else {
		top.index++
	}
}

// namePath renders where a repeated name sits, as the enclosing member names
// joined by "." with a list element written as an index, so "no_ci" and
// "review.path_rules[0].paths" both name themselves.
func namePath(stack []*jsonFrame, name string) string {
	var b strings.Builder
	for _, f := range stack[:len(stack)-1] {
		if !f.object {
			b.WriteString("[" + itoa(f.index) + "]")
			continue
		}
		if f.name == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(f.name)
	}
	if b.Len() > 0 {
		b.WriteByte('.')
	}
	b.WriteString(name)
	return b.String()
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
		if strings.Contains(name, ".") {
			return flatKeyError(prefix, name)
		}
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

// flatKeyError refuses a member name written in the flat dotted form. A dotted
// key has exactly one spelling here, because the flat form and the nested form
// can both appear in one document and the parser would silently keep one of
// them. Configuration decides which commands run with the repository owner's
// credentials and can arrive from a contributor's branch, so a document that
// reads one way to a human and another way to the parser is refused rather
// than resolved.
//
// The refusal names both spellings, so an author can see what they wrote and
// what to write instead.
func flatKeyError(prefix, name string) error {
	full := name
	if prefix != "" {
		full = prefix + "." + name
	}
	return &KeyError{
		Key:    Key(full),
		Detail: "the key is written in the flat dotted form " + quote(name) + "; write it as nested objects, " + nestedForm(name),
	}
}

// nestedForm sketches the nested spelling a dotted name has to be written as.
func nestedForm(name string) string {
	parts := strings.Split(name, ".")
	out := "..."
	for i := len(parts) - 1; i >= 0; i-- {
		out = "{" + quote(parts[i]) + ": " + out + "}"
	}
	return out
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
