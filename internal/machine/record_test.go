package machine_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// valuesAt returns every value a decoded JSON document holds under key, at any
// depth, so a field is found wherever an answer nests the record that carries
// it.
func valuesAt(document any, key string) []any {
	var found []any
	switch v := document.(type) {
	case map[string]any:
		for k, child := range v {
			if k == key {
				found = append(found, child)
			}
			found = append(found, valuesAt(child, key)...)
		}
	case []any:
		for _, child := range v {
			found = append(found, valuesAt(child, key)...)
		}
	}
	return found
}

// store.Run.CurrentHead is a column nothing in production writes, and a run's
// last pushed head is owned by its checkpoint, so P14 leaves the column no
// place on the wire: an answer carrying it would be a second record of that
// fact, null on every run. Every answer that composes the record is encoded
// with the column set to a known value, which is the case the old tag would
// have emitted even had something written it, and none carries the key or
// the value. The positive control is that the same documents do carry the
// record's other fields, submitted_head among them with the value given, so
// the search is shown to reach the records it says are clean.
func TestNoAnswerCarriesTheDeadCurrentHead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	const submitted, dead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cccccccccccccccccccccccccccccccccccccccc"
	record := store.Run{
		ID:            "a-run",
		RepositoryID:  "a-repository",
		Branch:        "a-change",
		SubmittedHead: submitted,
		Base:          "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CurrentHead:   store.Known(dead),
		Status:        store.RunRunning,
	}
	answers := map[string]any{
		"run":       machine.Run{Record: record},
		"runs":      machine.Runs{Runs: []store.Run{record, record}},
		"lifecycle": machine.Lifecycle{Active: []store.Run{record}},
	}
	for name, answer := range answers {
		var out bytes.Buffer
		if err := machine.NewEncoder(&out).Encode(answer); err != nil {
			t.Fatalf("encoding the %s answer: %v", name, err)
		}
		if bytes.Contains(out.Bytes(), []byte(dead)) {
			t.Errorf("the %s answer carries the dead column's value:\n%s", name, out.String())
		}
		var document any
		if err := json.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatalf("the %s answer does not decode: %v", name, err)
		}
		if got := valuesAt(document, "current_head"); len(got) != 0 {
			t.Errorf("the %s answer carries current_head %v:\n%s", name, got, out.String())
		}
		heads := valuesAt(document, "submitted_head")
		if len(heads) == 0 {
			t.Errorf("the %s answer carries no submitted_head, so the search reached no record:\n%s", name, out.String())
		}
		for _, head := range heads {
			if head != submitted {
				t.Errorf("the %s answer carries submitted_head %v, want %s", name, head, submitted)
			}
		}
		for _, field := range []string{"id", "repository_id", "branch", "base", "status", "approved_commit"} {
			if len(valuesAt(document, field)) == 0 {
				t.Errorf("the %s answer lost the record's %s:\n%s", name, field, out.String())
			}
		}
	}
}
