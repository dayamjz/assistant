package service_test

import (
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
)

// Nothing the worker paths export reaches a fixer session: the service's
// own exported surface, the worker group set and its membership, and the
// shapes the four worker-facing methods answer with. The walk is asked of
// the static type graph with agents.Resolution as the positive control, so
// a walk that stopped inspecting anything fails here rather than reporting
// the guarantee. What it does not see is what internal/session's walk
// states: unexported fields, and a value behind an interface-typed field.
// machine.Wakes is not a root here on purpose: it carries store.Wake, whose
// store.Optional fields answer database/sql's Valuer with an empty
// interface, which the walk reports as able to carry anything; that is the
// same shape every answer carrying a store record has, machine.Run among
// them, and not a route the worker paths added.
func TestNothingTheWorkerPathsExportReachesAFixerSession(t *testing.T) {
	for _, root := range []reflect.Type{
		reflect.TypeOf(&service.Service{}),
		reflect.TypeOf(&session.Workers{}),
		reflect.TypeOf(session.Membership{}),
		reflect.TypeOf(machine.Appended{}),
		reflect.TypeOf(machine.Acknowledged{}),
		reflect.TypeOf(machine.TaskEventRequest{}),
		reflect.TypeOf(machine.AcknowledgeRequest{}),
	} {
		if found := route.ToFixerSession(root); len(found) != 0 {
			t.Errorf("%s reaches a fixer session:\n%v", root, found)
		}
	}
	if found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{})); len(found) == 0 {
		t.Fatal("the walk reports no route out of agents.Resolution, which has an exported Runner field, so it is not inspecting anything")
	}
}
