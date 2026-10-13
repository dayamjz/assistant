package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
)

// waitUnplaced polls the set until a group is no longer a member, and
// fails naming the membership it still reports.
func waitUnplaced(t *testing.T, w *session.Workers, group int) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		m, ok := w.Member(group)
		if !ok {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("group %d is still a member, as %+v", group, m)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Creating a session joins its worker's group to the provider's set, naming
// the task, for exactly the process's lifetime: a worker that ends on its
// own leaves it, and Close leaves it too. The group is read off the
// session's reference, which is the identifier the real adapter reported
// for the process it started, so a membership stated by hand reaches
// nothing here. The set is the Pipe's own and empty before the first
// Create, which is what lets a caller holding it answer "nobody" without
// asking the operating system about anything.
func TestASessionsGroupIsAMemberForTheProcessLifetime(t *testing.T) {
	principles.Cite(t, principles.P14)
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	set := p.Workers()
	if !set.Empty() {
		t.Fatal("a provider that created nothing holds a member")
	}

	ended := create(t, p, sub.spec("implement the thing"))
	group := ended.Reference().PID
	if set.Empty() {
		t.Fatal("Create joined nothing")
	}
	m, ok := set.Member(group)
	if !ok || m != (session.Membership{Task: sub.id, Group: group}) {
		t.Fatalf("Member(%d) = %+v, %v; want the session's task", group, m, ok)
	}
	if _, ok := set.Member(group + 1); ok {
		t.Fatalf("a group nobody leads is a member")
	}
	if err := p.Send(context.Background(), ended, "finish"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitProbe(t, p, ended.Reference().String(), session.ReasonExited)
	waitUnplaced(t, set, group)

	closed := create(t, p, sub.spec("implement the other thing"))
	if _, ok := set.Member(closed.Reference().PID); !ok {
		t.Fatal("the second session's group is not a member")
	}
	if err := p.Close(closed); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := set.Member(closed.Reference().PID); ok {
		t.Fatal("the group of a session Close ended is still a member")
	}
}
