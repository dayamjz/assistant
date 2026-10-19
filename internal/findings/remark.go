package findings

import (
	"encoding/json"
	"strings"
)

// Remark is one entry of a report's Uncertainties or FollowUps: a short text
// in the stage's own words and, optionally, where it is about, on the terms a
// finding's Location is.
//
// A Remark is not a Finding, and the type is what keeps it from being read as
// one: it has no action and no severity, so nothing that decides from an
// action (Held, Fixable, AllNotes, the hold registration internal/tasks
// derives from a report) can select one. An uncertainty that needs a person's
// decision is an ask finding, never a remark, and a remark that arrives
// carrying an action is refused rather than read with the action dropped,
// because dropping it would turn what the writer meant as an ask into text
// that holds nothing. RemarkList.UnmarshalJSON records that an action was
// present and Validate refuses it with DefectActionOutsideFinding.
type Remark struct {
	// Text is what the stage could not settle or recommends doing next.
	Text string `json:"text"`
	// Location is where the remark is about, empty when it is about no file.
	// It is carried and never resolved against a filesystem.
	Location Location `json:"location,omitzero"`

	// action records that the remark arrived with an action key, whatever
	// its value. Only RemarkList.UnmarshalJSON sets it, so a remark built in
	// Go never carries one; it is unexported so it has no wire form and a
	// remark that carried it cannot survive Validate to be encoded.
	action bool
}

// RemarkList is a report's Uncertainties or FollowUps as a stage wrote them.
// It is read from untrusted output, so it decodes on the terms Traces already
// set here: a part of a report that decides nothing must not discard the
// findings around it.
type RemarkList []Remark

// UnmarshalJSON reads the shapes a stage writes a list of remarks in, and
// cannot fail:
//
//   - JSON null yields no remarks.
//   - A JSON list yields one remark per element. Any other value is read as a
//     list of that one element, so a single remark written without the list
//     is kept rather than lost.
//   - An element that is a JSON string is a remark with that text.
//   - An element that is a JSON object is read for "text" and "location",
//     each on its own terms: a text that is not a string and a location
//     Location cannot read are each left empty while the other is still read.
//     An object carrying an "action" key, compared the way encoding/json
//     matches a field name and whatever its value, is marked so Validate
//     refuses it.
//   - Every other element, an object out of which nothing was read, and a
//     marked object that carries nothing else, is a remark holding the JSON as
//     it was written as its text, so what the stage said stays visible rather
//     than becoming a gap. An object whose text was read as a string is read,
//     even when that string is blank, and Normalize then drops it as the
//     empty remark it is.
func (l *RemarkList) UnmarshalJSON(b []byte) error {
	text := strings.TrimSpace(string(b))
	if text == "null" {
		*l = nil
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(b, &list) != nil {
		list = []json.RawMessage{json.RawMessage(text)}
	}
	read := make(RemarkList, 0, len(list))
	for _, entry := range list {
		read = append(read, remarkValue(entry))
	}
	*l = read
	return nil
}

// remarkValue is one element as a remark, on the rules RemarkList.UnmarshalJSON
// states.
func remarkValue(raw json.RawMessage) Remark {
	written := strings.TrimSpace(string(raw))
	var s string
	if strings.HasPrefix(written, `"`) && json.Unmarshal(raw, &s) == nil {
		return Remark{Text: s}
	}
	var fields map[string]json.RawMessage
	if strings.HasPrefix(written, "{") && json.Unmarshal(raw, &fields) == nil {
		// The parts are read through encoding/json's own field matching, so
		// a key written twice resolves the way the decoder resolves it
		// everywhere else in a report; the map is asked only whether an
		// action key is present at all.
		var parts struct {
			Text     json.RawMessage `json:"text"`
			Location json.RawMessage `json:"location"`
		}
		var r Remark
		read := false
		if json.Unmarshal(raw, &parts) == nil {
			var t string
			if json.Unmarshal(parts.Text, &t) == nil {
				r.Text, read = t, true
			}
			var loc Location
			if len(parts.Location) > 0 && json.Unmarshal(parts.Location, &loc) == nil {
				r.Location, read = loc, read || !loc.Empty()
			}
		}
		for key := range fields {
			if strings.EqualFold(key, "action") {
				r.action = true
			}
		}
		if !read || (r.action && blankRemark(r)) {
			// Nothing was read, so the object as written is the text, and a
			// marked one keeps its mark for Validate to refuse. A text the
			// stage wrote blank was read, and is dropped by Normalize as the
			// empty remark it is.
			r.Text = written
		}
		return r
	}
	return Remark{Text: written}
}

// blankRemark reports whether a remark carries nothing once trimmed.
func blankRemark(r Remark) bool {
	return strings.TrimSpace(r.Text) == "" && strings.TrimSpace(r.Location.Path) == "" && r.Location.Line == 0
}

// normalizeRemarks trims each remark's text and location path, drops the
// ones that carry nothing at all, and preserves order and a nil input. A
// remark marked as carrying an action is never dropped, so Validate sees it.
// A negative line is left for Validate to refuse, as a finding's is.
func normalizeRemarks(list RemarkList) RemarkList {
	if list == nil {
		return nil
	}
	out := make(RemarkList, 0, len(list))
	for _, r := range list {
		r.Text = strings.TrimSpace(r.Text)
		r.Location.Path = strings.TrimSpace(r.Location.Path)
		if blankRemark(r) && !r.action {
			continue
		}
		out = append(out, r)
	}
	return out
}

// rewrittenRemarks is text over every string a list of remarks carries,
// copied, with a nil list staying nil.
func rewrittenRemarks(list RemarkList, text func(string) string) RemarkList {
	if list == nil {
		return nil
	}
	out := make(RemarkList, len(list))
	for i, r := range list {
		r.Text = text(r.Text)
		r.Location.Path = text(r.Location.Path)
		out[i] = r
	}
	return out
}
