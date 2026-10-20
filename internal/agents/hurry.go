package agents

import "context"

// hurryKey carries an invocation's hurry channel on its context.
type hurryKey struct{}

// WithHurry returns ctx carrying hurry. An invocation run on the returned
// context terminates its process tree on runProcess's terms, except that once
// hurry is closed every polite wait of that termination is cut short and the
// forceful signal is sent at once, whether the termination has begun or not.
//
// It is for a caller that is about to give up on the invocation's process
// tree and exit: a service whose stop_grace ran out. It shortens the
// invocation's own termination and widens nothing: what is signalled is
// decided exactly as before, by the invocation that started the tree and
// holds its leader, so the residual gap is terminateTree's own window, a
// group checked non-empty that empties and is reused before the signal. A
// nil hurry, and a context carrying none, never hurries anything.
//
// The hurry acts as well as shortens: closing it starts the termination of
// an invocation's tree even when nobody cancelled its context, and of a
// worker's even when nobody closed it, so a descendant holding an output
// pipe open, which holds the process's Wait, cannot keep the tree alive.
//
// A worker or subordinate coordinator started on the returned context, by
// OpenWorker, OpenSubordinate or OpenTerminalWorker, keeps the channel for
// its lifetime: it is closed the moment the channel is, and its own
// termination, on its leader's exit or on Close, is hurried the same way.
func WithHurry(ctx context.Context, hurry <-chan struct{}) context.Context {
	return context.WithValue(ctx, hurryKey{}, hurry)
}

// hurryOf is the hurry channel ctx carries, nil when it carries none.
func hurryOf(ctx context.Context) <-chan struct{} {
	hurry, _ := ctx.Value(hurryKey{}).(<-chan struct{})
	return hurry
}
