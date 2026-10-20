package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// A refused pushed configuration is a typed refusal the caller must handle,
// not a fault, so it crosses the socket as refused and the structured answer's
// code says so. Both ways a pushed document is refused are driven here: one
// that does not parse, and one naming a key only the operator's own file may
// set; each answer says which copy and, for the key, which key.
//
// The control is a run whose trusted copy cannot be fetched at all: it is
// stopped before launch all the same, but nothing was read to be refused, so
// its code stays internal. Without it a classification that called every
// configuration failure refused would pass.
func TestARefusedPushedConfigurationCrossesAsRefused(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P7)

	cases := []struct {
		name     string
		document string
		says     []string
	}{
		{"unparseable", `{"ignore_patterns": ["vendor/**",]}`, []string{"pushed configuration"}},
		{"global-only key", `{"` + string(config.KeySessionReuse) + `": false}`,
			[]string{"pushed configuration", string(config.KeySessionReuse)}},
		{"stop grace", `{"` + string(config.KeyStopGrace) + `": "10m"}`,
			[]string{"pushed configuration", string(config.KeyStopGrace), "global-only"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			serve(t, h)
			if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
				t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
			}
			standingOnChange(t, subject, "a-change")
			if err := os.WriteFile(filepath.Join(subject, config.RepositoryDocument), []byte(c.document), 0o600); err != nil {
				t.Fatalf("writing the pushed document: %v", err)
			}
			git(t, subject, "add", "-A")
			git(t, subject, "commit", "--quiet", "-m", "carry a configuration document on the branch")

			failure := startFailure(t, h, subject)
			if failure.Code != string(ipc.CodeRefused) {
				t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeRefused, failure.Error)
			}
			for _, want := range c.says {
				if !strings.Contains(failure.Error, want) {
					t.Errorf("the refusal does not say %q: %s", want, failure.Error)
				}
			}
		})
	}

	t.Run("unfetchable trusted copy", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		serve(t, h)
		if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
			t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
		}
		standingOnChange(t, subject, "a-change")
		// The upstream init recorded goes away, so the trusted copy's fetch
		// fails and nothing is read that could be refused.
		if err := os.RemoveAll(git(t, subject, "remote", "get-url", "origin")); err != nil {
			t.Fatalf("removing the upstream: %v", err)
		}

		failure := startFailure(t, h, subject)
		if failure.Code != string(ipc.CodeInternal) {
			t.Fatalf("a failed fetch crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeInternal, failure.Error)
		}
		if !strings.Contains(failure.Error, "trusted configuration") || !strings.Contains(failure.Error, "fetching") {
			t.Errorf("the failure does not say the trusted copy could not be fetched: %s", failure.Error)
		}
	})
}

// startFailure starts a run from subject with --json and returns the failure
// it answered with, failing the test when the start did not fail
// operationally with one structured document.
func startFailure(t *testing.T, h *home.Home, subject string) machine.Failure {
	t.Helper()
	got := runArgs(t, subject, "--home", h.Root(), "--json", "--intent", "a change with its tradeoffs stated")
	if got.code != machine.ExitFailure {
		t.Fatalf("the start exited %s, want the operational failure:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the failure is not one document: %v\n%s", err, got.stdout)
	}
	return failure
}
