package forge_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestOpenRefusesWhenTheProviderResolvesADifferentRepository is the positive
// control for the one guard in this package that stands between a run and an
// outward-facing act it cannot take back.
//
// A repository specifier and the repository a provider reaches through it are
// two different things, and nothing in this package can see the second. A pull
// request opened against the wrong one exists on a host where people can see
// it, and nothing later in the run can undo that. PRD principle P1 makes the
// push to the gate the consent boundary for the pull request that run opens;
// one opened somewhere the run's record does not name has no consent behind
// it at all.
//
// So this points the adapter at owner/name and has the provider report that
// specifier as somewhere-else/name. The two assertions are separate on
// purpose: that the call refused, and that the provider was never asked to
// create anything. A guard that refused after the write would satisfy the
// first and fail the second, and it is the second that is the point.
func TestOpenRefusesWhenTheProviderResolvesADifferentRepository(t *testing.T) {
	principles.Cite(t, principles.P1)

	h := newWriteHarness(t, ghScript{
		"repo":   {{Stdout: repoViewJSON("somewhere-else/name")}},
		"create": {{Stdout: createdURL}},
		"list":   {{Stdout: oneListedJSON}},
		"view":   {{Stdout: openPullRequestJSON}},
	})

	_, err := h.gh.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change", Body: "a body",
	})

	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Open returned %v, want a *Refusal naming the wrong repository", err)
	}
	if refusal.Reason != forge.ReasonWrongRepository {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonWrongRepository)
	}
	if !strings.Contains(refusal.Detail, "somewhere-else/name") {
		t.Errorf("the refusal does not say where the provider resolved the specifier to: %q", refusal.Detail)
	}
	if n := len(h.callsFor("create")); n != 0 {
		t.Fatalf("the provider was asked to create a pull request %d times in a repository the run did not name", n)
	}
}

// TestTheSameGuardLetsTheRightRepositoryThrough is what keeps the test above
// from passing over a guard that refuses everything.
//
// It is the same call against the same harness with one thing changed: the
// provider reports the specifier the adapter was given. A confirmation that
// always refused, or one whose comparison never matched, would fail here while
// the refusal test still passed, which is the shape of a guard nobody has
// watched succeed.
func TestTheSameGuardLetsTheRightRepositoryThrough(t *testing.T) {
	h := newWriteHarness(t, ghScript{
		"create": {{Stdout: createdURL}},
		"list":   {{Stdout: oneListedJSON}},
		"view":   {{Stdout: openPullRequestJSON}},
	})

	pr, err := h.gh.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change", Body: "a body",
	})
	if err != nil {
		t.Fatalf("Open against the repository the provider confirms: %v", err)
	}
	if pr.Number != 12 {
		t.Errorf("Open returned pull request %d, want 12", pr.Number)
	}
	if n := len(h.callsFor("create")); n != 1 {
		t.Fatalf("the provider was asked to create a pull request %d times, want 1", n)
	}
}

// TestTheConfirmationIsAskedOnceAndNotRepeated checks the cost of the guard is
// one invocation for an adapter that confirms, not one per write.
func TestTheConfirmationIsAskedOnceAndNotRepeated(t *testing.T) {
	h := newWriteHarness(t, ghScript{
		"list":   {{Stdout: noneListedJSON}, {Stdout: oneListedJSON}},
		"create": {{Stdout: createdURL}},
		"view":   {{Stdout: openPullRequestJSON}},
		"edit":   {{}},
	})
	spec := forge.OpenSpec{Head: "fm/work", Base: "main", Title: "feat: a change", Body: "first"}
	if _, err := forge.Submit(context.Background(), h.gh, spec); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	spec.Body = "second"
	if _, err := forge.Submit(context.Background(), h.gh, spec); err != nil {
		t.Fatalf("Submit again: %v", err)
	}
	if n := len(h.callsFor("repo")); n != 1 {
		t.Errorf("the adapter confirmed its repository %d times across two writes, want 1", n)
	}
}

// TestAWriteRefusesFromAnAdapterThatNamesNoRepository holds the other half of
// the write rule.
//
// NewGitHub accepts an adapter given only a working directory, which is enough
// to read with. It is not enough to write with: there is no specifier for the
// confirmation to compare the provider's answer against, so the write would go
// wherever the directory resolved to. The refusal happens before the provider
// is invoked at all.
func TestAWriteRefusesFromAnAdapterThatNamesNoRepository(t *testing.T) {
	h := newHarness(t, ghScript{
		"create": {{Stdout: createdURL}},
		"edit":   {{}},
	})
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"opening a pull request", func() error {
			_, err := h.gh.Open(context.Background(), forge.OpenSpec{
				Head: "fm/work", Base: "main", Title: "feat: a change",
			})
			return err
		}},
		{"replacing a body", func() error {
			_, err := h.gh.UpdateBody(context.Background(), 12, "a body")
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.Is(err, forge.ErrInvalidArgument) {
				t.Fatalf("the call returned %v, want ErrInvalidArgument", err)
			}
		})
	}
	if n := len(h.calls()); n != 0 {
		t.Errorf("the provider was invoked %d times for a write that names no repository", n)
	}
}

// TestAHostOpensTheRepositoryItIsGiven is the run-scoped half of the seam: a
// Host holds everything settled once and the repository arrives per call, so
// two runs of one service address two repositories.
func TestAHostOpensTheRepositoryItIsGiven(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, ghScript{"checks": {{Stdout: rollup()}}})
	host := forge.NewGitHubHost(testRedactor,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{fakeGHDir + "=" + dir}),
	)

	for _, repository := range []string{"owner/first", "owner/second"} {
		provider, err := host.Open(repository)
		if err != nil {
			t.Fatalf("Open(%q): %v", repository, err)
		}
		if _, err := provider.Checks(context.Background(), 12); err != nil {
			t.Fatalf("Checks against %q: %v", repository, err)
		}
	}

	calls := readCalls(dir)
	if len(calls) != 2 {
		t.Fatalf("the stand-in recorded %d calls, want 2", len(calls))
	}
	for i, want := range []string{"--repo=owner/first", "--repo=owner/second"} {
		if !calls[i].hasArg(want) {
			t.Errorf("call %d does not carry %s: %v", i, want, calls[i].Args)
		}
	}
}

// TestAHostRefusesARepositoryItCannotAddress covers the specifiers a Host will
// not open a provider on. An empty one is the case a run reaches when its
// record names no repository on this host, and it matters most: a provider
// opened on it would resolve a repository of its own.
func TestAHostRefusesARepositoryItCannotAddress(t *testing.T) {
	host := forge.NewGitHubHost(testRedactor)
	for _, spec := range []string{"", "owner", "owner/name/extra", "https://github.com/owner/name", secretURL} {
		t.Run(spec, func(t *testing.T) {
			provider, err := host.Open(spec)
			if !errors.Is(err, forge.ErrInvalidArgument) {
				t.Fatalf("Open(%q) returned %v, want ErrInvalidArgument", spec, err)
			}
			if provider != nil {
				t.Errorf("Open(%q) returned a provider alongside its refusal", spec)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the refusal carries the credential: %v", err)
			}
		})
	}
}

// TestAHostCannotBeFixedToOneRepository is the lifetime rule as a test. A Host
// is built once per service and opened per run, so an option naming a
// repository must not survive into the adapter: one that did would send every
// run's pull request to the same repository.
func TestAHostCannotBeFixedToOneRepository(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, ghScript{"checks": {{Stdout: rollup()}}})
	host := forge.NewGitHubHost(testRedactor,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{fakeGHDir + "=" + dir}),
		forge.WithRepository("owner/fixed"),
	)
	provider, err := host.Open("owner/asked")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := provider.Checks(context.Background(), 12); err != nil {
		t.Fatalf("Checks: %v", err)
	}
	calls := readCalls(dir)
	if len(calls) != 1 {
		t.Fatalf("the stand-in recorded %d calls, want 1", len(calls))
	}
	if !calls[0].hasArg("--repo=owner/asked") {
		t.Errorf("the host opened a repository other than the one it was asked for: %v", calls[0].Args)
	}
}

// TestGitHubRepositoryReadsAGitRemote holds the derivation a run's record goes
// through on its way to a specifier.
//
// The refusals matter more than the acceptances. A remote on another host that
// yielded owner/name would send a pull request to the github.com repository
// that happens to share the name, because the specifier this package puts on a
// command line names no host.
func TestGitHubRepositoryReadsAGitRemote(t *testing.T) {
	cases := []struct {
		remote string
		want   string
	}{
		{"https://github.com/dayamjz/assistant.git", "dayamjz/assistant"},
		{"https://github.com/dayamjz/assistant", "dayamjz/assistant"},
		{"https://github.com/dayamjz/assistant/", "dayamjz/assistant"},
		{"HTTPS://GitHub.com/dayamjz/assistant.git", "dayamjz/assistant"},
		// internal/store holds a remote after internal/redact has run over it,
		// so this is the shape the run's record actually carries.
		{"https://redacted@github.com/dayamjz/assistant.git", "dayamjz/assistant"},
		{"https://x-access-token:tok@github.com/dayamjz/assistant.git", "dayamjz/assistant"},
		{"ssh://git@github.com/dayamjz/assistant.git", "dayamjz/assistant"},
		{"ssh://git@github.com:22/dayamjz/assistant.git", "dayamjz/assistant"},
		{"git@github.com:dayamjz/assistant.git", "dayamjz/assistant"},
		{"  git@github.com:dayamjz/assistant.git  ", "dayamjz/assistant"},

		{"", ""},
		{"https://gitlab.com/dayamjz/assistant.git", ""},
		{"git@gitlab.com:dayamjz/assistant.git", ""},
		{"https://github.example.com/dayamjz/assistant.git", ""},
		{"https://notgithub.com/dayamjz/assistant.git", ""},
		{"https://github.com/dayamjz", ""},
		{"https://github.com/dayamjz/assistant/pull/1", ""},
		{"https://github.com/", ""},
		{"/srv/repos/assistant.git", ""},
		{"C:\\repos\\assistant", ""},
		{"file:///srv/repos/assistant.git", ""},
	}
	for _, c := range cases {
		t.Run(c.remote, func(t *testing.T) {
			got, ok := forge.GitHubRepository(c.remote)
			if got != c.want || ok != (c.want != "") {
				t.Errorf("GitHubRepository(%q) = %q, %v; want %q, %v",
					c.remote, got, ok, c.want, c.want != "")
			}
		})
	}
}

// TestEveryInvocationNamesTheHostItAddresses checks the one entry in the
// invocation environment that decides where a write lands.
//
// A specifier is owner/name and names no host, so without this the host a
// provider is pointed at would be whatever the invoking environment last set,
// which is an ambient value nothing in a run chose. The stand-in records the
// environment it was given, so what this checks is what the child is handed
// rather than a claim about what the provider does with it.
//
// The base environment it starts from names another host on purpose, so the
// assertion is that the entry was replaced and not merely that it is present.
func TestEveryInvocationNamesTheHostItAddresses(t *testing.T) {
	h := newHarnessWithEnv(t, ghScript{"checks": {{Stdout: rollup()}}},
		"GH_HOST=an-operators-own-installation.example")
	if _, err := h.gh.Checks(context.Background(), 12); err != nil {
		t.Fatalf("Checks: %v", err)
	}
	calls := h.calls()
	if len(calls) != 1 {
		t.Fatalf("the stand-in recorded %d calls, want 1", len(calls))
	}
	want := "GH_HOST=" + forge.GitHubHostname
	var found, kept bool
	for _, entry := range calls[0].Env {
		switch {
		case entry == want:
			found = true
		case strings.HasPrefix(entry, "GH_HOST=") && entry != want:
			kept = true
		}
	}
	if !found {
		t.Errorf("the invocation was not given %s: %v", want, calls[0].Env)
	}
	if kept {
		t.Errorf("the invocation kept the environment's own host: %v", calls[0].Env)
	}
}

// TestAPullRequestBodyReachesTheProviderRedacted is the positive control for
// outbound redaction.
//
// A pull request body is generated from stage reports - fix summaries, command
// output, whatever a stage had to say - and it lands on an external host. A
// credential that reaches it has been published, and publishing is not an act
// with an undo. Inbound provider text was redacted here from the start and
// outbound text was not, which is the asymmetry this closes.
//
// It plants a credentialed URL in a body, drives the two writes that carry
// one, and reads what the stand-in provider actually received. The assertions
// are on the bytes on the wire rather than on the adapter having called a
// redactor, because what matters is what left the process.
func TestAPullRequestBodyReachesTheProviderRedacted(t *testing.T) {
	principles.Cite(t, principles.P1)

	const body = "the rebase stage re-pointed the remote to " + secretURL + " and retried"
	h := newWriteHarness(t, ghScript{
		"list":   {{Stdout: oneListedJSON}},
		"create": {{Stdout: createdURL}},
		"view":   {{Stdout: openPullRequestJSON}},
		"edit":   {{}},
	})

	if _, err := h.gh.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change", Body: body,
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := h.gh.UpdateBody(context.Background(), 12, body); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}

	for _, op := range []string{"create", "edit"} {
		calls := h.callsFor(op)
		if len(calls) != 1 {
			t.Fatalf("the adapter made %d %s calls, want 1", len(calls), op)
		}
		sent := calls[0].Stdin
		if strings.Contains(sent, secret) {
			t.Errorf("the %s call published the credential: %q", op, sent)
		}
		// The wire carries the supplied Redactor's output, which is the whole
		// contract: this adapter writes no redaction of its own, so what it
		// sends is what the Redactor it was constructed with produced.
		//
		// The assertion is not vacuous, because the body planted above is one
		// that Redactor changes: an adapter that sent the body through
		// untouched would fail this comparison rather than satisfy it.
		if want := testRedactor.Redact(body); sent != want {
			t.Errorf("the %s call sent %q, want the redactor's output %q", op, sent, want)
		}
		// The body is redacted and not discarded: what a reviewer is meant to
		// read still arrives, which is the half a redactor that emptied its
		// input would fail.
		if !strings.Contains(sent, "the rebase stage re-pointed the remote to") ||
			!strings.Contains(sent, "and retried") {
			t.Errorf("the %s call did not carry the body a reviewer reads: %q", op, sent)
		}
	}
}

// TestATitleReachesTheProviderRedacted is the same rule for the other outbound
// surface. A title travels on the argument vector rather than on standard
// input, so a redaction applied to bodies alone would miss it.
func TestATitleReachesTheProviderRedacted(t *testing.T) {
	const title = "fix: stop cloning " + secretURL
	h := newWriteHarness(t, ghScript{
		"list":   {{Stdout: oneListedJSON}},
		"create": {{Stdout: createdURL}},
		"view":   {{Stdout: openPullRequestJSON}},
	})

	if _, err := h.gh.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: title, Body: "a body",
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	calls := h.callsFor("create")
	if len(calls) != 1 {
		t.Fatalf("the adapter made %d create calls, want 1", len(calls))
	}
	joined := strings.Join(calls[0].Args, " ")
	if strings.Contains(joined, secret) {
		t.Errorf("the create call published the credential on its argument vector: %v", calls[0].Args)
	}
	if want := "--title=" + testRedactor.Redact(title); !slices.Contains(calls[0].Args, want) {
		t.Errorf("the create call did not carry the redactor's output for the title.\n"+
			" got: %v\nwant an argument: %q", calls[0].Args, want)
	}
	if !strings.Contains(joined, "fix: stop cloning") {
		t.Errorf("the create call did not carry the title: %v", calls[0].Args)
	}
}

// TestTheStandInRefusesOutboundTextItWasNotSupposedToSee is the positive
// control for the stand-in's own tripwire.
//
// That tripwire is what makes every test in this package a check on outbound
// redaction rather than only the two above. A guard nobody has watched refuse
// is what this repository keeps shipping, so this hands the predicate text on
// both sides and confirms it recognizes each, and then hands it what the
// adapter's redactor produces and confirms it does not refuse that.
func TestTheStandInRefusesOutboundTextItWasNotSupposedToSee(t *testing.T) {
	if where, carried := carriesCredential([]string{"--title=" + secretURL}, ""); !carried || where != "argument" {
		t.Errorf("a credentialed argument was not recognized: where=%q carried=%v", where, carried)
	}
	if where, carried := carriesCredential(nil, "cloned "+secretURL); !carried || where != "body" {
		t.Errorf("a credentialed body was not recognized: where=%q carried=%v", where, carried)
	}
	// What the adapter's redactor produces must pass, or every write would
	// fail the tripwire and it would be a guard that refuses everything.
	if _, carried := carriesCredential([]string{"--title=" + testRedactor.Redact(secretURL)},
		testRedactor.Redact("cloned "+secretURL)); carried {
		t.Error("redacted text was reported as carrying a credential, so the tripwire refuses everything")
	}
}

// TestTheStandInTripwireFiresOnARealWrite is the other half: the predicate
// above recognizing text is one thing, and the stand-in acting on it during an
// actual invocation is another.
//
// It drives a write whose body reaches the provider unredacted, by handing the
// adapter a Redactor that removes nothing. That is a shape a caller can build -
// NewGitHub takes whatever Redactor it is given - so this is the failure a
// misconfigured caller produces, and the stand-in refuses the invocation
// rather than recording it.
func TestTheStandInTripwireFiresOnARealWrite(t *testing.T) {
	inert := vcs.RedactorFunc(func(s string) string { return s })
	dir := t.TempDir()
	writeScript(t, dir, ghScript{
		"repo":   {{Stdout: repoViewJSON(harnessRepository)}},
		"create": {{Stdout: createdURL}},
		"list":   {{Stdout: oneListedJSON}},
		"view":   {{Stdout: openPullRequestJSON}},
	})
	gh, err := forge.NewGitHub(inert,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{fakeGHDir + "=" + dir}),
		forge.WithRepository(harnessRepository),
	)
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}

	_, err = gh.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change", Body: "cloned " + secretURL,
	})
	if err == nil {
		t.Fatal("the stand-in accepted a body carrying a credential, so the tripwire protects nothing")
	}
	if !strings.Contains(err.Error(), "carries a credential") {
		t.Fatalf("the write failed for some other reason: %v", err)
	}
}
