package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A promoted task is recorded under its project's posture, read through the
// same posture reader intake reads it through, once per promotion, and
// written in the flip: the record the promotion returns and the record the
// store reads back carry it, and the promoted entry names it. Each defined
// posture is promoted, so the gated case is the control that the local one
// is the project's answer and not a default.
func TestPromoteRecordsTheProjectsPosture(t *testing.T) {
	principles.Cite(t, principles.P7)
	principles.Cite(t, principles.P14)
	for _, project := range config.Deliveries() {
		s := newPromotionSubject(t)
		s.posture = project
		tk := s.investigation(t)
		got := s.promote(t, request(tk.id))
		if got.Task.Delivery != store.Known(project) {
			t.Fatalf("a task promoted in a %s project was recorded under %v", project, got.Task.Delivery)
		}
		if stored := s.record(t, tk.id); stored.Delivery != store.Known(project) || stored.Shape != store.TaskDelivery {
			t.Fatalf("the stored task is %+v, want a delivery task under %s", stored, project)
		}
		if !strings.Contains(got.Event.Detail, "under "+string(project)) {
			t.Fatalf("the promoted entry %q does not name the posture %s", got.Event.Detail, project)
		}
		if n := s.postureAsked.Load(); n != 1 {
			t.Fatalf("the posture was read %d times for one promotion", n)
		}
	}
}

// An unreadable trusted copy refuses the promotion with ErrPostureUnreadable
// and records nothing: the task is still the investigation it was, with no
// entry past the ones it had and nothing written into its copy, and no
// posture is assumed in the copy's place, not even gated and not the
// coordinator's own request. The control is the same promotion once the
// copy reads, which is recorded.
func TestAnUnreadablePostureRefusesThePromotion(t *testing.T) {
	principles.Cite(t, principles.P7)
	ctx := context.Background()
	s := newPromotionSubject(t)
	tk := s.investigation(t)
	n := len(s.history(t, tk.id))
	s.postureErr = errors.New("the trusted copy could not be fetched")
	for _, asked := range []string{"", "gated"} {
		req := request(tk.id)
		req.Delivery = asked
		if _, err := s.queue.Promote(ctx, req); !errors.Is(err, ErrPostureUnreadable) {
			t.Fatalf("a promotion asking %q over an unreadable copy = %v, want ErrPostureUnreadable", asked, err)
		}
		s.untouched(t, tk, n)
		if s.record(t, tk.id).Delivery.IsKnown() {
			t.Fatalf("a refused promotion recorded a posture")
		}
	}
	s.postureErr = nil
	s.posture = config.Delivery("direct")
	if _, err := s.queue.Promote(ctx, request(tk.id)); !errors.Is(err, ErrPostureUnreadable) {
		t.Fatalf("a promotion over a posture this build does not define = %v, want ErrPostureUnreadable", err)
	}
	s.untouched(t, tk, n)

	s.posture = config.DeliveryLocal
	if got := s.promote(t, request(tk.id)); got.Task.Delivery != store.Known(config.DeliveryLocal) {
		t.Fatalf("the control was recorded under %v", got.Task.Delivery)
	}
}

// Tighten, never loosen, at promotion on intake's terms: local asked of a
// gated project is a *PostureError naming both, a refusal IsRefusal reports,
// and nothing is recorded. gated asked of a local project is recorded as
// gated, and local asked of a local project is no change and is recorded.
// direct and an unknown word are malformed requests, refused before the
// posture is read.
func TestPromoteTightensAPostureAndNeverLoosensIt(t *testing.T) {
	principles.Cite(t, principles.P7)
	ctx := context.Background()
	s := newPromotionSubject(t)
	s.posture = config.DeliveryGated
	tk := s.investigation(t)
	n := len(s.history(t, tk.id))
	req := request(tk.id)
	req.Delivery = "local"
	_, err := s.queue.Promote(ctx, req)
	var lighter *PostureError
	if !errors.As(err, &lighter) || !errors.Is(err, ErrLighterPosture) || !IsRefusal(err) ||
		lighter.Requested != config.DeliveryLocal || lighter.Project != config.DeliveryGated {
		t.Fatalf("local asked of a gated project = %v, want a *PostureError naming both", err)
	}
	s.untouched(t, tk, n)

	asked := s.postureAsked.Load()
	for _, word := range []string{"direct", "fast"} {
		req := request(tk.id)
		req.Delivery = word
		if _, err := s.queue.Promote(ctx, req); !errors.Is(err, ErrRequest) {
			t.Fatalf("posture %q = %v, want ErrRequest", word, err)
		}
	}
	if got := s.postureAsked.Load(); got != asked {
		t.Fatalf("a malformed request read the posture %d times", got-asked)
	}
	s.untouched(t, tk, n)

	for _, want := range []config.Delivery{config.DeliveryGated, config.DeliveryLocal} {
		s := newPromotionSubject(t)
		s.posture = config.DeliveryLocal
		tk := s.investigation(t)
		req := request(tk.id)
		req.Delivery = string(want)
		if got := s.promote(t, req); got.Task.Delivery != store.Known(want) {
			t.Fatalf("%s asked of a local project was recorded as %v", want, got.Task.Delivery)
		}
	}
}

// A housekeeping pass is delivered through the gate whatever posture its
// project declares, PRD section 6's housekeeping ("delivery through the
// gate, pull request"; "reaches a shared branch only through the gate"):
// its scope is held at the review of the run its push starts, and a task
// under the local posture starts no run, so a pass promoted under it would
// change files outside housekeeping.paths with no ask. A pass in a local
// project is promoted gated; a request for the local posture on a pass is
// refused as a malformed request, recording nothing; a request for gated
// is what it already is. The control is an ordinary investigation in the
// same project, promoted under the project's local posture.
func TestAPassIsPromotedThroughTheGateWhateverThePostureIs(t *testing.T) {
	principles.Cite(t, principles.P7)
	ctx := context.Background()
	s := newPromotionSubject(t)
	s.posture = config.DeliveryLocal
	tk := s.pass(t)
	n := len(s.history(t, tk.id))
	req := request(tk.id)
	req.Delivery = "local"
	if _, err := s.queue.Promote(ctx, req); !errors.Is(err, ErrRequest) || !IsRefusal(err) {
		t.Fatalf("local asked of a pass = %v, want ErrRequest", err)
	}
	s.untouched(t, tk, n)
	if got := s.promote(t, request(tk.id)); got.Task.Delivery != store.Known(config.DeliveryGated) {
		t.Fatalf("a pass promoted in a local project was recorded under %v, want gated", got.Task.Delivery)
	}
	if stored := s.record(t, tk.id); stored.Delivery != store.Known(config.DeliveryGated) || !stored.Housekeeping {
		t.Fatalf("the stored pass is %+v, want a gated delivery still marked", stored)
	}
	control := s.investigation(t)
	req = request(control.id)
	req.Branch = "topic-control" // the promoted pass holds topic
	if got := s.promote(t, req); got.Task.Delivery != store.Known(config.DeliveryLocal) {
		t.Fatalf("the control, an investigation in the local project, was recorded under %v", got.Task.Delivery)
	}

	// A pass asked gated, in another subject since a repository has one
	// open pass, is what it already is.
	s = newPromotionSubject(t)
	s.posture = config.DeliveryLocal
	tk = s.pass(t)
	req = request(tk.id)
	req.Delivery = "gated"
	if got := s.promote(t, req); got.Task.Delivery != store.Known(config.DeliveryGated) {
		t.Fatalf("a pass asked gated was recorded under %v", got.Task.Delivery)
	}
}

// A housekeeping pass is held to the same read as any promotion (P7): it is
// recorded gated whatever the project declares, but the project's posture
// is still read from its trusted copy, and a copy that cannot be read, or
// that declares a posture this build does not define, refuses the promotion
// with ErrPostureUnreadable and records nothing, with no posture assumed in
// its place. The control is an ordinary investigation over the same copy,
// refused the same way, which is what says the refusal is the reader's.
// Once the copy reads, the same pass is promoted gated, with the posture
// read once.
func TestAnUnreadablePostureRefusesThePromotionOfAPass(t *testing.T) {
	principles.Cite(t, principles.P7)
	ctx := context.Background()
	s := newPromotionSubject(t)
	s.posture = config.DeliveryLocal
	tk := s.pass(t)
	n := len(s.history(t, tk.id))
	s.postureErr = errors.New("the trusted copy could not be fetched")
	asked := s.postureAsked.Load()
	if _, err := s.queue.Promote(ctx, request(tk.id)); !errors.Is(err, ErrPostureUnreadable) {
		t.Fatalf("promoting a pass over an unreadable copy = %v, want ErrPostureUnreadable", err)
	}
	s.untouched(t, tk, n)
	if got := s.postureAsked.Load(); got != asked+1 {
		t.Fatalf("the pass's promotion read the posture %d times, want once", got-asked)
	}
	control := s.investigation(t)
	m := len(s.history(t, control.id))
	if _, err := s.queue.Promote(ctx, request(control.id)); !errors.Is(err, ErrPostureUnreadable) {
		t.Fatalf("the control, an investigation over the same copy = %v, want ErrPostureUnreadable", err)
	}
	s.untouched(t, control, m)

	s.postureErr = nil
	s.posture = config.Delivery("direct")
	if _, err := s.queue.Promote(ctx, request(tk.id)); !errors.Is(err, ErrPostureUnreadable) {
		t.Fatalf("promoting a pass over a posture this build does not define = %v, want ErrPostureUnreadable", err)
	}
	s.untouched(t, tk, n)

	s.posture = config.DeliveryLocal
	asked = s.postureAsked.Load()
	if got := s.promote(t, request(tk.id)); got.Task.Delivery != store.Known(config.DeliveryGated) {
		t.Fatalf("the pass promoted once the copy reads was recorded under %v, want gated", got.Task.Delivery)
	}
	if got := s.postureAsked.Load(); got != asked+1 {
		t.Fatalf("the pass's promotion read the posture %d times, want once", got-asked)
	}
}
