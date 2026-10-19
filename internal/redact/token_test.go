package redact_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/redact"
)

// synthetic builds a value of a row's documented shape: the prefix and then
// body characters up to length in all. The body is a fixed pattern assembled
// at run time rather than a literal, so no value in this file reads as a real
// credential to a scanner, and none is one.
func synthetic(prefix, body string, length int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for b.Len() < length {
		b.WriteString(body)
	}
	return b.String()[:length]
}

// bareToken is one synthetic value of a row's shape, the prefix the
// replacement keeps, and a near-miss of the same shape one character short of
// the row's floor.
type bareToken struct {
	name, prefix, value, nearMiss string
}

// bareTokens is one value per row of the table, each at exactly its row's
// minimum length so the floor itself is what admits it.
func bareTokens() []bareToken {
	shape := func(name, keep, prefix, body string, length int) bareToken {
		return bareToken{name, keep, synthetic(prefix, body, length), synthetic(prefix, body, length-1)}
	}
	return []bareToken{
		shape("anthropic api key", "sk-ant-", "sk-ant-api03-", "Ab3_x-Yz9Q", 32),
		shape("anthropic admin key", "sk-ant-", "sk-ant-admin01-", "Wv5-Ut4_Sr", 32),
		shape("anthropic oauth token", "sk-ant-", "sk-ant-oat01-", "Qx7-Lm2_Rt", 32),
		shape("github classic pat", "ghp_", "ghp_", "aB3dE6gH9k", 40),
		shape("github fine-grained pat", "github_pat_", "github_pat_", "11AB_cd34E", 40),
		shape("github oauth token", "gho_", "gho_", "Zy8Xw7Vu6T", 40),
		shape("github user token", "ghu_", "ghu_", "Mn4Op5Qr6S", 40),
		shape("github installation token", "ghs_", "ghs_", "12345_eyJh.bG-ci", 40),
		shape("github refresh token", "ghr_", "ghr_", "Jk1Lm2Np3Q", 40),
		shape("gitlab pat", "glpat-", "glpat-", "ts8y8Mk2gx", 26),
		shape("openai project key", "sk-", "sk-proj-", "Pq9_Rs8-Tu", 51),
		shape("openai service account key", "sk-", "sk-svcacct-", "Lk2_Jh3-Gf", 51),
		shape("openai legacy key", "sk-", "sk-", "T3BlbkFJab", 51),
	}
}

// A prefix counts at a word boundary when the byte before it is not part of
// that row's character class, so an underscore before ghp_ is a boundary but
// an alphanumeric byte is not, and for github_pat_ an underscore before the
// prefix is inside a longer token while a hyphen is a boundary.
func TestABareTokenPrefixRespectsItsRowBoundary(t *testing.T) {
	t.Parallel()
	ghp := synthetic("ghp_", "aB3dE6gH9k", 40)
	r := redact.New()
	wantGH := "failed_ghp_" + redact.Marker
	if got := r.Redact("failed_" + ghp); got != wantGH {
		t.Fatalf("Redact(%q) = %q, want %q", "failed_"+ghp, got, wantGH)
	}
	embedded := "x" + ghp
	if got := r.Redact(embedded); got != embedded {
		t.Fatalf("Redact(%q) = %q, want it unchanged", embedded, got)
	}
	pat := synthetic("github_pat_", "11AB_cd34E", 40)
	if got := r.Redact("prefix_" + pat); got != "prefix_"+pat {
		t.Fatalf("Redact(%q) = %q, want underscore-glued prefix left alone", "prefix_"+pat, got)
	}
	wantPAT := "failed-github_pat_" + redact.Marker
	if got := r.Redact("failed-" + pat); got != wantPAT {
		t.Fatalf("Redact(%q) = %q, want %q", "failed-"+pat, got, wantPAT)
	}
}

// A bare value of each documented shape is cut where it stands: the prefix
// and the text around it stay and the rest of the value becomes the marker.
// The near-miss one character short of the row's floor is the control that
// the floor is what decides, so a scan that cut every prefix fails here, and
// so does one that cut nothing.
func TestABareTokenValueDoesNotSurvive(t *testing.T) {
	t.Parallel()
	for _, tok := range bareTokens() {
		t.Run(tok.name, func(t *testing.T) {
			t.Parallel()
			text := "pasted " + tok.value + " into the note"
			got := redact.New().Redact(text)
			if want := "pasted " + tok.prefix + redact.Marker + " into the note"; got != want {
				t.Fatalf("Redact(%q) = %q, want %q", text, got, want)
			}
			miss := "pasted " + tok.nearMiss + " into the note"
			if got := redact.New().Redact(miss); got != miss {
				t.Fatalf("Redact(%q) = %q, want the near-miss left alone", miss, got)
			}
		})
	}
}

// A routable GitLab token carries dots between its segments and is longer
// than the floor, and it goes whole rather than up to its first dot.
func TestARoutableGitLabTokenGoesWhole(t *testing.T) {
	t.Parallel()
	value := synthetic("glpat-", "YzowCm86MQp1OjFSDd", 50) + ".01.1a2b3c4d5"
	got := redact.New().Redact("token " + value + " end")
	if want := "token glpat-" + redact.Marker + " end"; got != want {
		t.Fatalf("Redact = %q, want %q", got, want)
	}
}

// Prose that names a prefix, a word that contains one, and the placeholders
// the issuers' own documentation prints are not credentials, so they are left
// as written.
func TestABareTokenNearMissIsLeftAlone(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"sk-antique furniture is not a key",
		"the token was ghp_abc and nothing more",
		"a word such as myglpat-" + strings.Repeat("x", 30) + " is not a token",
		"a GitLab token starts with glpat- and then its value",
		"the glpat-prefixed tokens are personal",
		"export ANTHROPIC_API_KEY=sk-ant-api03-...",
		"export OPENAI_API_KEY=sk-proj-...",
		"task-" + strings.Repeat("a", 60),
		"risk-" + strings.Repeat("b", 60),
		"GHP_" + strings.Repeat("A", 40),
	} {
		if got := redact.New().Redact(text); got != text {
			t.Errorf("Redact(%q) = %q, want it unchanged", text, got)
		}
	}
}

// Two values of different shapes in one message are both cut, and a value
// ending a sentence keeps the sentence's full stop.
func TestTwoBareTokensOfDifferentShapesAreBothCut(t *testing.T) {
	t.Parallel()
	anthropic := synthetic("sk-ant-api03-", "Ab3_x-Yz9Q", 60)
	github := synthetic("ghp_", "aB3dE6gH9k", 40)
	text := "key " + anthropic + " and token " + github + "."
	got := redact.New().Redact(text)
	if want := "key sk-ant-" + redact.Marker + " and token ghp_" + redact.Marker + "."; got != want {
		t.Fatalf("Redact(%q) = %q, want %q", text, got, want)
	}
	if again := redact.New().Redact(got); again != got {
		t.Fatalf("redacting a redacted message changed it again: %q", again)
	}
}

// A bare value inside the shapes this package already recognized is still
// gone: in an Authorization header, after Bearer, in a URL's userinfo, and in
// an environment variable's assignment.
func TestABareTokenInsideAnotherShapeDoesNotSurvive(t *testing.T) {
	t.Parallel()
	value := synthetic("github_pat_", "11AB_cd34E", 82)
	secret := value[len("github_pat_"):]
	for _, text := range []string{
		"Authorization: token " + value,
		"Bearer " + value,
		"https://" + value + "@github.com/o/r.git",
		"GH_TOKEN=" + value + " gh pr list",
	} {
		got := redact.New().Redact(text)
		if strings.Contains(got, secret) {
			t.Errorf("redacting %q left the value in %q", text, got)
		}
		if !strings.Contains(got, redact.Marker) {
			t.Errorf("redacting %q produced %q, which carries no marker", text, got)
		}
	}
}
