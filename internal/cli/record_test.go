package cli_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// recordValues returns every value a decoded JSON document holds under key,
// at any depth, so a field is found wherever an answer nests the record.
func recordValues(document any, key string) []any {
	var found []any
	switch v := document.(type) {
	case map[string]any:
		for k, child := range v {
			if k == key {
				found = append(found, child)
			}
			found = append(found, recordValues(child, key)...)
		}
	case []any:
		for _, child := range v {
			found = append(found, recordValues(child, key)...)
		}
	}
	return found
}

// The run record's current_head column is dead, and a run's last pushed head
// is owned by its checkpoint, so P14 keeps it off the wire: no --json answer
// that carries a run record, the run a start answers with and the run list,
// names current_head. The positive control is that the same documents carry
// the rest of the record: the run's identifier, and submitted_head equal to
// the commit the checkout stood on, read from git rather than from the
// product, so the search is shown to reach the record it says is clean.
func TestNoJSONRunRecordCarriesCurrentHead(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P14)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	head := git(t, subject, "rev-parse", "HEAD")

	started := run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s%s", started.code, started.stdout, started.stderr)
	}
	id := decodeRun(t, started.stdout).Record.ID
	listed := run(t, h, subject, "--json", "runs")
	if listed.code != machine.ExitOK {
		t.Fatalf("assistant runs exited %s:\n%s%s", listed.code, listed.stdout, listed.stderr)
	}

	for name, stdout := range map[string]string{"the started run": started.stdout, "the run list": listed.stdout} {
		var document any
		if err := json.Unmarshal([]byte(stdout), &document); err != nil {
			t.Fatalf("%s does not decode: %v\n%s", name, err, stdout)
		}
		if got := recordValues(document, "current_head"); len(got) != 0 {
			t.Errorf("%s carries current_head %v:\n%s", name, got, stdout)
		}
		heads := recordValues(document, "submitted_head")
		if len(heads) == 0 {
			t.Errorf("%s carries no submitted_head, so the search reached no record:\n%s", name, stdout)
		}
		for _, got := range heads {
			if got != head {
				t.Errorf("%s carries submitted_head %v, want the checkout's %s", name, got, head)
			}
		}
		named := false
		for _, got := range recordValues(document, "id") {
			named = named || got == id
		}
		if !named {
			t.Errorf("%s does not name the run %s:\n%s", name, id, stdout)
		}
	}
}
