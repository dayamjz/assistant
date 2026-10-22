package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
)

// An operator's file that sets a remote host, a placement limit or
// architecture, the remote disconnect grace, or the herdr provider is refused
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
	for _, doc := range []string{"no_ci: true\n", "session_provider: tmux\n", "session_provider: pipe\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.GlobalConfig(path); err != nil {
			t.Errorf("GlobalConfig(%q) = %v, want it read", doc, err)
		}
	}
}
