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
// A bearer credential in an HTTP Authorization value, which is the shape an
// API key takes on its way to a hosted service: the token in
// "Authorization: Bearer TOKEN", and in "Bearer TOKEN" wherever it stands
// alone, is replaced and the word Bearer is kept, so a reader can still see a
// credential was there. An Authorization value under any other scheme loses
// everything after the header name to the end of its line, because this
// package cannot tell which part of an unknown scheme is the secret. The
// decision-model client in internal/judge is the caller this shape was added
// for; it builds no error from its own header, and this is the owner that
// removes one wherever text carrying it reaches an error, a log, or a record.
//
// A bare "Bearer" counts only when what follows is at least eight characters
// a token may be made of, so prose such as "the bearer of" stays as written.
// A shorter token is the residual gap of that rule, and so is a token split
// across lines.
//
// The exception is a scheme whose userinfo is a login name rather than a
// secret, and it is narrow: ssh and git carry no password, so ssh://git@host
// loses nothing by keeping its user and a reader loses the account the
// connection was made under if it does not. A userinfo with a colon in it is
// redacted whatever the scheme, because a password half is a password half.
//
// A bare token value, which is the shape a person pastes: a value standing
// anywhere in the text, with no header or URL around it, whose prefix is one
// of the rows of tokenShapes in token.go. Those are the Anthropic "sk-ant-"
// values (API keys, Admin API keys, and OAuth access tokens of the
// "sk-ant-oat01-" form), the six GitHub prefixes "ghp_", "github_pat_",
// "gho_", "ghu_", "ghs_" and "ghr_", GitLab's "glpat-" (personal,
// impersonation, project and group access tokens), and OpenAI's "sk-" keys,
// "sk-proj-", "sk-svcacct-" and "sk-admin-" among them. Each row names the
// source its prefix and its length come from. The value is cut where it
// stands, keeping its prefix and the surrounding text, and only when it is at
// least the row's minimum length, so a near-miss such as "sk-antique", "ghp_"
// followed by three characters, or "glpat-" in prose is left as written.
//
// The token from "claude setup-token" (CLAUDE_CODE_OAUTH_TOKEN) is not a row
// of its own: no Anthropic source this package could cite states its prefix,
// so it is cut only if it begins "sk-ant-" like the OAuth access token
// Anthropic's SDK example shows, and a value of any other shape stands. Every
// row has the same residual gaps: a value shorter than the row's floor, a
// value split across lines, and a value issued under a prefix the table does
// not list are not cut.
//
// # What it does not do
//
// It does not find a credential in any other shape. A token from an issuer
// with no row in tokenShapes, a private key in a file, and a password a
// command printed on its own are all outside what this recognizes; an
// environment variable's assignment is recognized only when its value matches
// a row; and nothing here reports that it looked. PRD section 8 asks for
// removal from URLs, errors, and anything persisted; this covers the URL,
// bearer and bare token shapes wherever they appear in that text, and the
// residual gap is every other shape.
//
// It does not parse the whole string into a structure. Text arriving here is
// an error message, a command line, or a log line, and a URL inside one is
// surrounded by prose that no URL parser accepts, so credentials in URLs are
// found by scanning for URL-shaped runs rather than parsing. Bare token values
// are found the same way, by prefix and character class over the raw text, not
// by parsing the surrounding line. Bytes that match none of those scans are
// left as written.
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

// authorization matches an Authorization header's value to the end of its
// line, whatever its scheme. The name is matched without regard to case,
// because HTTP header names are case-insensitive and a log may print either.
var authorization = regexp.MustCompile(`(?i)\b(authorization)(\s*[:=]\s*)[^\r\n]+`)

// bearer matches a bearer token standing on its own. Its token is at least
// eight characters from the set RFC 6750 allows a bearer token, so the word in
// ordinary prose is left alone.
var bearer = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9\-._~+/]{8,}=*`)

// Redactor removes credentials from text. It holds nothing, so the zero value
// is usable and safe for concurrent use, which internal/vcs requires of a
// Redactor because a repository calls it from whichever invocation failed.
type Redactor struct{}

// New returns the redactor. It exists so a caller wires this package by naming
// a constructor rather than by knowing that the zero value works, which keeps
// the choice reversible if this ever needs configuring.
func New() Redactor { return Redactor{} }

// Redact returns s with every credential it recognizes replaced by Marker: the
// userinfo of every URL-shaped run, every bearer or Authorization value, and
// every bare token value tokenShapes recognizes.
// Text holding neither comes back unchanged, and so does a URL with no
// userinfo, so it is safe to call on anything.
func (Redactor) Redact(s string) string {
	s = redactTokens(s)
	s = redactAuthorization(s)
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

// redactAuthorization replaces every Authorization value and every bearer
// token in s. An Authorization value is replaced whole, keeping the word
// Bearer when that was its scheme so the replacement still says what kind of
// credential stood there; a bearer token outside a header is replaced on its
// own.
func redactAuthorization(s string) string {
	lower := strings.ToLower(s)
	if !strings.Contains(lower, "bearer") && !strings.Contains(lower, "authorization") {
		return s
	}
	s = authorization.ReplaceAllStringFunc(s, func(match string) string {
		parts := authorization.FindStringSubmatch(match)
		value := strings.TrimSpace(match[len(parts[1])+len(parts[2]):])
		if scheme, _, ok := strings.Cut(value, " "); ok && strings.EqualFold(scheme, "bearer") {
			return parts[1] + parts[2] + scheme + " " + Marker
		}
		return parts[1] + parts[2] + Marker
	})
	return bearer.ReplaceAllStringFunc(s, func(match string) string {
		return bearer.FindStringSubmatch(match)[1] + " " + Marker
	})
}
