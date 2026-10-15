package service_test

import (
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/service"
)

// The supervisor loop's provider seam reaches no fixer session: a caller
// holding a Terminals, or what one answers, can obtain no agents.Runner and
// nothing agents.OpenFixer opens a session from, so P4's split survives the
// seam the loop observes terminals through. The walk is asked of the static
// type graph, with agents.Resolution as the positive control, so a walk that
// stopped inspecting anything fails rather than reporting the guarantee.
// What it does not see is stated on internal/session's own walk: a value
// held behind an interface-typed field, and the unexported fields of an
// implementation, which is the mechanism and not a gap.
func TestNoRouteFromTheTerminalSeamToAFixerSession(t *testing.T) {
	t.Parallel()
	for _, root := range []reflect.Type{
		reflect.TypeOf((*service.Terminals)(nil)).Elem(),
		reflect.TypeOf(service.Options{}.Terminals),
	} {
		if root == nil {
			continue
		}
		if found := route.ToFixerSession(root); len(found) != 0 {
			t.Errorf("%s reaches a fixer session:\n%v", root, found)
		}
	}
	if found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{})); len(found) == 0 {
		t.Fatal("the walk reports no route out of agents.Resolution, which has an exported Runner field, so it is not inspecting anything")
	}
}
