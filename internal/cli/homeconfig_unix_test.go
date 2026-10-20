//go:build linux || darwin

package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// badHomeConfig is a home configuration file the service refuses: a key
// misspelled, which is refused by name rather than ignored.
const badHomeConfig = "sesion_provider: tmux\n"

// writeHomeConfig puts text in h's configuration file.
func writeHomeConfig(t *testing.T, h *home.Home, text string) {
	t.Helper()
	if err := os.WriteFile(h.ConfigFile(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// instanceOf asks the service of h which process is answering, over the
// protocol, and returns "" when nothing answers.
func instanceOf(t *testing.T, h *home.Home) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, h.Socket(), ipc.ClientConfig{})
	if err != nil {
		return ""
	}
	defer func() { _ = client.Close() }()
	var health machine.Health
	if err := client.Call(ctx, ipc.MethodHealth, nil, &health); err != nil {
		return ""
	}
	return health.Instance
}

// A start on a home configuration file the service would refuse launches
// nothing and says why: internal/machine's row, then the file's refusal,
// which names the file and the key. The same home with the file corrected
// starts, so the refusal is the file's and not the start's.
//
// It runs serially, unlike most tests here: it starts a service as a process
// of its own and waits on a bound for it to answer, which the load of the
// package's parallel tests could exhaust, and serial tests run before any
// parallel test resumes.
func TestAStartOnABadHomeConfigurationLaunchesNothingAndSaysWhy(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P7)
	h := newHome(t)
	writeHomeConfig(t, h, badHomeConfig)

	got := run(t, h, "", "service", "start")
	if got.code != machine.ExitFailure {
		t.Fatalf("a start on a bad configuration exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	for _, want := range []string{machine.PhraseStartConfigurationRefused.String(), h.ConfigFile(), "sesion_provider", "unrecognized key"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, got.stderr)
		}
	}
	if instance := instanceOf(t, h); instance != "" {
		t.Fatalf("a refused start left a service answering, %s", instance)
	}
	if _, err := os.Stat(h.ServiceLog()); !os.IsNotExist(err) {
		t.Errorf("a refused start left a service log, so something was launched: %v", err)
	}

	writeHomeConfig(t, h, "supervision_beacon_interval: 1s\n")
	if got := run(t, h, "", "service", "start"); got.code != machine.ExitOK {
		t.Fatalf("a start on the corrected configuration exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if instanceOf(t, h) == "" {
		t.Fatal("the start on the corrected configuration left nothing answering")
	}
}

// A restart on a home configuration file a replacement would refuse is
// refused, and the service asked to restart goes on answering as the same
// process: the home is not left with no service. The refusal is the
// restart row and the file's refusal, with a failure exit. Corrected, the
// same restart goes through to a different process, which is the control
// showing the refusal is the file's.
//
// It runs serially, unlike most tests here: it starts a service as a process
// of its own and waits on a bound for it to answer, which the load of the
// package's parallel tests could exhaust, and serial tests run before any
// parallel test resumes.
func TestARestartOnABadHomeConfigurationLeavesTheServiceUp(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if got := run(t, h, "", "service", "start"); got.code != machine.ExitOK {
		t.Fatalf("starting the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	before := instanceOf(t, h)
	if before == "" {
		t.Fatal("the started service does not answer")
	}

	writeHomeConfig(t, h, badHomeConfig)
	got := run(t, h, "", "service", "restart")
	if got.code != machine.ExitFailure {
		t.Fatalf("a restart on a bad configuration exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	row := machine.LifecyclePhrase(machine.Lifecycle{ConfigurationRefused: true}).String()
	if row == machine.LifecyclePhrase(machine.Lifecycle{}).String() {
		t.Fatal("a restart refused on the configuration reads as one refused because runs are under way")
	}
	for _, want := range []string{row, h.ConfigFile(), "sesion_provider"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the refusal does not say %q:\n%s%s", want, got.stdout, got.stderr)
		}
	}
	structured := run(t, h, "", "--json", "service", "restart")
	var answer machine.Lifecycle
	if err := json.Unmarshal([]byte(structured.stdout), &answer); err != nil {
		t.Fatalf("the structured refusal does not decode: %v\n%s", err, structured.stdout)
	}
	if answer.Accepted || !answer.ConfigurationRefused || !strings.Contains(answer.Detail, "sesion_provider") {
		t.Errorf("the structured refusal is %+v, want a configuration refusal naming the key", answer)
	}
	if after := instanceOf(t, h); after != before {
		t.Fatalf("after a refused restart the answering service is %q, want the one asked, %q", after, before)
	}

	writeHomeConfig(t, h, "supervision_beacon_interval: 1s\n")
	if got := run(t, h, "", "service", "restart"); got.code != machine.ExitOK {
		t.Fatalf("a restart on the corrected configuration exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if after := instanceOf(t, h); after == "" || after == before {
		t.Fatalf("after the corrected restart the answering service is %q, want one other than %q", after, before)
	}
	if got := run(t, h, "", "service", "stop"); got.code != machine.ExitOK {
		t.Fatalf("stopping the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
}

// A service that holds the home and cannot start writes why to the service
// log before it exits, so the log an operator is pointed at holds the cause.
//
// It runs serially, unlike most tests here: it starts a service as a process
// of its own and waits on a bound for it to answer, which the load of the
// package's parallel tests could exhaust, and serial tests run before any
// parallel test resumes.
func TestAServiceThatCannotStartWritesWhyToItsLog(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	writeHomeConfig(t, h, badHomeConfig)
	got := run(t, h, "", "service", "start", "--foreground")
	if got.code != machine.ExitFailure || !strings.Contains(got.stderr, "sesion_provider") {
		t.Fatalf("serving on a bad configuration exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	logged, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	if !strings.Contains(string(logged), "could not start") || !strings.Contains(string(logged), "sesion_provider") {
		t.Fatalf("the service log does not hold why the service could not start:\n%s", logged)
	}
}

// A launched service that exits before it answers is reported as having
// exited, pointing at the log, rather than as a readiness wait that ran out,
// and the log holds the cause. Here the home's database is a directory, which
// the start's own read of the configuration cannot foresee.
//
// It runs serially, unlike most tests here: it starts a service as a process
// of its own and waits on a bound for it to answer, which the load of the
// package's parallel tests could exhaust, and serial tests run before any
// parallel test resumes.
func TestAStartWhoseServiceExitsSaysSoAndPointsAtTheLog(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if err := os.MkdirAll(h.Database(), 0o700); err != nil {
		t.Fatal(err)
	}
	got := run(t, h, "", "service", "start")
	if got.code != machine.ExitFailure {
		t.Fatalf("a start whose service cannot open its database exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "exited before it answered") || !strings.Contains(got.stderr, h.ServiceLog()) {
		t.Fatalf("the failure does not say the service exited and where its log is:\n%s", got.stderr)
	}
	logged, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	if !strings.Contains(string(logged), "could not start") {
		t.Fatalf("the service log does not hold why the service could not start:\n%s", logged)
	}
}
