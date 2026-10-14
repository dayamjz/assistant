package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MessageAcknowledgedEventKind is the kind of the event AcknowledgeMessage
// appends to a task's history in the transaction that records the
// acknowledgement on the message: PRD section 6's acknowledgement, which a
// worker appends through the worker event path naming the message. Its detail
// is the message's identifier in decimal, which ParseMessageID reads. As with
// OpeningEventKind, the store records the word and internal/taskstate's table
// is what gives it a meaning.
const MessageAcknowledgedEventKind = "message-acknowledged"

// Message is one row of PRD section 8's message record: a message sent to a
// task's worker. It is appended before the send, and the three facts on it
// are each recorded once and never revised: Written or Refused, which
// exclude each other, and Acknowledged. It is the one place whether a message
// was delivered is read from (P8); no event, wake or log line answers it.
type Message struct {
	// ID is the row's identifier, which the store assigns and an
	// acknowledgement names.
	ID int64 `json:"id"`
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// Attempt is the attempt whose worker the message was sent to.
	Attempt int64 `json:"attempt"`
	// Text is what was sent, with credentials removed by the redactor the
	// store was opened with, before the row was written.
	Text string `json:"text"`
	// SentBy is who sent it, from the closed set Sender names. It has no
	// encoded form, on Resolver's terms, so it does not travel.
	SentBy Sender `json:"-"`
	// CreatedAt is when the row was appended, which precedes the send.
	CreatedAt time.Time `json:"created_at"`
	// WrittenAt is when the provider's Send returned. It proves the bytes
	// reached the worker's input and nothing about whether the worker read
	// them.
	WrittenAt Optional[time.Time] `json:"written_at"`
	// RefusedAt is when Send failed, and Refusal why.
	RefusedAt Optional[time.Time] `json:"refused_at"`
	Refusal   Optional[string]    `json:"refusal"`
	// AcknowledgedAt is when the worker's acknowledgement was recorded, and
	// AcknowledgedEvent the sequence of the event it appended in the same
	// transaction.
	AcknowledgedAt    Optional[time.Time] `json:"acknowledged_at"`
	AcknowledgedEvent Optional[int64]     `json:"acknowledged_event"`
}

// Delivery is what the record says of a message's send: written, refused,
// or unknown when neither was recorded, which is a send that never
// reported, after a crash between the row and the send's answer.
type Delivery string

// The deliveries.
const (
	DeliveryWritten Delivery = "written"
	DeliveryRefused Delivery = "refused"
	DeliveryUnknown Delivery = "unknown"
)

// Delivery reads the record's delivery fact.
func (m Message) Delivery() Delivery {
	switch {
	case m.WrittenAt.IsKnown():
		return DeliveryWritten
	case m.RefusedAt.IsKnown():
		return DeliveryRefused
	default:
		return DeliveryUnknown
	}
}

// Acknowledged reports whether the worker's acknowledgement is recorded.
func (m Message) Acknowledged() bool { return m.AcknowledgedAt.IsKnown() }

// ParseMessageID reads the detail of a MessageAcknowledgedEventKind event:
// a positive decimal message identifier and nothing else.
func ParseMessageID(detail string) (int64, error) {
	id, err := strconv.ParseInt(detail, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != detail {
		return 0, fmt.Errorf("store: %q is not a message identifier", detail)
	}
	return id, nil
}

// AppendMessage records a message before it is sent: the task, the attempt
// whose worker it is for, the text with credentials removed by the store's
// redactor, and who sent it. The attempt must be one of the task's, which the
// database holds. The zero Sender is refused with ErrNoSender and empty text
// is refused, both before anything is written. It records no delivery fact:
// MarkMessageWritten and MarkMessageRefused are the send's answer.
func (s *Store) AppendMessage(ctx context.Context, taskID string, attempt int64, text string, by Sender) (Message, error) {
	if strings.TrimSpace(taskID) == "" {
		return Message{}, errors.New("store: a message names no task")
	}
	if strings.TrimSpace(text) == "" {
		return Message{}, fmt.Errorf("store: a message to task %s carries no text", taskID)
	}
	who, err := senderText(by)
	if err != nil {
		return Message{}, fmt.Errorf("store: recording a message to task %s: %w", taskID, err)
	}
	redacted := s.redact.Redact(text)
	var out Message
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, taskID); err != nil {
			return err
		}
		var known int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM task_attempt WHERE task_id = ? AND sequence = ?`, taskID, attempt).Scan(&known); err != nil {
			return err
		}
		if known == 0 {
			return fmt.Errorf("%w: attempt %d of task %s", ErrNotFound, attempt, taskID)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO message (task_id, attempt, text, sent_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			taskID, attempt, redacted, who, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = messageByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return Message{}, fmt.Errorf("store: recording a message to task %s: %w", taskID, err)
	}
	return out, nil
}

// MarkMessageWritten records that Send returned for the message. A message
// whose delivery is already recorded, written or refused, is refused with
// ErrMessageDelivery and nothing changes; an unknown identifier is
// ErrNotFound.
func (s *Store) MarkMessageWritten(ctx context.Context, id int64) (Message, error) {
	return s.markDelivery(ctx, id, `UPDATE message SET written_at = ? WHERE id = ?`, encodeTime(nowUTC()), id)
}

// MarkMessageRefused records that Send failed for the message, with the
// reason passed through the store's redactor. It is refused on
// MarkMessageWritten's terms, and an empty reason is refused.
func (s *Store) MarkMessageRefused(ctx context.Context, id int64, reason string) (Message, error) {
	if strings.TrimSpace(reason) == "" {
		return Message{}, fmt.Errorf("store: a refused message %d records why", id)
	}
	return s.markDelivery(ctx, id, `UPDATE message SET refused_at = ?, refusal = ? WHERE id = ?`,
		encodeTime(nowUTC()), s.redact.Redact(reason), id)
}

// markDelivery is the one write behind both delivery facts: it reads the row,
// refuses one whose delivery is recorded, and writes, in one transaction.
func (s *Store) markDelivery(ctx context.Context, id int64, statement string, args ...any) (Message, error) {
	var out Message
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		m, err := messageByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Delivery() != DeliveryUnknown {
			return fmt.Errorf("%w: message %d is %s", ErrMessageDelivery, id, m.Delivery())
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
		out, err = messageByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return Message{}, fmt.Errorf("store: recording the delivery of message %d: %w", id, err)
	}
	return out, nil
}

// AcknowledgeMessage records a worker's acknowledgement of a message to its
// task: it appends a MessageAcknowledgedEventKind event naming the message to
// the task's history and records the acknowledgement, with that event's
// sequence, on the message, in one transaction, so neither exists without
// the other. A message that is not the task's, or does not exist, is
// ErrNotFound; one already acknowledged is ErrMessageAcknowledged; one whose
// send was refused is ErrMessageRefused. In each case nothing is written.
//
// A message whose delivery is unknown may be acknowledged: the
// acknowledgement is the worker saying it read the text, which proves the
// send reached it whatever the record says of the send's answer.
func (s *Store) AcknowledgeMessage(ctx context.Context, taskID string, id int64) (TaskEvent, Message, error) {
	var event TaskEvent
	var out Message
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		m, err := messageByID(ctx, tx, id)
		switch {
		case err != nil:
			return err
		case m.TaskID != taskID:
			return fmt.Errorf("%w: message %d is not task %s's", ErrNotFound, id, taskID)
		case m.Acknowledged():
			return fmt.Errorf("%w: message %d", ErrMessageAcknowledged, id)
		case m.Delivery() == DeliveryRefused:
			return fmt.Errorf("%w: message %d", ErrMessageRefused, id)
		}
		event, err = appendTaskEventTx(ctx, tx, taskID, MessageAcknowledgedEventKind,
			strconv.FormatInt(id, 10), Unknown[int64]())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE message SET acknowledged_at = ?, acknowledged_event = ? WHERE id = ?`,
			encodeTime(event.At), event.Sequence, id); err != nil {
			return err
		}
		out, err = messageByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return TaskEvent{}, Message{}, fmt.Errorf("store: acknowledging message %d of task %s: %w", id, taskID, err)
	}
	return event, out, nil
}

// TaskMessage returns one message by identifier, or ErrNotFound.
func (s *Store) TaskMessage(ctx context.Context, id int64) (Message, error) {
	if err := s.live(); err != nil {
		return Message{}, err
	}
	m, err := messageByID(ctx, s.read, id)
	if err != nil {
		return Message{}, fmt.Errorf("store: reading message %d: %w", id, err)
	}
	return m, nil
}

// TaskMessages returns every message sent to a task's workers, oldest first.
func (s *Store) TaskMessages(ctx context.Context, taskID string) ([]Message, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryMessages(ctx, fmt.Sprintf("the messages of task %s", taskID),
		messageColumns+` FROM message WHERE task_id = ? ORDER BY id`, taskID)
}

// WrittenUnacknowledgedMessages returns every message whose send was
// recorded written and whose acknowledgement is not recorded, keyed by task
// and oldest first within a task. It is the supervisor loop's read for PRD
// section 6's "an unacknowledged message surfaces once"; a refused message
// and one whose delivery is unknown are not in it, because neither was
// written.
func (s *Store) WrittenUnacknowledgedMessages(ctx context.Context) (map[string][]Message, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	all, err := s.queryMessages(ctx, "the unacknowledged messages",
		messageColumns+` FROM message WHERE written_at IS NOT NULL AND acknowledged_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]Message)
	for _, m := range all {
		out[m.TaskID] = append(out[m.TaskID], m)
	}
	return out, nil
}

func (s *Store) queryMessages(ctx context.Context, what, query string, args ...any) ([]Message, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading %s: %w", what, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", what, err)
	}
	return out, nil
}

const messageColumns = `SELECT id, task_id, attempt, text, sent_by, created_at, written_at, refused_at, refusal,
	acknowledged_at, acknowledged_event`

func messageByID(ctx context.Context, q querier, id int64) (Message, error) {
	m, err := scanMessage(q.QueryRowContext(ctx, messageColumns+` FROM message WHERE id = ?`, id))
	if isNoRows(err) {
		return Message{}, fmt.Errorf("%w: message %d", ErrNotFound, id)
	}
	return m, err
}

func scanMessage(sc scanner) (Message, error) {
	var m Message
	var sentBy, created string
	var written, refused, acknowledged Optional[string]
	if err := sc.Scan(&m.ID, &m.TaskID, &m.Attempt, &m.Text, &sentBy, &created, &written, &refused, &m.Refusal,
		&acknowledged, &m.AcknowledgedEvent); err != nil {
		return Message{}, err
	}
	var err error
	if m.SentBy, err = senderValue(sentBy, "sent_by"); err != nil {
		return Message{}, err
	}
	if m.CreatedAt, err = decodeTime(created); err != nil {
		return Message{}, err
	}
	if m.WrittenAt, err = optionalTimeValue(written, "written_at"); err != nil {
		return Message{}, err
	}
	if m.RefusedAt, err = optionalTimeValue(refused, "refused_at"); err != nil {
		return Message{}, err
	}
	if m.AcknowledgedAt, err = optionalTimeValue(acknowledged, "acknowledged_at"); err != nil {
		return Message{}, err
	}
	return m, nil
}
