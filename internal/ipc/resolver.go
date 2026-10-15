package ipc

import "github.com/dayamjz/assistant/internal/store"

// HoldResolver returns who a hold resolution carried by a request on this
// protocol is recorded as having come from, which is always
// [store.ResolvedByMachineInterface].
//
// PRD section 8 requires the value meaning a person decided to be unreachable
// from any path an agent reaches, on any surface. This surface is one an agent
// reaches: MethodRunRespond and MethodTaskRespond are restricted, so a caller
// the kernel places inside an active validation stage is refused, and the
// service refuses a task's worker MethodTaskRespond, but a coordinator acting
// under the authority PRD section 9 gives it is not refused, and that is
// intended. So what has to
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

// WakeAcknowledger returns who an acknowledgement carried by a request on
// this protocol is recorded as having come from, which is always
// [store.AcknowledgedByCoordinator]. A drain records the same value as who
// it handed its rows out to, which closes nothing; only an acknowledgement
// closes a row.
//
// It is HoldResolver's rule applied to the queue. MethodWakesAcknowledge and
// the two drains, MethodWakesList and MethodWakesWait, are the
// coordinator's: the handler refuses a task's worker from the caller's
// credentials, and a coordinator acting under PRD section 9's authority is
// the one caller left. So, as with a hold, what has to hold is what a call
// may say about itself, and the two mechanisms are the same: no frame this
// package defines carries an acknowledger, so there is no field to write one
// into, and the receiver is unnamed because nothing in the request reaches
// the answer. A body that carried an acknowledger field would be decoded by
// the handler into a shape that has none and ignored.
//
// The vocabulary is internal/store's, per P14, and that package says why the
// set has no member meaning a person: no surface here witnesses one, and the
// queue is the coordinator's. This package chooses which member the protocol
// may produce and owns no part of what the members mean.
func (Request) WakeAcknowledger() store.Acknowledger {
	return store.AcknowledgedByCoordinator()
}

// MessageSender returns who a message carried by a request on this protocol
// is recorded as having sent it, which is always [store.SentByCoordinator].
//
// It is WakeAcknowledger's rule applied to PRD section 6's steering.
// MethodTaskMessage is the coordinator's: the handler refuses a task's worker
// from the caller's credentials. What has to hold is what a call may say
// about itself, and the mechanisms are the same: no frame this package
// defines carries a sender, and the receiver is unnamed because nothing in
// the request reaches the answer. internal/store says why the set has no
// member meaning a person.
func (Request) MessageSender() store.Sender {
	return store.SentByCoordinator()
}
