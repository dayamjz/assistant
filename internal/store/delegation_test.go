package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
)

// seedDomainTask is seedTask for a task in a domain.
func seedDomainTask(t *testing.T, s *Store, id, domain string) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/" + id, Domain: domain,
	}, Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// A delegation is written once and anchored: a second delegation of a
// standing domain is refused, an anchor the domain's rows have outrun is a
// *DelegationMovedError, and the end is the row's one revision, refused
// twice and refused with nobody. A name the home refuses and a posture
// outside the set are refused before anything is read. The database holds
// the same rule against a direct rewrite and a delete.
func TestADelegationIsWrittenOnceAndItsEndIsItsOneRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.Delegate(ctx, DelegationRequest{Domain: "Alpha", Delivery: config.DeliveryGated}); !errors.Is(err, home.ErrDomain) {
		t.Fatalf("a domain the home refuses = %v, want ErrDomain", err)
	}
	if _, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: "ungated"}); !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("a posture outside the set = %v, want ErrUnknownDelivery", err)
	}
	first, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil || first.Domain != "alpha" || first.Delivery != config.DeliveryGated || !first.Standing() || first.Anchor != 0 {
		t.Fatalf("Delegate = %+v, %v", first, err)
	}
	if _, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated, Expected: first.ID}); !errors.Is(err, ErrDomainDelegated) {
		t.Fatalf("delegating a standing domain again = %v, want ErrDomainDelegated", err)
	}
	var moved *DelegationMovedError
	if _, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated}); !errors.As(err, &moved) ||
		moved.Expected != 0 || moved.Stands != first.ID || !errors.Is(err, ErrDelegationMoved) {
		t.Fatalf("an outrun anchor = %v, want a *DelegationMovedError naming %d", err, first.ID)
	}
	if _, _, err := s.EndDelegation(ctx, first.ID, DelegationEnd{}, DelegationEndClosure{}); !errors.Is(err, ErrNoDelegationEnd) {
		t.Fatalf("ending with nobody = %v, want ErrNoDelegationEnd", err)
	}
	ended, _, err := s.EndDelegation(ctx, first.ID, EndedByCoordinator(), DelegationEndClosure{})
	if err != nil || ended.Standing() || ended.EndedBy != Known(EndedByCoordinator()) {
		t.Fatalf("EndDelegation = %+v, %v", ended, err)
	}
	if _, _, err := s.EndDelegation(ctx, first.ID, EndedByCoordinator(), DelegationEndClosure{}); !errors.Is(err, ErrDelegationEnded) {
		t.Fatalf("ending twice = %v, want ErrDelegationEnded", err)
	}
	if _, _, err := s.EndDelegation(ctx, 99, EndedByCoordinator(), DelegationEndClosure{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ending an unknown delegation = %v, want ErrNotFound", err)
	}
	latest, found, err := s.Delegation(ctx, "alpha")
	if err != nil || !found || latest.ID != first.ID || latest.Standing() {
		t.Fatalf("Delegation(alpha) = %+v, %v, %v; want the ended row", latest, found, err)
	}
	second, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryLocal, Expected: first.ID})
	if err != nil || second.ID <= first.ID || second.Anchor != first.ID || !second.Standing() {
		t.Fatalf("delegating the ended domain again = %+v, %v", second, err)
	}
	standing, err := s.StandingDelegations(ctx)
	if err != nil || len(standing) != 1 || standing[0].ID != second.ID {
		t.Fatalf("StandingDelegations = %+v, %v; want the second alone", standing, err)
	}
	for _, statement := range []string{
		fmt.Sprintf(`UPDATE delegation SET delivery = 'local' WHERE id = %d`, second.ID),
		fmt.Sprintf(`UPDATE delegation SET ended_at = '2026-01-01T00:00:00Z' WHERE id = %d`, second.ID),
		fmt.Sprintf(`UPDATE delegation SET end_reason = 'coordinator' WHERE id = %d`, first.ID),
		fmt.Sprintf(`DELETE FROM delegation WHERE id = %d`, first.ID),
	} {
		if _, err := s.write.ExecContext(ctx, statement); err == nil {
			t.Errorf("%s was accepted by the database", statement)
		}
	}
}

// A delegation attempt is anchored history on the task attempt's terms:
// the first is begun against zero, a second against the same anchor is
// refused as moved, the session is recorded once, and an ended delegation
// begins no attempt. The database refuses a second session and a delete.
func TestADelegationAttemptIsAnchoredAndRecordsItsSessionOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	d, err := s.Delegate(ctx, DelegationRequest{Domain: "beta", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.BeginDelegationAttempt(ctx, d.ID, 0)
	if err != nil || first.Sequence != 1 || first.SessionRef.IsKnown() {
		t.Fatalf("BeginDelegationAttempt = %+v, %v", first, err)
	}
	var moved *DelegationMovedError
	if _, err := s.BeginDelegationAttempt(ctx, d.ID, 0); !errors.As(err, &moved) || !moved.Attempt || moved.Stands != 1 {
		t.Fatalf("a second attempt against the same anchor = %v, want a *DelegationMovedError on the attempts", err)
	}
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 1, "", SessionHost{}); err == nil {
		t.Fatal("an empty reference was recorded")
	}
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 1, "ref-one", SessionHost{}); err != nil {
		t.Fatalf("SetDelegationAttemptSession: %v", err)
	}
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 1, "ref-two", SessionHost{}); !errors.Is(err, ErrAttemptSessionRecorded) {
		t.Fatalf("a second session = %v, want ErrAttemptSessionRecorded", err)
	}
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 2, "ref-two", SessionHost{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session on no attempt = %v, want ErrNotFound", err)
	}
	second, err := s.BeginDelegationAttempt(ctx, d.ID, 1)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("the second attempt = %+v, %v", second, err)
	}
	attempts, err := s.DelegationAttempts(ctx, d.ID)
	if err != nil || len(attempts) != 2 || attempts[0].SessionRef.Or("") != "ref-one" || attempts[1].SessionRef.IsKnown() {
		t.Fatalf("DelegationAttempts = %+v, %v", attempts, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE delegation_attempt SET session_ref = 'x' WHERE delegation = ? AND sequence = 1`, d.ID); err == nil {
		t.Error("the database rewrote a recorded session")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM delegation_attempt WHERE delegation = ?`, d.ID); err == nil {
		t.Error("the database deleted an attempt")
	}
	if _, _, err := s.EndDelegation(ctx, d.ID, EndedByCoordinator(), DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginDelegationAttempt(ctx, d.ID, 2); !errors.Is(err, ErrDelegationEnded) {
		t.Fatalf("an attempt of an ended delegation = %v, want ErrDelegationEnded", err)
	}
	// A session started for the delegation while it was being ended lands
	// after the end: it is refused in the recording transaction, so the
	// attempt reads as one that never recorded a session. The second
	// attempt recorded none before the end, which is the positive control
	// for the refusal being the end's and not the attempt's.
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 2, "ref-late", SessionHost{}); !errors.Is(err, ErrDelegationEnded) {
		t.Fatalf("a session recorded on an ended delegation = %v, want ErrDelegationEnded", err)
	}
	if attempts, err := s.DelegationAttempts(ctx, d.ID); err != nil || len(attempts) != 2 || attempts[1].SessionRef.IsKnown() {
		t.Fatalf("after the refused record the attempts are %+v (%v), want the second still naming no session", attempts, err)
	}
	if _, err := s.BeginDelegationAttempt(ctx, 99, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an attempt of no delegation = %v, want ErrNotFound", err)
	}
}

// A task's domain is written at intake and never moved: a name the home
// refuses is refused, the row reads it back, a row written with none reads
// empty, and the database refuses every change of the column, from empty
// included, because ownership never transfers (PRD section 11).
func TestATasksDomainIsWrittenAtIntakeAndNeverMoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.CreateTask(ctx, Task{ID: "taskdomainaa", Shape: TaskInvestigation, Project: "one", Mode: "m",
		WorktreePath: "/w", Domain: "No"}, Disposition{}); !errors.Is(err, home.ErrDomain) {
		t.Fatalf("a domain the home refuses = %v, want ErrDomain", err)
	}
	in := seedDomainTask(t, s, "taskdomainab", "alpha")
	out := seedTask(t, s, "taskdomainac")
	if in.Domain != "alpha" || out.Domain != "" {
		t.Fatalf("domains read back %q and %q, want alpha and none", in.Domain, out.Domain)
	}
	for _, statement := range []string{
		`UPDATE task SET domain = 'beta' WHERE id = 'taskdomainab'`,
		`UPDATE task SET domain = NULL WHERE id = 'taskdomainab'`,
		`UPDATE task SET domain = 'alpha' WHERE id = 'taskdomainac'`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err == nil {
			t.Errorf("%s was accepted by the database", statement)
		}
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE task SET domain = 'alpha' WHERE id = 'taskdomainab'`); err != nil {
		t.Errorf("a rewrite that changes nothing was refused: %v", err)
	}
}

// An escalation is written once, about a task that exists, with its text
// redacted; the database refuses an update and a delete.
func TestAnEscalationIsWrittenOnceAndRedacted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedDomainTask(t, s, "taskescalate", "alpha")
	if _, err := s.RecordEscalation(ctx, task.ID, "  "); err == nil {
		t.Fatal("an empty escalation was recorded")
	}
	if _, err := s.RecordEscalation(ctx, "tasknotthere", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an escalation about no task = %v, want ErrNotFound", err)
	}
	e, err := s.RecordEscalation(ctx, task.ID, "promote it; the token is https://user:secret@example.com/x")
	if err != nil || e.TaskID != task.ID || e.Text == "" {
		t.Fatalf("RecordEscalation = %+v, %v", e, err)
	}
	if got, _ := s.TaskEscalations(ctx, task.ID); len(got) != 1 || got[0] != e {
		t.Fatalf("TaskEscalations = %+v, want the one row", got)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE escalation SET text = 'y' WHERE id = ?`, e.ID); err == nil {
		t.Error("the database rewrote an escalation")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM escalation WHERE id = ?`, e.ID); err == nil {
		t.Error("the database deleted an escalation")
	}
}

// P14 at the queue: a wake's recipient is decided by its task's domain and
// its kind, read from the task row and the delegation table at the drain,
// and every queued row is on offer to exactly one of the two coordinators.
// While a delegation stands, a kind the route names actable of a task in
// its domain is offered to the subordinate's drain and not the
// coordinator's, a kind outside it to the coordinator's and not the
// subordinate's, and a task in no domain to the coordinator alone; once the
// delegation has ended the subordinate is offered nothing and the next
// coordinator's drain is offered the domain's rows. The subordinate's
// handout is recorded as its member and reads back as it. OfferedWakes and
// QueuedWakesFor count and list the same partition.
func TestAWakeIsOfferedToExactlyOneCoordinatorByItsTasksDomainAndKind(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	inside := seedDomainTask(t, s, "taskroutedaa", "alpha").ID
	outside := seedTask(t, s, "taskroutedab").ID
	d, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	inc := incarnationOf(t, s)
	queue := func(task, kind string) int64 {
		t.Helper()
		out, err := s.AppendWakeWithMarkers(ctx, inc, WakeRequest{TaskID: task, Kind: kind, Marker: kind + ":" + task}, MarkerAdvance{})
		if err != nil {
			t.Fatalf("AppendWakeWithMarkers(%s, %s): %v", task, kind, err)
		}
		return out.Wake.ID
	}
	actable := queue(inside, "terminal-gone")
	decision := queue(inside, "hold-opened")
	plain := queue(outside, "terminal-gone")
	route := func(domain string) WakeRoute {
		return WakeRoute{Subordinate: domain, Actable: []string{"terminal-gone", "no-change"}}
	}
	ids := func(wakes []Wake) []int64 {
		out := make([]int64, 0, len(wakes))
		for _, w := range wakes {
			out = append(out, w.ID)
		}
		return out
	}
	drain := func(domain string) []Wake {
		t.Helper()
		to := AcknowledgedByCoordinator()
		if domain != "" {
			to = AcknowledgedBySubordinate(domain)
		}
		out, err := s.DrainWakes(ctx, 10, WakeHandout{To: to, Under: "u", Route: route(domain)})
		if err != nil {
			t.Fatalf("DrainWakes(%q): %v", domain, err)
		}
		return out
	}

	sub := drain("alpha")
	if got := ids(sub); !slices.Equal(got, []int64{actable}) {
		t.Fatalf("the subordinate's drain = %v, want only the actable row %d of its domain", got, actable)
	}
	if sub[0].HandedOutTo != Known(AcknowledgedBySubordinate("alpha")) {
		t.Fatalf("the subordinate's handout was recorded to %v", sub[0].HandedOutTo)
	}
	if got := ids(drain("")); !slices.Equal(got, []int64{decision, plain}) {
		t.Fatalf("the coordinator's drain = %v, want the decision row %d and the undelegated row %d", got, decision, plain)
	}
	if got := ids(drain("beta")); len(got) != 0 {
		t.Fatalf("a subordinate of an undelegated domain was offered %v", got)
	}
	if n, err := s.OfferedWakes(ctx, nil, route("alpha")); err != nil || n != 1 {
		t.Fatalf("OfferedWakes(alpha) = %d, %v; want 1", n, err)
	}
	if n, err := s.OfferedWakes(ctx, nil, route("")); err != nil || n != 2 {
		t.Fatalf("OfferedWakes(coordinator) = %d, %v; want 2", n, err)
	}
	if listed, err := s.QueuedWakesFor(ctx, 10, route("alpha")); err != nil || !slices.Equal(ids(listed), []int64{actable}) {
		t.Fatalf("QueuedWakesFor(alpha) = %v, %v; want the actable row", ids(listed), err)
	}
	if listed, err := s.QueuedWakesFor(ctx, 10, route("")); err != nil || !slices.Equal(ids(listed), []int64{decision, plain}) {
		t.Fatalf("QueuedWakesFor(coordinator) = %v, %v", ids(listed), err)
	}
	// WakeRouted is the same partition read for one row, which the push to
	// the coordinator's stream asks before pushing: the subordinate's
	// actable row is routed to its domain and not to the coordinator, the
	// other two the reverse; the zero route, which names no actable kind,
	// routes every row to the coordinator and is what the push would read
	// with the routing forced off.
	for _, c := range []struct {
		id     int64
		domain string
		want   bool
	}{{actable, "alpha", true}, {actable, "", false}, {decision, "alpha", false}, {decision, "", true}, {plain, "alpha", false}, {plain, "", true}} {
		if got, err := s.WakeRouted(ctx, c.id, route(c.domain)); err != nil || got != c.want {
			t.Fatalf("WakeRouted(%d, %q) = %v, %v; want %v", c.id, c.domain, got, err, c.want)
		}
	}
	if got, err := s.WakeRouted(ctx, actable, WakeRoute{}); err != nil || !got {
		t.Fatalf("WakeRouted(%d, the zero route) = %v, %v; want the row carried, since the zero route names nothing actable", actable, got, err)
	}
	if _, err := s.WakeRouted(ctx, 999, route("")); !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("WakeRouted of no wake = %v, want ErrWakeUnknown", err)
	}
	scopes, err := s.WakeScopes(ctx, []int64{actable, plain})
	if err != nil || scopes[actable] != (WakeScope{Domain: "alpha", Kind: "terminal-gone"}) || scopes[plain] != (WakeScope{Kind: "terminal-gone"}) {
		t.Fatalf("WakeScopes = %v, %v", scopes, err)
	}
	if _, err := s.WakeScopes(ctx, []int64{999}); !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("WakeScopes of no wake = %v, want ErrWakeUnknown", err)
	}
	if err := s.AcknowledgeWakes(ctx, []int64{actable}, AcknowledgedBySubordinate("alpha")); err != nil {
		t.Fatalf("the subordinate's acknowledgement: %v", err)
	}
	if wakes, _ := s.TaskWakes(ctx, inside); wakes[0].AcknowledgedBy != Known(AcknowledgedBySubordinate("alpha")) {
		t.Fatalf("the acknowledgement reads back as %v", wakes[0].AcknowledgedBy)
	}
	if err := s.AcknowledgeWakes(ctx, []int64{decision}, AcknowledgedBySubordinate("Bad")); !errors.Is(err, ErrUnknownAcknowledger) {
		t.Fatalf("a subordinate member naming a domain the home refuses = %v, want ErrUnknownAcknowledger", err)
	}

	again := queue(inside, "no-change")
	if _, _, err := s.EndDelegation(ctx, d.ID, EndedByCoordinator(), DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if got := ids(drain("alpha")); len(got) != 0 {
		t.Fatalf("after the end the subordinate was offered %v", got)
	}
	if got := ids(drain("")); !slices.Equal(got, []int64{decision, plain, again}) {
		t.Fatalf("after the end the coordinator's drain = %v, want every queued row, %d among them", got, again)
	}
}

// The subordinate members of the two closed sets carry their domain and
// read back as themselves, and no text outside the set decodes: a column
// holding a subordinate member whose domain the home refuses is refused on
// the read rather than fabricated.
func TestTheSubordinateMembersCarryTheirDomainAndRefuseAnInvalidOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if got := AcknowledgedBySubordinate("alpha"); got.Domain() != "alpha" || got.String() != "subordinate of domain alpha" {
		t.Fatalf("AcknowledgedBySubordinate(alpha) = %q, domain %q", got, got.Domain())
	}
	if got := SentBySubordinate("alpha"); got.Domain() != "alpha" || got.String() != "subordinate of domain alpha" {
		t.Fatalf("SentBySubordinate(alpha) = %q, domain %q", got, got.Domain())
	}
	if AcknowledgedByCoordinator().Domain() != "" || SentByCoordinator().Domain() != "" {
		t.Fatal("the coordinator's members carry a domain")
	}
	task := seedDomainTask(t, s, "tasksenderaa", "alpha")
	if _, err := s.BeginTaskAttempt(ctx, task.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(ctx, task.ID, 1, "hello", SentBySubordinate("No")); !errors.Is(err, ErrUnknownSender) {
		t.Fatalf("a subordinate sender naming a domain the home refuses = %v, want ErrUnknownSender", err)
	}
	m, err := s.AppendMessage(ctx, task.ID, 1, "hello", SentBySubordinate("alpha"))
	if err != nil || m.SentBy != SentBySubordinate("alpha") {
		t.Fatalf("AppendMessage = %+v, %v", m, err)
	}
	// The message row is sealed, so the column read is asked of the decoder
	// directly: the one conversion from text refuses a subordinate member
	// naming a domain the home refuses, as it refuses any text outside the
	// set.
	if _, err := senderValue("subordinate:Bad", "sent_by"); !errors.Is(err, ErrUnknownSender) {
		t.Fatalf("a column outside the set decoded as %v, want ErrUnknownSender", err)
	}
	if got, err := senderValue("subordinate:alpha", "sent_by"); err != nil || got != SentBySubordinate("alpha") {
		t.Fatalf("the subordinate's column decoded as %v, %v", got, err)
	}
	inc := incarnationOf(t, s)
	out, err := s.AppendWakeWithMarkers(ctx, inc, WakeRequest{TaskID: task.ID, Kind: "k", Marker: "m"}, MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE wake SET handled_at = '2026-01-01T00:00:00.000000000Z', acknowledged_by = 'subordinate:Bad' WHERE id = ?`, out.Wake.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskWakes(ctx, task.ID); !errors.Is(err, ErrUnknownAcknowledger) {
		t.Fatalf("a wake column outside the set read back as %v, want ErrUnknownAcknowledger", err)
	}
}

// An attempt records the identity of the process hosting its terminal with
// its session, once, in the same write: it reads back as recorded, a zero
// host reads back zero, and nothing records a host after the session.
func TestAnAttemptRecordsItsSessionsHostWithItsReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	d, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	for seq := int64(1); seq <= 2; seq++ {
		if _, err := s.BeginDelegationAttempt(ctx, d.ID, seq-1); err != nil {
			t.Fatal(err)
		}
	}
	host := SessionHost{PID: 4242, Start: 1792483494420481}
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 1, "ref-one", host); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDelegationAttemptSession(ctx, d.ID, 2, "ref-two", SessionHost{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.DelegationAttempts(ctx, d.ID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("DelegationAttempts = %+v, %v", attempts, err)
	}
	if attempts[0].Host != host || attempts[1].Host != (SessionHost{}) {
		t.Fatalf("the attempts record hosts %+v and %+v, want %+v and none", attempts[0].Host, attempts[1].Host, host)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE delegation_attempt SET host_pid = 1 WHERE delegation = ? AND sequence = 2`, d.ID); err == nil {
		t.Fatal("a host was recorded after the session")
	}
}

// A delegation's end answers the holds its prefix selects, read in the
// same transaction: an open one is resolved under the resolver given and
// reported, one already resolved is left as it stood and not reported, a
// key outside the prefix ("d/10/" beside "d/1/" included) is left open, and
// a closure with no resolution or nobody is refused with nothing written,
// the delegation still standing.
func TestADelegationsEndAnswersTheHoldsItNamesInItsTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	d, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"d/1/open-one", "d/1/answered-one", "d/10/other", "e/1/open"} {
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "whether"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ResolveHold(ctx, "d/1/answered-one", "retry", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	const prefix = "d/1/"
	if _, _, err := s.EndDelegation(ctx, d.ID, EndedByCoordinator(), DelegationEndClosure{KeyPrefix: prefix, By: ResolvedByMachineInterface()}); !errors.Is(err, ErrNoResolution) {
		t.Fatalf("a closure with no resolution = %v, want ErrNoResolution", err)
	}
	if _, _, err := s.EndDelegation(ctx, d.ID, EndedByCoordinator(), DelegationEndClosure{KeyPrefix: prefix, Resolution: "stop"}); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("a closure naming nobody = %v, want ErrNoResolver", err)
	}
	if latest, _, err := s.Delegation(ctx, "alpha"); err != nil || !latest.Standing() {
		t.Fatalf("after the refusals the delegation reads %+v (%v), want it standing", latest, err)
	}
	ended, closed, err := s.EndDelegation(ctx, d.ID, EndedByCoordinator(),
		DelegationEndClosure{KeyPrefix: prefix, Resolution: "stop", Note: "ended", By: ResolvedByMachineInterface()})
	if err != nil || ended.Standing() || !slices.Equal(closed, []string{"d/1/open-one"}) {
		t.Fatalf("EndDelegation = %+v, %v, %v; want it ended answering d/1/open-one alone", ended, closed, err)
	}
	for _, key := range []string{"d/10/other", "e/1/open"} {
		if h, err := s.Hold(ctx, key); err != nil || !h.Open() {
			t.Fatalf("%s outside the prefix reads %+v (%v), want it still open", key, h, err)
		}
	}

	// The prefix is compared in bytes, not characters: a key with a
	// multi-byte character in it is selected by its own byte prefix, and
	// one whose bytes only share the prefix's characters' count is not.
	other, err := s.Delegate(ctx, DelegationRequest{Domain: "beta", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"é/1/0", "é/10/0", "éa/1/0"} {
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "whether"}); err != nil {
			t.Fatal(err)
		}
	}
	_, closed, err = s.EndDelegation(ctx, other.ID, EndedByCoordinator(),
		DelegationEndClosure{KeyPrefix: "é/1/", Resolution: "stop", By: ResolvedByMachineInterface()})
	if err != nil || !slices.Equal(closed, []string{"é/1/0"}) {
		t.Fatalf("ending with a non-ASCII prefix closed %v (%v), want é/1/0 alone", closed, err)
	}
	for _, key := range []string{"é/10/0", "éa/1/0"} {
		if h, err := s.Hold(ctx, key); err != nil || !h.Open() {
			t.Fatalf("%s outside the non-ASCII prefix reads %+v (%v), want it still open", key, h, err)
		}
	}
	if h, err := s.Hold(ctx, "d/1/open-one"); err != nil || h.Resolution.Or("") != "stop" || h.ResolvedBy.Or(Resolver{}) != ResolvedByMachineInterface() || h.ResolutionNote.Or("") != "ended" {
		t.Fatalf("open-one reads %+v (%v), want it answered stop by the machine interface", h, err)
	}
	if h, err := s.Hold(ctx, "d/1/answered-one"); err != nil || h.Resolution.Or("") != "retry" {
		t.Fatalf("answered-one reads %+v (%v), want its own answer kept", h, err)
	}
}

// AnswerDelegationHold's answer and end are one transaction: with the
// delegation's end made to fail inside the transaction, by a trigger this
// test installs on the table, the answer is rolled back with it and the
// hold reads open, so no failure between the two leaves a hold answered
// stop beside a delegation still standing. With the trigger dropped, the
// same call records both, the positive control. Two transactions in
// sequence would leave the hold answered here.
func TestAnAnswerThatEndsADelegationIsWrittenWithTheEndOrNotAtAll(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	d, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterDelegationHold(ctx, d.ID, Hold{Key: "bound", Subject: "whether"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `CREATE TEMP TRIGGER fail_the_end BEFORE UPDATE OF ended_at ON delegation
		BEGIN SELECT RAISE(ABORT, 'the end fails'); END`); err != nil {
		t.Fatal(err)
	}
	answer := DelegationHoldAnswer{Key: "bound", Resolution: "stop", By: ResolvedByMachineInterface(), End: EndedByAnswer()}
	if _, _, err := s.AnswerDelegationHold(ctx, d.ID, answer); err == nil {
		t.Fatal("the answer succeeded with its end failing")
	}
	if h, err := s.Hold(ctx, "bound"); err != nil || !h.Open() {
		t.Fatalf("after the failed end the hold reads %+v (%v), want it still open", h, err)
	}
	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER fail_the_end`); err != nil {
		t.Fatal(err)
	}
	h, ended, err := s.AnswerDelegationHold(ctx, d.ID, answer)
	if err != nil || h.Open() || ended.Standing() || ended.EndedBy.Or(DelegationEnd{}) != EndedByAnswer() {
		t.Fatalf("AnswerDelegationHold = %+v, %+v, %v; want the hold answered and the delegation ended by it", h, ended, err)
	}
	if _, err := s.RegisterDelegationHold(ctx, d.ID, Hold{Key: "late", Subject: "whether"}); !errors.Is(err, ErrDelegationEnded) {
		t.Fatalf("registering on the ended delegation = %v, want ErrDelegationEnded", err)
	}
	if _, err := s.Hold(ctx, "late"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused registration wrote a hold: %v", err)
	}
}
