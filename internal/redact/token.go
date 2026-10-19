package redact

import "strings"

// charClass is the set of bytes a token's value may be made of after its
// prefix. A value is the longest run of these bytes that follows the prefix,
// so the run ends at the first byte outside the class and the text after it
// is kept.
type charClass uint8

const (
	// alnum is the base62 alphabet GitHub's token format announcement gives
	// its tokens: a-z, A-Z and 0-9.
	alnum charClass = iota
	// alnumUnderscore adds the underscore, which a fine-grained GitHub token
	// carries as a separator after its prefix.
	alnumUnderscore
	// alnumDash adds the dash and the underscore, the base64url alphabet the
	// Anthropic and OpenAI shapes are drawn from as their published examples
	// show them (sk-ant-api03-..., sk-proj-...).
	alnumDash
	// alnumDashDot adds the dot to alnumDash, which a routable GitLab token
	// and a stateless GitHub installation token carry between their segments.
	alnumDashDot
)

// holds reports whether b belongs to the class.
func (c charClass) holds(b byte) bool {
	switch {
	case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		return true
	case b == '_':
		return c != alnum
	case b == '-':
		return c == alnumDash || c == alnumDashDot
	case b == '.':
		return c == alnumDashDot
	}
	return false
}

// tokenShape is one row of the bare token table: a value is cut when it
// starts with prefix at a word boundary, continues in class, and is at least
// minLength bytes long counting the prefix, which is how every source below
// states a length. A shorter run is left as written, so the prefix in prose
// and a truncated placeholder such as "sk-ant-api03-..." are not cut.
type tokenShape struct {
	prefix    string
	minLength int
	class     charClass
}

// tokenShapes is the table of bare token values this package recognizes.
// Every prefix is taken from the source named beside it, and so is every
// length a source states; where a source states none, the row says the floor
// is this package's own choice. A row is matched in order and the more
// specific prefix comes first, so "sk-ant-" is cut as an Anthropic value
// before the OpenAI row could see it.
//
// A replaced value keeps its prefix and loses the rest to Marker, so a reader
// can still tell which kind of credential stood there, and the replacement is
// shorter than every row's minimum, so redacting it again changes nothing.
var tokenShapes = []tokenShape{
	// Anthropic. "sk-ant-api..." is the API key and "sk-ant-admin..." the
	// Admin API key in Anthropic's own documentation
	// (platform.claude.com/docs/en/manage-claude/authentication and
	// .../admin-api), and "sk-ant-oat01-..." is the OAuth access token in
	// anthropics/anthropic-sdk-python's examples/workload_identity.py at
	// b5cc700. All three start "sk-ant-", which is the prefix the row
	// matches. Anthropic documents no length, so the floor of 32 is this
	// package's choice: long enough that the documented placeholders are not
	// cut, and a shorter genuine value would be missed.
	{prefix: "sk-ant-", minLength: 32, class: alnumDash},

	// GitHub. The six prefixes are the table in GitHub's "About
	// authentication to GitHub" (docs.github.com, "GitHub's token formats").
	// The length is GitHub's "Behind GitHub's new authentication token
	// formats" (github.blog, 2021): 30 random base62 characters and a 6
	// character checksum after the prefix, 40 in all. GitHub documents no
	// length for "github_pat_"; the 40 floor there is the one openai/codex
	// applies to every GitHub prefix (codex-rs/network-proxy/src/
	// credential_broker/providers/github.rs at 1bfd383). GitHub's docs say an
	// installation token now also comes in a stateless "ghs_APPID_JWT" form,
	// so that row allows a JWT's dots and dashes.
	{prefix: "github_pat_", minLength: 40, class: alnumUnderscore},
	{prefix: "ghp_", minLength: 40, class: alnum},
	{prefix: "gho_", minLength: 40, class: alnum},
	{prefix: "ghu_", minLength: 40, class: alnum},
	{prefix: "ghr_", minLength: 40, class: alnum},
	{prefix: "ghs_", minLength: 40, class: alnumDashDot},

	// GitLab. "glpat-" is the prefix GitLab's token overview
	// (docs.gitlab.com/security/tokens) lists for a personal, an
	// impersonation, a project and a group access token alike. The length is
	// gitlab-org/gitlab merge request 169322: a non-routable token is 20
	// characters after the prefix and a routable one 37 to 310, with dots
	// between its segments, so the floor is the shorter form's 26.
	{prefix: "glpat-", minLength: 26, class: alnumDashDot},

	// OpenAI. openai/codex lists the key prefixes "sk-proj-", "sk-svcacct-",
	// "sk-admin-" and "sk-" and a minimum key length of 51
	// (codex-rs/network-proxy/src/credential_broker/providers/openai.rs at
	// 1bfd383). Every one of them starts "sk-", so one row covers them. It
	// cuts any "sk-" value of that shape whoever issued it, which removes a
	// credential this package was not told about rather than keeping one.
	{prefix: "sk-", minLength: 51, class: alnumDash},
}

// redactTokens replaces every bare token value tokenShapes recognizes in s,
// keeping each value's prefix and the text around it.
func redactTokens(s string) string {
	for _, shape := range tokenShapes {
		if strings.Contains(s, shape.prefix) {
			s = shape.redact(s)
		}
	}
	return s
}

// redact replaces every value of this shape in s. A prefix counts only at a
// word boundary, where the byte before it could not be part of a token, so
// "myglpat-..." and "task-..." are left alone. A value ending in a dot loses
// the dot from the run before its length is counted, because a sentence that
// ends on a token puts its full stop there and no shape here ends in one.
func (t tokenShape) redact(s string) string {
	var out strings.Builder
	rest := s
	for {
		i := strings.Index(rest, t.prefix)
		if i < 0 {
			break
		}
		start := i + len(t.prefix)
		end := start
		for end < len(rest) && t.class.holds(rest[end]) {
			end++
		}
		for end > start && rest[end-1] == '.' {
			end--
		}
		if (i > 0 && t.class.holds(rest[i-1])) || end-i < t.minLength {
			out.WriteString(rest[:start])
			rest = rest[start:]
			continue
		}
		out.WriteString(rest[:start])
		out.WriteString(Marker)
		rest = rest[end:]
	}
	if out.Len() == 0 {
		return s
	}
	out.WriteString(rest)
	return out.String()
}
