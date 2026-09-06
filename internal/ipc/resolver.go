package ipc

import "github.com/dayamjz/assistant/internal/store"

// HoldResolver returns who a hold resolution carried by a request on this
// protocol is recorded as having come from, which is always
// [store.ResolvedByMachineInterface].
//
// PRD section 8 requires the value meaning a person decided to be unreachable
// from any path an agent reaches, on any surface. This surface is one an agent
// reaches: MethodRunRespond is restricted, so a caller the kernel places inside
// an active validation stage is refused, but a coordinator acting under the
// authority PRD section 9 gives it is not, and that is intended. So what has to
// hold here is not who may call, but what a call may say about itself.
//
// Two things put the person value out of reach, and neither is a check a caller
// could satisfy. No frame this package defines carries a resolver, in the same
// way none carries a process identifier or a user, so there is no field to
// write one into. And this answer is derived rather than read: the receiver is
// unnamed because nothing in the request reaches it, so no method, no marker,
// and no parameter body a caller composes changes it.
//
// What it costs is that a person answering through a client of the machine
// interface is recorded the same way an agent is. That understates who decided
// rather than overstating it, and the alternative is a claim this connection
// does not establish: a person's client and an agent's client arrive on the
// same socket as the same user, and a client that said which it was would be
// authorizing itself. [store.ResolvedByPerson] belongs to a surface that
// witnessed the person, and this repository has none yet.
//
// The vocabulary is internal/store's, per P14. This package chooses which
// member the protocol may produce and owns no part of what the members mean.
func (Request) HoldResolver() store.Resolver {
	return store.ResolvedByMachineInterface()
}
