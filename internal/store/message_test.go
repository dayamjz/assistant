package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// seedAttempt records a task and its first attempt.
func seedAttempt(t *testing.T, s *Store, id string) TaskAttempt {
	t.Helper()
	task := seedTask(t, s, id)
	a, err := s.BeginFirstTaskAttempt(context.Background(), task.ID, OnThisMachine())
	if err != nil {
		t.Fatalf("beginning the first attempt: %v", err)
	}
	return a
}

// A message is appended before its send, with credentials removed, and its
// delivery is recorded once: written or refused, never both and never
// revised. An attempt the task does not have, the zero sender and empty text
// are refused before anything is written.
func TestAMessagesDeliveryIsRecordedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	a := seedAttempt(t, s, "taskmsgaaaaa")

	m, err := s.AppendMessage(ctx, a.TaskID, a.Sequence, "push to https://user:secret@example.com/x.git", SentByCoordinator())
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if strings.Contains(m.Text, "secret") || m.Delivery() != DeliveryUnknown || m.Acknowledged() || m.SentBy != SentByCoordinator() {
		t.Fatalf("the appended message is %+v, want it redacted, undelivered, unacknowledged and the coordinator's", m)
	}
	if _, err := s.AppendMessage(ctx, a.TaskID, 9, "x", SentByCoordinator()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a message to an attempt the task does not have = %v, want ErrNotFound", err)
	}
	if _, err := s.AppendMessage(ctx, a.TaskID, a.Sequence, "x", Sender{}); !errors.Is(err, ErrNoSender) {
		t.Fatalf("a message from nobody = %v, want ErrNoSender", err)
	}
	if _, err := s.AppendMessage(ctx, a.TaskID, a.Sequence, "  ", SentByCoordinator()); err == nil {
		t.Fatal("a message with no text was recorded")
	}

	written, err := s.MarkMessageWritten(ctx, m.ID)
	if err != nil || written.Delivery() != DeliveryWritten {
		t.Fatalf("MarkMessageWritten = %+v, %v", written, err)
	}
	if _, err := s.MarkMessageWritten(ctx, m.ID); !errors.Is(err, ErrMessageDelivery) {
		t.Fatalf("writing a written message again = %v, want ErrMessageDelivery", err)
	}
	if _, err := s.MarkMessageRefused(ctx, m.ID, "too late"); !errors.Is(err, ErrMessageDelivery) {
		t.Fatalf("refusing a written message = %v, want ErrMessageDelivery", err)
	}

	other, err := s.AppendMessage(ctx, a.TaskID, a.Sequence, "second", SentByCoordinator())
	if err != nil {
		t.Fatal(err)
	}
	refused, err := s.MarkMessageRefused(ctx, other.ID, "session: the provider does not hold this session")
	if err != nil || refused.Delivery() != DeliveryRefused || refused.Refusal.Or("") == "" {
		t.Fatalf("MarkMessageRefused = %+v, %v", refused, err)
	}
	if _, err := s.MarkMessageWritten(ctx, other.ID); !errors.Is(err, ErrMessageDelivery) {
		t.Fatalf("writing a refused message = %v, want ErrMessageDelivery", err)
	}

	// The seal holds at the database, whatever an accessor would do.
	if _, err := s.write.ExecContext(ctx, `UPDATE message SET text = 'rewritten' WHERE id = ?`, m.ID); err == nil {
		t.Fatal("the database let a message's text be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE message SET written_at = NULL WHERE id = ?`, m.ID); err == nil {
		t.Fatal("the database let a written message be unwritten")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM message WHERE id = ?`, m.ID); err == nil {
		t.Fatal("the database let a message be deleted")
	}
}

// PRD principle P8 at the message record: the acknowledgement is the event
// the worker appended and the fact on the record, written in one
// transaction, so reading either finds the other. The event's detail names
// the message, and the record names the event's sequence. A message of
// another task, one already acknowledged and one that was refused are each
// refused with nothing appended, which the history's length shows. The
// written-unacknowledged read is the loop's, and it holds exactly the
// written message nobody acknowledged.
func TestAnAcknowledgementIsTheEventAndTheRecordInOneTransaction(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	a := seedAttempt(t, s, "taskackaaaaa")
	other := seedAttempt(t, s, "taskackbbbbb")

	m, _ := s.AppendMessage(ctx, a.TaskID, a.Sequence, "stay on scope", SentByCoordinator())
	if _, err := s.MarkMessageWritten(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	waiting, _ := s.AppendMessage(ctx, a.TaskID, a.Sequence, "and the tests", SentByCoordinator())
	if _, err := s.MarkMessageWritten(ctx, waiting.ID); err != nil {
		t.Fatal(err)
	}
	refused, _ := s.AppendMessage(ctx, a.TaskID, a.Sequence, "never arrived", SentByCoordinator())
	if _, err := s.MarkMessageRefused(ctx, refused.ID, "ended"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.TaskEvents(ctx, a.TaskID, 0, 0)
	otherBefore, _ := s.TaskEvents(ctx, other.TaskID, 0, 0)

	event, acked, err := s.AcknowledgeMessage(ctx, a.TaskID, m.ID)
	if err != nil {
		t.Fatalf("AcknowledgeMessage: %v", err)
	}
	if event.Kind != MessageAcknowledgedEventKind || event.Detail != strconv.FormatInt(m.ID, 10) {
		t.Fatalf("the acknowledgement appended %+v, want the kind naming message %d", event, m.ID)
	}
	if !acked.Acknowledged() || acked.AcknowledgedEvent.Or(0) != event.Sequence {
		t.Fatalf("the record reads %+v, want it acknowledged by event %d", acked, event.Sequence)
	}
	history, _ := s.TaskEvents(ctx, a.TaskID, 0, 0)
	if len(history) != len(before)+1 || history[len(history)-1] != event {
		t.Fatalf("the history is %+v, want one event more, the acknowledgement", history)
	}
	if id, err := ParseMessageID(history[len(history)-1].Detail); err != nil || id != m.ID {
		t.Fatalf("the event's detail reads as message %d (%v), want %d", id, err, m.ID)
	}

	for _, refusal := range []struct {
		name string
		task string
		id   int64
		want error
	}{
		{"another task's message", other.TaskID, m.ID, ErrNotFound},
		{"a message acknowledged already", a.TaskID, m.ID, ErrMessageAcknowledged},
		{"a refused message", a.TaskID, refused.ID, ErrMessageRefused},
		{"no such message", a.TaskID, 999, ErrNotFound},
	} {
		if _, _, err := s.AcknowledgeMessage(ctx, refusal.task, refusal.id); !errors.Is(err, refusal.want) {
			t.Errorf("acknowledging %s = %v, want %v", refusal.name, err, refusal.want)
		}
	}
	if after, _ := s.TaskEvents(ctx, a.TaskID, 0, 0); len(after) != len(before)+1 {
		t.Errorf("a refused acknowledgement appended to the task: %d events, want %d", len(after), len(before)+1)
	}
	if after, _ := s.TaskEvents(ctx, other.TaskID, 0, 0); len(after) != len(otherBefore) {
		t.Errorf("a refused acknowledgement appended to the other task: %d events, want %d", len(after), len(otherBefore))
	}

	pending, err := s.WrittenUnacknowledgedMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := pending[a.TaskID]; len(got) != 1 || got[0].ID != waiting.ID {
		t.Fatalf("the written unacknowledged messages are %+v, want message %d alone", pending, waiting.ID)
	}
	if _, ok := pending[other.TaskID]; ok {
		t.Fatalf("a task with no message has unacknowledged messages: %+v", pending)
	}
}

// The detail of an acknowledgement is a message identifier exactly.
func TestParseMessageIDReadsOnlyAnIdentifier(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "0", "-1", "01", "1 ", "one", "1.0"} {
		if _, err := ParseMessageID(bad); err == nil {
			t.Errorf("ParseMessageID(%q) was accepted", bad)
		}
	}
	if id, err := ParseMessageID("42"); err != nil || id != 42 {
		t.Fatalf("ParseMessageID(\"42\") = %d, %v", id, err)
	}
}
