package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// The upstream and the fork the fork tests name. Both are on github.com, the
// host the GitHub adapter addresses, so init reads a repository out of each.
const (
	forkTestUpstream = "https://github.com/upstream/assistant.git"
	forkTestFork     = "https://github.com/me/assistant.git"
)

// init --fork-url records the fork beside the upstream, as two fields of the
// repository record, and says so in a phrased row; the fork's credential is
// removed on the way in like the upstream's. Running init again without the
// flag records no fork, and the row is gone, which is the control that the
// row reads the record rather than the flag.
func TestInitRecordsAForkBesideItsUpstream(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	vocabtest.Assert(t, machine.PhraseInitFork.String())
	h := newHome(t)
	subject := newSubject(t)
	credentialed := "https://me:s3cret@github.com/me/assistant.git"

	answer := recordedDefaultBranch(t, h, subject, "--upstream", forkTestUpstream, "--fork-url", credentialed)
	fork, named := answer.Repository.ForkURL.Get()
	if !named {
		t.Fatalf("init given --fork-url recorded no fork: %+v", answer.Repository)
	}
	if strings.Contains(fork, "s3cret") || !strings.HasSuffix(fork, "@github.com/me/assistant.git") {
		t.Fatalf("init recorded the fork as %q, want it with its credential removed", fork)
	}
	if answer.Repository.UpstreamURL != forkTestUpstream {
		t.Fatalf("init recorded the upstream %q beside the fork, want %q", answer.Repository.UpstreamURL, forkTestUpstream)
	}
	records := openStore(t, h)
	stored, err := records.RepositoryAt(t.Context(), answer.Repository.WorkingPath)
	_ = records.Close()
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	if got, _ := stored.ForkURL.Get(); got != fork || stored.PushURL() != fork {
		t.Fatalf("the stored record names fork %q and pushes to %q, want %q for both", got, stored.PushURL(), fork)
	}

	readOut := run(t, h, subject, "init", "--upstream", forkTestUpstream, "--fork-url", forkTestFork)
	if readOut.code != machine.ExitOK || !strings.Contains(readOut.stdout, machine.PhraseInitFork.String()) {
		t.Fatalf("init with a fork exited %s without the fork's row:\n%s%s", readOut.code, readOut.stdout, readOut.stderr)
	}
	vocabtest.Assert(t, readOut.stdout)

	again := run(t, h, subject, "init", "--upstream", forkTestUpstream)
	if again.code != machine.ExitOK || strings.Contains(again.stdout, machine.PhraseInitFork.String()) {
		t.Fatalf("init without --fork-url exited %s and still reads the fork's row:\n%s%s", again.code, again.stdout, again.stderr)
	}
	if plain := recordedDefaultBranch(t, h, subject, "--upstream", forkTestUpstream); plain.Repository.ForkURL.IsKnown() {
		t.Fatalf("init without --fork-url left the fork recorded: %+v", plain.Repository)
	}
}

// init refuses a fork a run could neither send its branch to and propose
// from: one on a different host than the upstream, one given beside an
// upstream this build does not address, and one naming the upstream itself
// in another spelling. Each refusal is incorrect usage, because the remedy is a flag the
// caller supplies, is internal/machine's row for its reason in both
// renderings, and sets up nothing: no gate, no record. The control is the
// same pair given correctly in the test above.
func TestInitRefusesAForkItCannotProposeFrom(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	cases := []struct {
		name   string
		reason machine.ForkRefusal
		args   []string
	}{
		{"a fork off github.com", machine.ForkUnaddressable,
			[]string{"--upstream", forkTestUpstream, "--fork-url", "https://gitlab.com/me/assistant.git"}},
		{"an upstream off github.com", machine.ForkUpstreamUnaddressable,
			[]string{"--fork-url", forkTestFork}},
		{"the upstream again", machine.ForkIsUpstream,
			[]string{"--upstream", forkTestUpstream, "--fork-url", "git@github.com:Upstream/assistant"}},
	}
	for _, c := range cases {
		row := machine.ForkRefusalPhrase(c.reason).String()
		vocabtest.Assert(t, row)

		plain := run(t, h, subject, append([]string{"init"}, c.args...)...)
		if plain.code != machine.ExitUsage || !strings.Contains(plain.stderr, row) {
			t.Fatalf("%s: init exited %s, want %s carrying %q:\n%s%s", c.name, plain.code, machine.ExitUsage, row, plain.stdout, plain.stderr)
		}
		structured := run(t, h, subject, append([]string{"--json", "init"}, c.args...)...)
		var failure machine.Failure
		if err := json.Unmarshal([]byte(structured.stdout), &failure); err != nil || structured.code != machine.ExitUsage || failure.Error != row {
			t.Fatalf("%s: the structured refusal exited %s as %q (%v), want the row", c.name, structured.code, structured.stdout, err)
		}
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if binding, err := records.GateBinding(t.Context(), subject); err == nil {
		t.Fatalf("a refused init bound the working copy to gate %s", binding.GateID)
	}
	if all, err := records.Repositories(t.Context()); err != nil || len(all) != 0 {
		t.Fatalf("a refused init recorded %+v (%v)", all, err)
	}
}
