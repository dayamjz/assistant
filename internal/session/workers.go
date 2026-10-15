package session

import "sync"

// Membership is one live worker group: the process group a worker leads
// and the task it was created for. It is what a caller holding a process
// group learns from Workers, and it carries nothing else, because the task
// is the whole of what a worker's identity buys.
type Membership struct {
	// Task is the task the worker was created for, as the Spec named it.
	Task string
	// Group is the process group the worker leads, which is the identifier
	// agents.Worker.Group reports and the one a peer's process group is
	// compared against.
	Group int
}

// Workers is the set of live worker groups a provider has created and still
// holds, keyed by process group. It is the authority input PRD section 8's
// worker-to-program paths derive a task from: a caller's kernel credentials
// name a process, the process belongs to a group, and this set says which
// task that group's worker is. Nothing a caller states reaches it.
//
// It is a separate relation from internal/service's containment registry on
// purpose. That registry answers whether a peer is inside a validation stage
// and refuses the peer every method that drives a pipeline; this set answers
// which task a peer is the worker of and is what admits a worker's push to
// its own branch, its events to its own history, and refuses it the
// coordinator's queue. One map holding both would answer two questions with
// one lookup, which P14 forbids, and a worker registered as a stage would
// have every push it makes refused before admission.
//
// Its producers are the providers' Create, Pipe's and Tmux's, which join the
// worker's group when the session is created and leave it when the worker's
// process ends or Close ends the session; the join is unexported so a
// membership cannot be stated from outside this package. A group whose
// worker's done signal has closed is not a member even before the leave has
// run, so the answer is as current as that signal and no more: a pipe worker's
// closes when the process is reaped, while a terminal worker's is polled and
// can trail the exit by up to internal/agents' terminalPoll.
//
// A Workers is safe for concurrent use.
type Workers struct {
	mu     sync.Mutex
	groups map[int]member
}

// member is one entry: the membership and the signal that the worker's
// process has ended.
type member struct {
	membership Membership
	done       <-chan struct{}
}

// NewWorkers returns an empty set, which is a provider that has created no
// worker and so places nobody.
func NewWorkers() *Workers {
	return &Workers{groups: map[int]member{}}
}

// Member reports the membership of a process group, and whether the group
// leads a worker this set holds whose done signal has not closed.
func (w *Workers) Member(group int) (Membership, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	m, ok := w.groups[group]
	if !ok {
		return Membership{}, false
	}
	select {
	case <-m.done:
		return Membership{}, false
	default:
		return m.membership, true
	}
}

// Empty reports whether the set holds no worker at all, ended or not. A
// caller asked about a peer while this is true has nothing to place the peer
// in and need not read the peer's process group.
func (w *Workers) Empty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.groups) == 0
}

// join records a worker's group and returns the leave. A group already
// present is replaced, which happens only when the operating system has
// given a new worker the identifier of one this set still held: that worker
// has ended, or Member would have answered for it, so the newer entry is
// the one the identifier names now.
func (w *Workers) join(m Membership, done <-chan struct{}) func() {
	w.mu.Lock()
	w.groups[m.Group] = member{membership: m, done: done}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if held, ok := w.groups[m.Group]; ok && held.done == done {
			delete(w.groups, m.Group)
		}
	}
}
