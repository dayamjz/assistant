package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

// MaxEventIDBytes is the most an event identifier may carry. A longer one
// is refused rather than truncated.
const MaxEventIDBytes = 128

// MaxNoticeBytes is the most text a notice may carry. A longer one is
// refused rather than cut, which would deliver a notice the caller did not
// write.
const MaxNoticeBytes = 4096

// The kinds an action can name. KindAck is the only one Accept applies.
// The other three are the authorizing operations this gateway refuses even
// for an identity it trusts, so a caller cannot reach them by naming them.
const (
	// KindAck records that the event was seen. It changes no task.
	KindAck = "ack"
	// KindCreateTask asks to create a task. Accept refuses it.
	KindCreateTask = "create_task"
	// KindAnswerHold asks to answer a hold. Accept refuses it.
	KindAnswerHold = "answer_hold"
	// KindMerge asks to merge. Accept refuses it.
	KindMerge = "merge"
)

var (
	// ErrDisabled reports that the gateway is off. Nothing was delivered
	// and nothing was recorded.
	ErrDisabled = errors.New("notify: the gateway is off")
	// ErrUntrusted reports that the identity is not one the service has
	// already trusted. A name in the action is not a remedy.
	ErrUntrusted = errors.New("notify: the caller is not an identity this service trusts")
	// ErrScope reports that the action would authorize work. The gateway
	// does not create a task, answer a hold, or merge.
	ErrScope = errors.New("notify: the gateway cannot create a task, answer a hold, or merge")
	// ErrPublic reports that the notice asked for a public audience. The
	// only audience this package delivers to is the private channel.
	ErrPublic = errors.New("notify: a notice is delivered only on a private channel")
	// ErrNoEvent reports that an event identifier is missing or not one
	// this package will record.
	ErrNoEvent = errors.New("notify: an event has no usable identifier")
	// ErrNotice reports that the notice text cannot be one line of the
	// private channel's log.
	ErrNotice = errors.New("notify: a notice is one line of text")
)

// Identity is the local-socket credential the service already holds. UID is
// what Trust records and what Accept compares. A display name is not a
// field of this type.
type Identity struct {
	// UID is the user identifier.
	UID int
	// GID is the primary group identifier, recorded with the user and not
	// compared.
	GID int
	// PID is the process identifier at the time the service observed it.
	// It is not compared: a process identifier is reused, and the trust
	// decision is the user.
	PID int
}

// Notice is one outbound notification. Public asks for an audience this
// package refuses.
type Notice struct {
	// EventID is the identifier later attempts of this notice share.
	EventID string
	// Text is the line, without a break.
	Text string
	// Public is set when the caller asked to post publicly. Deliver and
	// Send refuse that.
	Public bool
}

// Action is one incoming request. User is the name a payload may claim.
// Accept does not read it.
type Action struct {
	// EventID is the identifier later attempts of this action share.
	EventID string
	// User is ignored. It is here so a payload can carry a name and a test
	// can show the decision did not use it.
	User string
	// Kind is what the caller asked for. Only KindAck is applied.
	Kind string
}

// Result is what Send or Accept did. ActorUID is set by Accept and is the
// trusted identity's user identifier, never a name from the action.
type Result struct {
	// EventID is the identifier that was applied or already recorded.
	EventID string
	// Duplicate is true when the identifier was already recorded and this
	// call did not deliver or record again.
	Duplicate bool
	// ActorUID is the user identifier the acceptance was decided for.
	ActorUID int
}

// Ledger is the record of event identifiers already applied. Seen reports
// whether Record has already kept id. Record writes id once and reports
// whether this call wrote it; a second call leaves the first row and
// reports false.
type Ledger interface {
	Seen(ctx context.Context, eventID string) (bool, error)
	Record(ctx context.Context, eventID string) (fresh bool, err error)
}

// Options builds a Gateway. A gateway that is off never calls Transport or
// Ledger. One that is on and has no transport or no ledger refuses rather
// than delivering somewhere else.
type Options struct {
	// Enabled is whether the operator turned the gateway on.
	Enabled bool
	// Transport delivers a notice. It is not called when Enabled is false.
	Transport Transport
	// Ledger records applied event identifiers.
	Ledger Ledger
}

// Gateway applies notices and actions. The zero value is off and refuses
// every call.
type Gateway struct {
	mu        sync.Mutex
	enabled   bool
	transport Transport
	ledger    Ledger
	trusted   map[int]struct{}
}

// New returns a gateway.
func New(o Options) *Gateway {
	return &Gateway{
		enabled:   o.Enabled,
		transport: o.Transport,
		ledger:    o.Ledger,
		trusted:   map[int]struct{}{},
	}
}

// Trust records id as an identity the service already trusts. The user
// identifier is the whole of the decision; a later Accept with any other
// user identifier is refused.
func (g *Gateway) Trust(id Identity) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.trusted == nil {
		g.trusted = map[int]struct{}{}
	}
	g.trusted[id.UID] = struct{}{}
}

// Send delivers n once. A duplicate identifier returns a result and does
// not deliver again. A public audience, a gateway that is off, and a
// transport error deliver nothing and record nothing.
func (g *Gateway) Send(ctx context.Context, n Notice) (Result, error) {
	if err := g.ready(); err != nil {
		return Result{}, err
	}
	if n.Public {
		return Result{}, ErrPublic
	}
	if err := checkEventID(n.EventID); err != nil {
		return Result{}, err
	}
	if err := checkText(n.Text); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.apply(ctx, n.EventID, func() error {
		return g.transport.Deliver(ctx, n)
	})
}

// Accept records an acknowledgement from who, once. who is the only
// identity the decision uses. action.User is not read. An identity this
// gateway was not told to trust is refused and not recorded. A kind other
// than KindAck is refused and not recorded, including KindCreateTask,
// KindAnswerHold, and KindMerge.
func (g *Gateway) Accept(ctx context.Context, action Action, who Identity) (Result, error) {
	if err := g.ready(); err != nil {
		return Result{}, err
	}
	if err := checkEventID(action.EventID); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.trusted[who.UID]; !ok {
		return Result{}, ErrUntrusted
	}
	if action.Kind != KindAck {
		return Result{}, ErrScope
	}
	res, err := g.apply(ctx, action.EventID, func() error { return nil })
	if err != nil {
		return Result{}, err
	}
	res.ActorUID = who.UID
	return res, nil
}

// ready reports whether a call may proceed. A nil or off gateway is
// ErrDisabled, which is checked before any field that would panic, so the
// zero value refuses.
func (g *Gateway) ready() error {
	if g == nil || !g.enabled {
		return ErrDisabled
	}
	if g.transport == nil || g.ledger == nil {
		return fmt.Errorf("notify: the gateway is on and has no private channel")
	}
	return nil
}

// apply records id after fn succeeds. A recorded id returns a duplicate
// and does not call fn.
func (g *Gateway) apply(ctx context.Context, eventID string, fn func() error) (Result, error) {
	seen, err := g.ledger.Seen(ctx, eventID)
	if err != nil {
		return Result{}, err
	}
	if seen {
		return Result{EventID: eventID, Duplicate: true}, nil
	}
	if err := fn(); err != nil {
		return Result{}, err
	}
	fresh, err := g.ledger.Record(ctx, eventID)
	if err != nil {
		return Result{}, err
	}
	return Result{EventID: eventID, Duplicate: !fresh}, nil
}

func checkEventID(id string) error {
	if id == "" || len(id) > MaxEventIDBytes {
		return ErrNoEvent
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return fmt.Errorf("%w: %q", ErrNoEvent, id)
		}
	}
	return nil
}

func checkText(text string) error {
	if len(text) > MaxNoticeBytes || !utf8.ValidString(text) {
		return ErrNotice
	}
	for _, r := range text {
		if r == '\n' || r == '\r' {
			return ErrNotice
		}
	}
	return nil
}
