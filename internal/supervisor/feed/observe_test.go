package feed_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// A database the read-only open cannot read is an in-flight reading of
// unknown, carried with store's typed refusal, and the guard blocks on it
// before it reads anything else: with the lock free and no beacon the
// reason is still the unknown reading. The same home with a readable, empty
// database is nothing in flight, so the unknown is the open's and not the
// fleet's.
func TestAnUnreadableDatabaseIsUnknownAndBlocks(t *testing.T) {
	t.Parallel()
	h := createdHome(t)
	fresh := supervisor.FreshnessFor(10 * time.Second)
	obs, err := feed.Observe(t.Context(), h)
	if obs.InFlight != supervisor.InFlightUnknown {
		t.Fatalf("a home with no database read as %v, want unknown", obs.InFlight)
	}
	var unreadable *store.UnreadableError
	if !errors.As(err, &unreadable) {
		t.Fatalf("the error %v does not carry store's typed refusal", err)
	}
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopInFlightUnknown {
		t.Fatalf("the guard answered %s, want in-flight-unknown", got.Reason)
	}
	withDatabase(t, h, nil)
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with an empty database the guard answered %s, want nothing-in-flight", got.Reason)
	}
	// A root that is not there is unknown on every input, and blocks.
	absent, err := home.Open(filepath.Join(t.TempDir(), "nowhere"))
	if err != nil {
		t.Fatal(err)
	}
	if got := feed.Guard(t.Context(), absent, fresh, time.Now()); got.Allowed() {
		t.Fatalf("a home that is not there answered %s, want blocked", got.Reason)
	}
}

// The feeder and the predicates reach no fixer session: a hook or a command
// holding an observation, a verdict, an arm result or the writer cannot get
// to a Runner through them. agents.Resolution is the positive control, on the
// terms internal/agents/route states.
func TestTheFeederAndThePredicatesReachNoFixerSession(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)
	if found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{})); len(found) == 0 {
		t.Fatal("the walk reports no route out of agents.Resolution, so it is inspecting nothing")
	}
	for _, root := range []any{
		supervisor.Observation{}, supervisor.StopVerdict{}, supervisor.Arm{}, supervisor.Beacon{},
		&feed.Writer{},
	} {
		if found := route.ToFixerSession(reflect.TypeOf(root)); len(found) != 0 {
			t.Errorf("%T reaches a fixer session: %v", root, found)
		}
	}
}
