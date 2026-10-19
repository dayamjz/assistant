//go:build darwin

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/managed"
)

// On macOS, assistant service start --install refuses a home serving a
// checkout inside the user's Documents folder, which macOS keeps from a
// program its service manager starts unless the user granted it access:
// the refusal is internal/machine's row naming both remedies and the
// checkout found, it is an operational failure, and nothing is written or
// asked of launchctl. Given --full-disk-access-granted, the operator's
// statement that the grant was made, the same install goes through, which is
// the control that it was the folder that refused it. That the grant itself
// reaches the process launchd starts is not something this test, or anything
// in this build, establishes; internal/managed's doc says so.
//
// It runs serially, unlike most tests here: it starts a service as a process
// of its own and waits on a bound for it to answer, which the load of the
// package's parallel tests could exhaust, and serial tests run before any
// parallel test resumes.
func TestAManagedInstallOnMacOSRefusesACheckoutInAProtectedFolder(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	w := newManagedWorld(t, h)
	m := w.manager(t, h)
	documents := filepath.Join(w.userHome, "Documents")
	if err := os.MkdirAll(documents, 0o755); err != nil {
		t.Fatal(err)
	}
	subject := filepath.Join(documents, "project")
	if err := os.Rename(newSubject(t), subject); err != nil {
		t.Fatalf("moving the checkout into Documents: %v", err)
	}
	if got := w.run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("init in Documents exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	row := machine.ManagedRefusalPhrase(managed.ReasonProtectedFolder).String()
	refused := w.run(t, h, "", "service", "start", "--install")
	if refused.code != machine.ExitFailure || !strings.Contains(refused.stderr, row) || !strings.Contains(refused.stderr, "project") {
		t.Fatalf("an install serving a checkout in Documents exited %s:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	if installed, _ := m.Installed(); installed {
		t.Fatal("a refused install wrote its definition")
	}
	if got := w.recorded(t); len(got) != 0 {
		t.Fatalf("a refused install asked launchctl %q", got)
	}

	granted := w.service(t, h, "start", "--install", "--full-disk-access-granted")
	if !granted.Running || granted.Managed == nil {
		t.Fatalf("the install with access granted answered %+v", granted)
	}
	if got := w.run(t, h, "", "service", "stop", "--uninstall"); got.code != machine.ExitOK {
		t.Fatalf("removing it exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
}
