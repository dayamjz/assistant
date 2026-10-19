package notify

import (
	"context"
	"fmt"
	"os"
)

// Transport delivers one notice. An implementation posts nowhere public
// and listens nowhere: the interface has no method that binds an address.
type Transport interface {
	Deliver(ctx context.Context, n Notice) error
}

// Private is the one transport this package ships. It appends a notice to
// a file and refuses a public audience before opening that file. Path is
// the file internal/home names; an empty path refuses rather than choosing
// another destination.
type Private struct {
	// Path is the private channel. Delivery creates it when it is absent
	// and leaves it readable by the owner alone.
	Path string
}

// Deliver appends n to Path. A public notice is refused and the file is
// not opened. A notice the gateway has already checked can still be
// refused here, so a caller that holds a Private directly meets the same
// rule.
func (p Private) Deliver(ctx context.Context, n Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.Public || p.Path == "" {
		if n.Public {
			return ErrPublic
		}
		return fmt.Errorf("notify: the private channel has no path")
	}
	if err := checkEventID(n.EventID); err != nil {
		return err
	}
	if err := checkText(n.Text); err != nil {
		return err
	}
	f, err := os.OpenFile(p.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("notify: opening the private channel: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("notify: restricting the private channel: %w", err)
	}
	line := n.EventID
	if n.Text != "" {
		line += " " + n.Text
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("notify: writing the private channel: %w", err)
	}
	return nil
}
