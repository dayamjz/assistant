// Package redact removes credentials from text on its way into an error, a
// log, or a record. PRD section 8 gives credential removal one owner, so every
// package that persists or reports text that might carry one calls this rather
// than deciding for itself: internal/vcs takes a Redactor, internal/store
// refuses to open without one, and internal/forge and internal/stages take
// one too.
//
// # What it recognizes
//
// A credential in a URL's userinfo, which is the shape a git remote carries
// one in: the token in https://token@host/repo.git and the password in
// https://user:password@host/repo.git are both there, and both are what ends
// up in a remote URL, a git error message, and a stored repository row.
//
// The whole userinfo goes, not the password half. A token is as often the user
// half as the password half - https://TOKEN:x-oauth-basic@host and
// https://user:TOKEN@host are both in use - so keeping either half by position
// would leave the credential standing in half the cases. What is left is
// enough to see which host and which path the URL named, which is what a
// diagnostic needs.
//
// The exception is a scheme whose userinfo is a login name rather than a
// secret, and it is narrow: ssh and git carry no password, so ssh://git@host
// loses nothing by keeping its user and a reader loses the account the
// connection was made under if it does not. A userinfo with a colon in it is
// redacted whatever the scheme, because a password half is a password half.
//
// # What it does not do
//
// It does not find a credential that is not in a URL. An API token in an
// environment variable, a private key in a file, and a password a command
// printed on its own are all outside what this recognizes, and nothing here
// reports that it looked. PRD section 8 asks for removal from URLs, errors,
// and anything persisted; this covers the URL shape wherever it appears in
// that text, and the residual gap is every other shape.
//
// It does not parse. Text arriving here is an error message, a command line,
// or a log line, and a URL inside one is surrounded by prose that no URL
// parser accepts, so the scan finds URL-shaped runs and leaves everything else
// exactly as it stands.
//
// It does not report what it removed. A caller that needs the credentialed URL
// recovers it from the gate, per PRD section 8; nothing here keeps a copy.
package redact

import (
	"regexp"
	"strings"
)

// Marker is what replaces a credential. It is a fixed word rather than a run
// of asterisks so that a reader of a redacted URL can tell redaction from a
// password that happens to be punctuation, and so that a test can assert on it.
const Marker = "redacted"

// loginSchemes are the schemes whose userinfo, when it carries no password
// half, is an account name rather than a secret. Both are the git transports
// that authenticate outside the URL: the userinfo names who to connect as and
// the credential is a key the transport finds elsewhere.
//
// A scheme not in this set has its userinfo removed whatever the userinfo
// looks like, which is the answer that claims the least about a string this
// package cannot inspect.
var loginSchemes = map[string]bool{
	"ssh":     true,
	"git":     true,
	"git+ssh": true,
}

// credentialed matches a URL's scheme and userinfo. The userinfo is bounded by
// the characters that would end an authority, so a path or a query holding an
// at sign later in the URL cannot be mistaken for one.
var credentialed = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*)://([^/?#@\s]*)@`)

// Redactor removes credentials from text. It holds nothing, so the zero value
// is usable and safe for concurrent use, which internal/vcs requires of a
// Redactor because a repository calls it from whichever invocation failed.
type Redactor struct{}

// New returns the redactor. It exists so a caller wires this package by naming
// a constructor rather than by knowing that the zero value works, which keeps
// the choice reversible if this ever needs configuring.
func New() Redactor { return Redactor{} }

// Redact returns s with the credential in every URL-shaped run replaced by
// Marker. Text holding no URL comes back unchanged, and so does a URL with no
// userinfo, so it is safe to call on anything.
func (Redactor) Redact(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return credentialed.ReplaceAllStringFunc(s, func(match string) string {
		parts := credentialed.FindStringSubmatch(match)
		scheme, userinfo := parts[1], parts[2]
		if loginSchemes[strings.ToLower(scheme)] && !strings.Contains(userinfo, ":") {
			return match
		}
		return scheme + "://" + Marker + "@"
	})
}
