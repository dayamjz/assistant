package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// ParseYAML decodes a configuration document written in YAML, read from
// origin, on exactly Parse's terms once the document is read. It is the
// format of the operator's own document, which PRD section 8 names
// config.yaml in the home; the repository document, RepositoryDocument, is
// JSON and is read with Parse.
//
// The YAML is reduced to the value tree Parse's JSON decoding produces -
// objects, lists, text, numbers, true and false, and null - and then every
// key is validated by the same walk Parse runs, so an unrecognized key, a
// null, a flat dotted name, a global-only key in a repository file and every
// value the key table refuses are refused here the same way and with the
// same refusal. A global document written as JSON, which was this file's
// format before, reads as it did, with one exception known: the escape \/,
// which JSON allows and the YAML reader does not, refuses the document as
// malformed rather than reading it some other way.
//
// What YAML adds is refused wherever it would let a document mean something
// other than what it plainly says, or say one thing two ways:
//
//   - A key repeated within one mapping is refused at any depth, naming
//     where it stands, as Parse refuses a repeated JSON member. The YAML
//     library is used only to build the document tree, and the tree keeps
//     every repetition, so nothing has already resolved one to a winner.
//   - A value is typed by what YAML's core schema resolves it to and is
//     never converted, so yes, on and a quoted "3" stay text, and a key
//     expecting true or false or a number refuses them as Parse refuses the
//     JSON text "3".
//   - A number must be written as a JSON number. Hexadecimal, octal, a
//     leading plus sign, digit separators, infinity and not-a-number are
//     refused rather than converted.
//   - An explicit tag, an anchor, an alias and a merge key are refused, and
//     so are a timestamp and binary data, which have no JSON value. A key
//     must be plain text.
//   - A stream holding more than one document is refused, and so is a
//     document whose top level is not a mapping.
//
// A document with no content - empty input, or only whitespace and comments
// - is valid and sets nothing, and is present in Present's sense.
func ParseYAML(origin Origin, data []byte) (Layer, error) {
	switch origin {
	case OriginGlobal, OriginTrusted, OriginPushed:
	default:
		return Layer{}, ErrUnknownOrigin
	}
	layer := Layer{origin: origin, present: true, values: map[Key]any{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return layer, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return layer, nil
		}
		return Layer{}, &DocumentError{Detail: "the document is not valid YAML", Err: err}
	}
	var more yaml.Node
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		if err != nil {
			return Layer{}, &DocumentError{Detail: "the document is not valid YAML", Err: err}
		}
		return Layer{}, &DocumentError{Detail: "the stream holds more than one YAML document"}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return Layer{}, &DocumentError{Detail: "the document has no single top-level value"}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return Layer{}, &DocumentError{Detail: "the document must be a YAML mapping"}
	}
	value, err := fromYAML(root, "")
	if err != nil {
		return Layer{}, err
	}
	if err := walk(origin, "", value.(map[string]any), layer.values); err != nil {
		return Layer{}, err
	}
	return layer, nil
}

// jsonNumber is the grammar of a JSON number. A YAML number is accepted only
// when written this way, so the number a reader sees is the number decoded.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// fromYAML reduces one YAML node to the value Parse's JSON decoding would
// have produced for the same content, or refuses it. at is where the node
// stands, in the form KeyError names a repeated member with, so a refusal
// says where it is.
func fromYAML(n *yaml.Node, at string) (any, error) {
	if err := plain(n, at); err != nil {
		return nil, err
	}
	switch n.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			name := k.Value
			full := name
			if at != "" {
				full = at + "." + name
			}
			if err := plain(k, full); err != nil {
				return nil, err
			}
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return nil, &KeyError{Key: Key(full),
					Detail: "a key must be plain text, and this one is a YAML " + k.ShortTag()}
			}
			if _, seen := out[name]; seen {
				return nil, &KeyError{
					Key:    Key(full),
					Detail: "the key appears more than once in this object; a repeated key is refused rather than resolved to one of its values",
				}
			}
			value, err := fromYAML(v, full)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for i, item := range n.Content {
			value, err := fromYAML(item, at+"["+itoa(i)+"]")
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case yaml.ScalarNode:
		switch tag := n.ShortTag(); tag {
		case "!!str":
			return n.Value, nil
		case "!!null":
			return nil, nil
		case "!!bool":
			return n.Value == "true" || n.Value == "True" || n.Value == "TRUE", nil
		case "!!int", "!!float":
			if !jsonNumber.MatchString(n.Value) {
				return nil, &KeyError{Key: Key(at), Value: jsonText(n.Value),
					Detail: "a number is written in decimal as JSON writes one; quote it if it is meant as text"}
			}
			return json.Number(n.Value), nil
		default:
			return nil, &KeyError{Key: Key(at), Value: jsonText(n.Value),
				Detail: "the value is a YAML " + tag + ", which no key takes; quote it if it is meant as text"}
		}
	}
	return nil, &KeyError{Key: Key(at), Detail: "the value is not a mapping, a list or a scalar"}
}

// plain refuses the YAML features that would let a node mean something other
// than its own text: an explicit tag, an anchor, an alias, and a merge key.
func plain(n *yaml.Node, at string) error {
	switch {
	case n.Kind == yaml.AliasNode:
		return &KeyError{Key: Key(at), Value: jsonText("*" + n.Value),
			Detail: "an alias is refused; write the value out where it is used"}
	case n.Anchor != "":
		return &KeyError{Key: Key(at), Value: jsonText("&" + n.Anchor),
			Detail: "an anchor is refused; a value means what is written where it stands"}
	case n.Style&yaml.TaggedStyle != 0:
		return &KeyError{Key: Key(at), Value: jsonText(n.Tag),
			Detail: "an explicit tag is refused; a value is typed by how it is written"}
	case n.Kind == yaml.ScalarNode && n.ShortTag() == "!!merge":
		return &KeyError{Key: Key(at), Value: jsonText(n.Value),
			Detail: "a merge key is refused; write each key out where it is used"}
	}
	return nil
}
