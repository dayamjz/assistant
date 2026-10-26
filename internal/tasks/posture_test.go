package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A delivery task is recorded under its project's posture, read through the
// posture reader at intake, in the field of its own and beside the launch
// notation it does not replace. An investigation task reads no posture and
// records none.
func TestIntakeRecordsTheProjectsPostureOnADeliveryTask(t *testing.T) {
	principles.Cite(t, principles.P14)
	for _, project := range config.Deliveries() {
		f := newFixture(t)
		f.posture = project
		got := f.create(t, delivery(""))
		if posture, ok := got.Task.Delivery.Get(); !ok || posture != project || got.Task.Mode != "interactive" {
			t.Fatalf("a task of a %s project was recorded under %v with launch notation %q", project, got.Task.Delivery, got.Task.Mode)
		}
		stored, err := f.store.Task(context.Background(), got.Task.ID)
		if err != nil || stored.Delivery != got.Task.Delivery {
			t.Fatalf("the stored task carries %v, %v", stored.Delivery, err)
		}
		if f.postureAsked.Load() != 1 {
			t.Fatalf("the posture was read %d times for one intake", f.postureAsked.Load())
		}
	}
	f := newFixture(t)
	investigation := f.create(t, Request{Shape: "investigation", Project: "one", Instructions: "look", Repository: repo})
	if investigation.Task.Delivery.IsKnown() || f.postureAsked.Load() != 0 {
		t.Fatalf("an investigation was recorded under %v after %d posture reads", investigation.Task.Delivery, f.postureAsked.Load())
	}
}

// Tighten, never loosen: the coordinator may record a stricter posture than
// the project's and never a lighter one, which is a typed refusal that
// records nothing. The positive control is gated asked of a local project,
// which is recorded; local asked of a local project is no change and is
// recorded too. direct, an unknown word, and a posture on an investigation
// task are malformed requests.
func TestIntakeTightensAPostureAndNeverLoosensIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.posture = config.DeliveryGated
	req := delivery("")
	req.Delivery = "local"
	_, err := f.queue.Create(ctx, req)
	var lighter *PostureError
	if !errors.As(err, &lighter) || !errors.Is(err, ErrLighterPosture) || !IsRefusal(err) ||
		lighter.Requested != config.DeliveryLocal || lighter.Project != config.DeliveryGated {
		t.Fatalf("local asked of a gated project = %v, want a *PostureError naming both", err)
	}
	if tasks, _ := f.store.Tasks(ctx); len(tasks) != 0 {
		t.Fatalf("a refused intake recorded %d tasks", len(tasks))
	}

	f.posture = config.DeliveryLocal
	for _, asked := range []config.Delivery{config.DeliveryGated, config.DeliveryLocal} {
		f := newFixture(t)
		f.posture = config.DeliveryLocal
		req := delivery("")
		req.Delivery = string(asked)
		got := f.create(t, req)
		if posture, _ := got.Task.Delivery.Get(); posture != asked {
			t.Fatalf("%s asked of a local project was recorded as %v", asked, got.Task.Delivery)
		}
	}

	for _, word := range []string{"direct", "fast"} {
		req := delivery("")
		req.Delivery = word
		if _, err := f.queue.Create(ctx, req); !errors.Is(err, ErrRequest) {
			t.Fatalf("posture %q = %v, want ErrRequest", word, err)
		}
	}
	if _, err := f.queue.Create(ctx, Request{Shape: "investigation", Project: "one", Instructions: "look",
		Delivery: "gated"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("a posture on an investigation = %v, want ErrRequest", err)
	}
}

// An unreadable trusted copy delivers nothing: the intake is refused with
// ErrPostureUnreadable and records nothing, and no posture is assumed in the
// copy's place, not even gated, and not the coordinator's own request
// either, because "never lighter" cannot be established against a posture
// nobody read.
func TestAnUnreadablePostureRefusesTheIntake(t *testing.T) {
	principles.Cite(t, principles.P7)
	ctx := context.Background()
	f := newFixture(t)
	f.postureErr = errors.New("the trusted copy could not be fetched")
	for _, asked := range []string{"", "gated"} {
		req := delivery("")
		req.Delivery = asked
		if _, err := f.queue.Create(ctx, req); !errors.Is(err, ErrPostureUnreadable) {
			t.Fatalf("an intake asking %q over an unreadable copy = %v, want ErrPostureUnreadable", asked, err)
		}
	}
	if tasks, _ := f.store.Tasks(ctx); len(tasks) != 0 {
		t.Fatalf("a refused intake recorded %d tasks", len(tasks))
	}
	f.postureErr = nil
	if got := f.create(t, delivery("")); got.Task.Delivery != store.Known(config.DeliveryGated) {
		t.Fatalf("the control was recorded under %v", got.Task.Delivery)
	}
}

// A floor records the stricter of itself and the project's posture, in the
// one read that records it, and never refuses (the delta review of
// 9ef7722, which found a carry on reading the project's posture once in
// the service and once in intake, so a project loosened between the two
// put the successor below the outlived task's): gated asked as the floor
// of a local project records gated, local as the floor of a gated project
// records gated, the project's, with no *PostureError, and local of a
// local project records local, each after one read of the project's
// posture. A floor beside a posture, a floor on an investigation task and
// a floor that is no posture are malformed requests, and a floor over an
// unreadable trusted copy is refused as any intake is, since the stricter
// of two cannot be decided against a posture nobody read.
func TestIntakeRecordsTheStricterOfAFloorAndTheProjectsPosture(t *testing.T) {
	principles.Cite(t, principles.P7)
	ctx := context.Background()
	for _, c := range []struct {
		project, floor, want config.Delivery
	}{
		{config.DeliveryLocal, config.DeliveryGated, config.DeliveryGated},
		{config.DeliveryGated, config.DeliveryLocal, config.DeliveryGated},
		{config.DeliveryLocal, config.DeliveryLocal, config.DeliveryLocal},
	} {
		f := newFixture(t)
		f.posture = c.project
		req := delivery("")
		req.DeliveryFloor = string(c.floor)
		got := f.create(t, req)
		if posture, _ := got.Task.Delivery.Get(); posture != c.want {
			t.Fatalf("a floor of %s in a %s project was recorded as %v, want %s", c.floor, c.project, got.Task.Delivery, c.want)
		}
		if f.postureAsked.Load() != 1 {
			t.Fatalf("the posture was read %d times for one intake with a floor", f.postureAsked.Load())
		}
	}

	f := newFixture(t)
	f.posture = config.DeliveryLocal
	both := delivery("")
	both.Delivery, both.DeliveryFloor = "gated", "gated"
	if _, err := f.queue.Create(ctx, both); !errors.Is(err, ErrRequest) {
		t.Fatalf("a floor beside a posture = %v, want ErrRequest", err)
	}
	if _, err := f.queue.Create(ctx, Request{Shape: "investigation", Project: "one", Instructions: "look",
		DeliveryFloor: "gated"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("a floor on an investigation = %v, want ErrRequest", err)
	}
	word := delivery("")
	word.DeliveryFloor = "direct"
	if _, err := f.queue.Create(ctx, word); !errors.Is(err, ErrRequest) {
		t.Fatalf("a floor of %q = %v, want ErrRequest", word.DeliveryFloor, err)
	}
	f.postureErr = errors.New("the trusted copy could not be fetched")
	floored := delivery("")
	floored.DeliveryFloor = "gated"
	if _, err := f.queue.Create(ctx, floored); !errors.Is(err, ErrPostureUnreadable) {
		t.Fatalf("a floor over an unreadable copy = %v, want ErrPostureUnreadable", err)
	}
	if tasks, _ := f.store.Tasks(ctx); len(tasks) != 0 {
		t.Fatalf("refused intakes recorded %d tasks", len(tasks))
	}
}
