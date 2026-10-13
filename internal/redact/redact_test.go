package redact_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The redactor is what internal/vcs and internal/store are handed, so it has
// to satisfy that interface and not merely look like it.
var _ vcs.Redactor = redact.New()

func TestACredentialInAURLDoesNotSurvive(t *testing.T) {
	t.Parallel()
	secrets := []struct {
		name string
		text string
		gone string
	}{
		{"a password half", "https://user:s3cr3t@example.com/repo.git", "s3cr3t"},
		{"a token in the user half", "https://ghp_abcdef@github.com/o/r.git", "ghp_abcdef"},
		{"a token in the user half with a fixed password", "https://ghp_abcdef:x-oauth-basic@github.com/o/r.git", "ghp_abcdef"},
		{"one inside an error message", `fatal: could not read from https://u:p4ss@example.com/r.git: denied`, "p4ss"},
		{"one under an unusual scheme", "svn+https://u:p4ss@example.com/r", "p4ss"},
		{"a password on an ssh URL", "ssh://git:p4ss@example.com/r.git", "p4ss"},
	}
	for _, s := range secrets {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			got := redact.New().Redact(s.text)
			if strings.Contains(got, s.gone) {
				t.Fatalf("redacting %q left %q in %q", s.text, s.gone, got)
			}
			if !strings.Contains(got, redact.Marker) {
				t.Fatalf("redacting %q produced %q, which carries no marker", s.text, got)
			}
		})
	}
}

func TestTextWithNothingToRemoveComesBackAsItStands(t *testing.T) {
	t.Parallel()
	unchanged := []string{
		"",
		"nothing here at all",
		"https://example.com/repo.git",
		"git@github.com:owner/repo.git",
		"ssh://git@example.com/owner/repo.git",
		"see https://example.com/docs#section for the at sign in a@b",
		"user@example.com wrote it",
	}
	for _, text := range unchanged {
		if got := redact.New().Redact(text); got != text {
			t.Fatalf("Redact(%q) = %q, want it unchanged", text, got)
		}
	}
}

func TestTheHostAndPathSurviveSoADiagnosticStillSaysWhere(t *testing.T) {
	t.Parallel()
	got := redact.New().Redact("https://user:s3cr3t@example.com/owner/repo.git")
	for _, want := range []string{"example.com", "/owner/repo.git", "https://"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Redact left %q, which no longer says %q", got, want)
		}
	}
}

func TestEveryURLInOneStringIsRedacted(t *testing.T) {
	t.Parallel()
	got := redact.New().Redact("from https://a:one@x.invalid/r to https://b:two@y.invalid/r")
	for _, gone := range []string{"one", "two"} {
		if strings.Contains(got, ":"+gone+"@") {
			t.Fatalf("Redact left %q in %q", gone, got)
		}
	}
}

// internal/store refuses a redactor that leaves its probe URL's credential
// intact, and that refusal is the one thing a caller cannot see for itself. It
// is asserted here rather than only in that package because this is the
// implementation the product wires in.
func TestTheStoreAcceptsThisRedactor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := store.Open(t.Context(), dir+"/state.db", store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening a store with this redactor: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
}

// The bearer shape is the one a hosted service's API key travels in, and it
// has to be gone wherever it appears: as a header line, as a bare token, and
// inside an error message that quotes either.
func TestABearerCredentialDoesNotSurvive(t *testing.T) {
	t.Parallel()
	const token = "ts_live_0123456789abcdef"
	for _, text := range []string{
		"Authorization: Bearer " + token,
		"authorization: bearer " + token,
		"AUTHORIZATION:Bearer " + token + "\r\nContent-Type: application/json",
		"request failed with header Authorization=Bearer " + token + " attached",
		"Bearer " + token,
		`Post "https://api.example.invalid/v1": sent Bearer ` + token + `: connection reset`,
		"Authorization: Basic " + token,
		"Authorization: token " + token,
	} {
		got := redact.New().Redact(text)
		if strings.Contains(got, token) {
			t.Errorf("redacting %q left the token in %q", text, got)
		}
		if !strings.Contains(got, redact.Marker) {
			t.Errorf("redacting %q produced %q, which carries no marker", text, got)
		}
	}
}

// The header's name and the scheme stay, so a diagnostic still says what kind
// of credential stood there, and a following header line is untouched because
// the value ends at its line.
func TestABearerRedactionKeepsTheSchemeAndTheNextLine(t *testing.T) {
	t.Parallel()
	got := redact.New().Redact("Authorization: Bearer abcdefgh12345678\nContent-Type: application/json")
	if want := "Authorization: Bearer " + redact.Marker + "\nContent-Type: application/json"; got != want {
		t.Fatalf("Redact = %q, want %q", got, want)
	}
	if again := redact.New().Redact(got); again != got {
		t.Fatalf("redacting a redacted header changed it again: %q", again)
	}
}

// Prose that uses the word is not a credential, so it is left as written.
func TestTheWordBearerInProseIsLeftAlone(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"the bearer of this note",
		"Bearer tokens are sent in a header.",
	} {
		if got := redact.New().Redact(text); got != text {
			t.Fatalf("Redact(%q) = %q, want it unchanged", text, got)
		}
	}
}
