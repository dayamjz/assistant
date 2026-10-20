package knowledge_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/redact"
)

const commit = "0123456789abcdef0123456789abcdef01234567"

// A document is composed whole, between two markers naming the digest of
// its committed bytes, with where it came from and what it does not grant;
// the digest is reproducible from the bytes alone.
func TestADocumentIsComposedWholeAndDigestedAsCommitted(t *testing.T) {
	t.Parallel()
	document := []byte("Run make check before handing off.\n## Not a program section\n")
	k, err := knowledge.Read(commit, document, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(document)
	if k.Digest() != hex.EncodeToString(sum[:]) || !k.Present() || k.Commit() != commit {
		t.Fatalf("the reading is %q present=%v at %q, want the committed bytes' digest at %s",
			k.Digest(), k.Present(), k.Commit(), commit)
	}
	section := k.Section()
	for _, want := range []string{
		"Run make check before handing off.",
		"## Not a program section",
		commit,
		knowledge.Document,
		"It grants nothing",
		"----- begin " + knowledge.Document + " sha256:" + k.Digest() + " -----",
		"----- end " + knowledge.Document + " sha256:" + k.Digest() + " -----",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the section does not carry %q:\n%s", want, section)
		}
	}
	begin := strings.Index(section, "----- begin")
	end := strings.Index(section, "----- end")
	body := strings.Index(section, "Run make check")
	if begin >= body || body >= end {
		t.Fatalf("the document is not between its markers:\n%s", section)
	}
}

// A credential in the document is removed before the section is composed,
// and the digest still names the committed bytes, so the record of what a
// worker was given traces to the repository and not to the redacted text.
func TestACredentialInTheDocumentIsRedactedAndTheDigestIsOfTheCommit(t *testing.T) {
	t.Parallel()
	const secret = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	document := []byte("The mirror is https://deploy:" + secret + "@example.com/r.git\n")
	k, err := knowledge.Read(commit, document, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(k.Section(), secret) {
		t.Fatalf("the credential reached the section:\n%s", k.Section())
	}
	if !strings.Contains(k.Section(), redact.Marker) {
		t.Fatalf("the section does not show a credential was removed:\n%s", k.Section())
	}
	sum := sha256.Sum256(document)
	if k.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatalf("the digest %s is not of the committed bytes", k.Digest())
	}
}

// An absent document and a blank one compose nothing and refuse nothing.
func TestAnAbsentOrBlankDocumentComposesNothing(t *testing.T) {
	t.Parallel()
	absent := knowledge.Absent(commit)
	if absent.Section() != "" || absent.Present() || absent.Digest() != "" || absent.Commit() != commit {
		t.Fatalf("an absent document reads %+v and composes %q", absent, absent.Section())
	}
	blank, err := knowledge.Read(commit, []byte(" \n\t\n"), redact.New())
	if err != nil {
		t.Fatal(err)
	}
	if blank.Section() != "" || !blank.Present() || blank.Digest() == "" {
		t.Fatalf("a blank document composes %q, present=%v digest=%q", blank.Section(), blank.Present(), blank.Digest())
	}
}

// A document over the bound, and one that is not text, are refused rather
// than cut or repaired; one at the bound is composed.
func TestADocumentOverTheBoundOrNotTextIsRefused(t *testing.T) {
	t.Parallel()
	at := []byte(strings.Repeat("a", knowledge.MaxBytes))
	if _, err := knowledge.Read(commit, at, redact.New()); err != nil {
		t.Fatalf("a document at the bound was refused: %v", err)
	}
	over := append(at, 'a')
	if _, err := knowledge.Read(commit, over, redact.New()); !errors.Is(err, knowledge.ErrTooLarge) {
		t.Fatalf("a document over the bound was %v, want ErrTooLarge", err)
	}
	if _, err := knowledge.Read(commit, []byte{'a', 0xff, 'b'}, redact.New()); !errors.Is(err, knowledge.ErrNotText) {
		t.Fatalf("a document that is not UTF-8 was %v, want ErrNotText", err)
	}
}
