package ipc_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
)

// TestMethodTableIsWrittenDownHere states the access class of every method
// independently of the table, because access is the whole of what containment
// means at this boundary and a row changed by accident would open a call a
// validating agent must never reach.
func TestMethodTableIsWrittenDownHere(t *testing.T) {
	restricted := map[ipc.Method]bool{
		"health":           false,
		"status":           false,
		"runs.list":        false,
		"run.get":          false,
		"run.start":        true,
		"run.rerun":        true,
		"run.respond":      true,
		"run.cancel":       true,
		"stage.report":     false,
		"tasks.list":       false,
		"task.get":         false,
		"service.stop":     true,
		"service.restart":  true,
		"events.subscribe": false,
	}
	seen := map[ipc.Method]bool{}
	for _, spec := range ipc.Methods() {
		want, known := restricted[spec.Method]
		if !known {
			t.Errorf("the table serves %q, which this test does not state an access class for", spec.Method)
			continue
		}
		if seen[spec.Method] {
			t.Errorf("%q appears twice in the table", spec.Method)
		}
		seen[spec.Method] = true
		if got := spec.Access == ipc.AccessRestricted; got != want {
			t.Errorf("%q restricted = %v, want %v", spec.Method, got, want)
		}
		if spec.Summary == "" {
			t.Errorf("%q has no summary", spec.Method)
		}
	}
	for method := range restricted {
		if !seen[method] {
			t.Errorf("the table no longer serves %q", method)
		}
	}
}

func TestOnlySubscribeStreams(t *testing.T) {
	for _, spec := range ipc.Methods() {
		want := ipc.KindRequest
		if spec.Method == "events.subscribe" {
			want = ipc.KindStream
		}
		if spec.Kind != want {
			t.Errorf("%q kind = %v, want %v", spec.Method, spec.Kind, want)
		}
	}
}

func TestLookupRefusesAMethodThisBuildDoesNotServe(t *testing.T) {
	if _, ok := ipc.Lookup("run.obliterate"); ok {
		t.Error("Lookup accepted a method the table does not declare")
	}
	if _, ok := ipc.Lookup(""); ok {
		t.Error("Lookup accepted the empty method")
	}
	if spec, ok := ipc.Lookup("run.start"); !ok || spec.Access != ipc.AccessRestricted {
		t.Errorf("Lookup(run.start) = %+v, %v", spec, ok)
	}
}

func TestMethodsIsACopy(t *testing.T) {
	first := ipc.Methods()
	first[0].Access = ipc.AccessRestricted
	first[0].Method = "tampered"
	second := ipc.Methods()
	if second[0].Method == "tampered" || second[0].Access == ipc.AccessRestricted {
		t.Error("Methods returned the table itself, so a caller can rewrite what is restricted")
	}
}
