// Package notify is the optional notification gateway. It is off unless the
// operator's own file sets config.KeyNotifications, and a repository file
// cannot set that key. It delivers a notice to one private channel and
// accepts one kind of incoming action. It does not create a task, answer a
// hold, or merge, and it has no path to the packages that do.
//
// The channel is a file internal/home names. Delivery appends a line to it.
// Nothing here listens, dials, or posts to a public audience, and the tests
// drive a scripted Transport rather than a network or a token. A notice
// marked public is refused before that file is opened.
//
// An incoming action is accepted only for an identity the service has
// already trusted. The service trusts the user identifier it recorded from
// its own process at open, which is the same fact the local socket reports
// about a peer, and it passes that credential in. A name carried in the
// action is not an identity and Accept does not read it. A kind other than
// an acknowledgement, including one that would create a task, answer a
// hold, or merge, is refused for a trusted identity too, and nothing is
// recorded.
//
// An event identifier is applied once. The record is internal/store's
// notification_event row, written after a successful delivery or acceptance
// and never on a refusal or a transport error, so a transport error leaves
// that row and every task row as they were and a later attempt of the same
// identifier can try again. A second attempt of an identifier already
// recorded does not deliver again. Send and Accept hold one lock across
// that read, the delivery, and the write, so two overlapping calls in one
// process apply an identifier once. The lock does not survive a restart;
// the row does. One delivery at a time is what the lock costs.
//
// What this does not cover: a delivery that succeeds and whose record then
// fails can be delivered again when the identifier is retried, because the
// row is written after the delivery and the table is sealed against
// changing a row. The file is not a proof anyone read it. Trust is the
// service's own user identifier, so any local peer with that identifier is
// trusted once the worker refusal in internal/service has been passed; this
// package does not know what a worker is. There is no section 9 verb and no
// phrase, because nothing here is rendered for a person.
package notify
