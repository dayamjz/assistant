package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// An operator's file that sets a remote host, a placement limit or
// architecture, the remote disconnect grace, a housekeeping.interval that
// is not zero, or the herdr provider is refused
// where the service reads it, naming the key, because this build parses each
// and acts on none (P7: a configuration the service cannot honor stops it
// before it launches anything, rather than being run as something else). The
// herdr provider matters most: the service would otherwise start every
// worker on pipes. The same file without the key reads, so the refusal is
// about the key and not about the file.
func TestTheServiceRefusesARemoteSettingThisBuildDoesNotActOn(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	cases := []struct {
		key config.Key
		doc string
	}{
		{config.KeySessionProvider, "session_provider: herdr\n"},
		{config.KeyRemoteHosts, "session_provider: herdr\nremote_hosts:\n  - host: studio\n    root: /r\n    bridge: /b\n"},
		{config.KeyRemoteHosts, "remote_hosts: []\n"},
		{config.KeyPlacementLocal, "placement:\n  local:\n    max_workers: 4\n"},
		{config.KeyPlacementArch, "placement:\n  arch: [arm64]\n"},
		{config.KeyRemoteDisconnectGrace, "remote_disconnect_grace: 1h\n"},
		{config.KeyHousekeepingInterval, "housekeeping:\n  interval: 12h\n"},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("no_ci: true\n"+c.doc), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := service.GlobalConfig(path)
		if err == nil {
			t.Errorf("GlobalConfig accepted %q, which this build does not act on", c.doc)
			continue
		}
		if !strings.Contains(err.Error(), string(c.key)) || !strings.Contains(err.Error(), "this build") {
			t.Errorf("GlobalConfig(%q) = %v, want a refusal naming %s and saying this build does not act on it", c.doc, err, c.key)
		}
	}
	for _, doc := range []string{"no_ci: true\n", "session_provider: tmux\n", "session_provider: pipe\n", "housekeeping:\n  interval: 0s\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.GlobalConfig(path); err != nil {
			t.Errorf("GlobalConfig(%q) = %v, want it read", doc, err)
		}
	}
}

// A repository whose default branch sets placement.arch is refused wherever
// the service reads that trusted copy, naming the key, whatever architectures
// it names, including this machine's own: this build places every task on
// this machine and takes no reading of any machine's architecture, so the
// narrowing cannot be honored, and a task taken under it would run where it
// may have been told not to (P7; PRD section 10's placement.arch row). A
// delivery intake records nothing, and a run is ended before any stage body
// runs. The same repository with the key removed takes the task and walks the
// run, so the refusal is about the key and not about the repository.
func TestATrustedCopyNamingPlacementArchIsRefusedWhereverItIsRead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	elsewhere := "amd64"
	if runtime.GOARCH == elsewhere {
		elsewhere = "arm64"
	}
	for _, arch := range []string{elsewhere, runtime.GOARCH} {
		h := newHome(t)
		subject := newSubject(t)
		repository := recordRepository(t, h, subject)
		plantTrustedDocument(t, subject, `{"placement": {"arch": ["`+arch+`"]}}`)

		var bodies atomic.Int64
		opts := options(t, h)
		opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
			nine := stages.All(deps)
			nine.Intent = pipeline.Implementation{NewBody: func() pipeline.Body {
				return func(context.Context, pipeline.Input) (pipeline.Output, error) {
					bodies.Add(1)
					return pipeline.Output{Report: findings.Report{Summary: "counted"}}, nil
				}
			}}
			return nine
		}
		withServiceOptions(t, opts, func(running serviceUnderTest) {
			create := func(branch string) error {
				var created machine.TaskCreated
				return running.client.Call(t.Context(), ipc.MethodTaskCreate, machine.TaskCreateRequest{
					Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
					Repository: repository.ID, Branch: branch,
				}, &created)
			}
			before := taskCount(t, h)
			err := create("task/narrowed")
			if err == nil {
				t.Fatalf("placement.arch [%s]: a delivery task was taken under a narrowing this build cannot honor", arch)
			}
			if !strings.Contains(err.Error(), string(config.KeyPlacementArch)) || !strings.Contains(err.Error(), "this build") {
				t.Errorf("placement.arch [%s]: the intake refusal %q does not name the key and say this build does not act on it", arch, err)
			}
			if got := taskCount(t, h); got != before {
				t.Fatalf("placement.arch [%s]: the refused intake recorded %d tasks", arch, got-before)
			}

			_, err = startRunErr(t, running.client, subject)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), string(config.KeyPlacementArch)) {
				t.Fatalf("placement.arch [%s]: the run start = %v, want a refusal naming the key", arch, err)
			}
			if got := bodies.Load(); got != 0 {
				t.Fatalf("placement.arch [%s]: %d stage bodies ran", arch, got)
			}
			if refused := onlyRun(t, h); refused.Status != store.RunTerminated {
				t.Fatalf("placement.arch [%s]: the refused run is recorded as %s, want it ended", arch, refused.Status)
			}

			plantTrustedDocument(t, subject, `{"ignore_patterns": ["vendor/**"]}`)
			if err := create("task/unnarrowed"); err != nil {
				t.Fatalf("the intake after placement.arch was removed: %v", err)
			}
			if fresh := startRun(t, running.client, subject); fresh.Record.Status == store.RunPending ||
				fresh.Record.Status == store.RunTerminated {
				t.Fatalf("the run after placement.arch was removed is recorded as %s, want it walked", fresh.Record.Status)
			}
		})
	}
}
