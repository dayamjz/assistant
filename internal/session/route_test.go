package session_test

import (
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/session"
)

// A caller holding the provider, a session handle, or the worker group set
// can reach no fixer session: a terminal session is not a conversation session, and nothing
// here feeds P4's. The walk is asked of the static type graph, with
// agents.Resolution as the positive control, so a walk that stopped
// inspecting anything fails here instead of reporting the guarantee.
//
// Two things the walk does not see are stated rather than implied. It reads
// exported fields and the results of exported methods, so Pipe's unexported
// runner field is outside it, which is the mechanism and not a gap: the
// guarantee is about what a caller in another package can reach. And a value
// held behind an interface-typed field is invisible to it, the blind spot
// route's own documentation pins, so a Provider implementation that exposed
// a Runner through an interface it holds would pass this walk; this package
// has no such field, and that is a rule the writer keeps.
func TestNoRouteFromTheProviderOrTheHandleToAFixerSession(t *testing.T) {
	for _, root := range []reflect.Type{
		reflect.TypeOf((*session.Provider)(nil)).Elem(),
		reflect.TypeOf(&session.Pipe{}),
		reflect.TypeOf(&session.Session{}),
		reflect.TypeOf(session.Session{}),
		reflect.TypeOf(&session.Workers{}),
		reflect.TypeOf(session.Membership{}),
	} {
		if found := route.ToFixerSession(root); len(found) != 0 {
			t.Errorf("%s reaches a fixer session:\n%v", root, found)
		}
	}
	if found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{})); len(found) == 0 {
		t.Fatal("the walk reports no route out of agents.Resolution, which has an exported Runner field, so it is not inspecting anything")
	}
}
