package forge_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
)

// TestOpenRefusesWhenTheProviderResolvesADifferentRepository is the positive
// control for the one guard in this package that stands between a run and an
// outward-facing act it cannot take back.
//
// A repository specifier and the repository a provider reaches through it are
// two different things. A renamed or transferred repository keeps answering
// under its old specifier, so a run addressing the old one reaches the new
// repository, and the pull request it opens exists on a host where people can
// see it. Nothing later in the run can undo that.
//
// So this points the adapter at owner/name and has the provider report that
// specifier as somewhere-else/name. The two assertions are separate on
// purpose: that the call refused, and that the provider was never asked to
// create anything. A guard that refused after the write would satisfy the
// first and fail the second, and it is the second that is the point.
func TestOpenRefusesWhenTheProviderResolvesADifferentRepository(t *testing.T) {
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
