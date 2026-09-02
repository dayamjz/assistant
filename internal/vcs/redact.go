package vcs

import (
	"regexp"
	"strings"
)

// Redactor removes credentials from text on its way into an error or a log.
//
// PRD section 8 makes a redact module the single owner of this, so when that
// module exists a caller passes its implementation with WithRedactor and this
// package stops being a second opinion on the question.
type Redactor interface {
	// Redact returns s with any credential it recognizes replaced. It must be
	// safe to call on text that holds no credential, and on text that is not a
	// URL, and it must be safe to call from several goroutines at once,
	// because a Repository is safe for concurrent use and calls this from
	// whichever invocation failed.
	Redact(s string) string
}

// RedactorFunc adapts a function to Redactor.
type RedactorFunc func(string) string

// Redact calls f.
func (f RedactorFunc) Redact(s string) string { return f(s) }

// urlUserinfo matches the userinfo of a URL that carries a scheme, which is
// the shape a credentialed git remote takes: scheme://user:password@host.
//
// What the pattern requires is a scheme, then at least one character before
// the @, so "http://@" does not match and neither does a mail-like token that
// carries no scheme. It requires nothing after the @, so "https://user:pw@"
// with no host does match and is redacted.
var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)

// defaultRedactor is what this package uses when no Redactor is supplied. It
// covers exactly one shape and claims nothing beyond it: the userinfo of a URL
// that carries a scheme. It does not find a password that reached the text by
// some other route, such as one echoed by a credential helper or embedded in a
// scheme-less remote, so a caller holding secrets of another shape supplies
// its own Redactor.
type defaultRedactor struct{}

func (defaultRedactor) Redact(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return urlUserinfo.ReplaceAllString(s, "${1}"+redactedUserinfo+"@")
}

// redactedUserinfo is what replaces the userinfo of a credentialed URL. It is
// not a valid userinfo, so a redacted URL cannot be mistaken for a usable one.
const redactedUserinfo = "REDACTED"
