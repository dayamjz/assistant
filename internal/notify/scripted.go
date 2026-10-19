package notify

import "context"

// Scripted is the stand-in Transport tests drive. It records each notice
// it is asked to deliver and returns Fail when that is set. It does not
// dial and it does not read a token. Before, Fail, and the record are for
// a test to set before a call; they are not changed underneath a delivery.
type Scripted struct {
	// Fail is returned from Deliver when set, and the notice is not recorded.
	Fail error
	// Before runs at the start of Deliver when set, before the notice is
	// recorded. A test uses it to hold one delivery.
	Before func()

	got []Notice
}

// Deliver records n, or returns Fail without recording it.
func (s *Scripted) Deliver(ctx context.Context, n Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Before != nil {
		s.Before()
	}
	if s.Fail != nil {
		return s.Fail
	}
	s.got = append(s.got, n)
	return nil
}

// Notices returns the notices Deliver recorded, in order.
func (s *Scripted) Notices() []Notice {
	out := make([]Notice, len(s.got))
	copy(out, s.got)
	return out
}
