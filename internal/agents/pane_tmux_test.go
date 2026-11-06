//go:build unix

package agents_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// catPanes is a tmux server of a test's own whose panes each run cat
// appending what is typed into it to a file of its own, so what reached a
// pane's input is read back rather than stated. cat reads its terminal a
// line at a time, so a line is in the file once Enter reached the pane.
type catPanes struct {
	binary, socket string
	ids            []string
	files          []string
}

// startCatPanes starts a server with n such panes, each in a window of its
// own, and ends the server when the test ends.
func startCatPanes(t *testing.T, n int) catPanes {
	t.Helper()
	dir := standin.SocketDir(t)
	c := catPanes{binary: standin.Tmux(t), socket: filepath.Join(dir, "s")}
	for i := range n {
		c.files = append(c.files, filepath.Join(dir, fmt.Sprintf("typed%d", i)))
	}
	c.tmux(t, "new-session", "-d", "-x", "120", "-y", "30", "--", "/bin/sh", "-c", "exec cat >> "+c.files[0])
	t.Cleanup(func() { _ = exec.Command(c.binary, "-S", c.socket, "kill-server").Run() })
	c.ids = append(c.ids, c.tmux(t, "display-message", "-p", "#{pane_id}"))
	for i := 1; i < n; i++ {
		c.ids = append(c.ids, c.tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "--", "/bin/sh", "-c", "exec cat >> "+c.files[i]))
	}
	return c
}

// tmux runs one tmux client on the server and returns its output.
func (c catPanes) tmux(t *testing.T, args ...string) string {
	t.Helper()
	raw, err := exec.Command(c.binary, append([]string{"-S", c.socket}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %q: %v: %s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}

// pane names pane i for this package.
func (c catPanes) pane(i int) agents.Pane {
	return agents.Pane{Tmux: c.binary, Socket: c.socket, ID: c.ids[i]}
}

// awaitTyped is everything pane i's file holds once it holds a whole line,
// or what it holds after ten seconds of not.
func (c catPanes) awaitTyped(t *testing.T, i int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, _ := os.ReadFile(c.files[i])
		if strings.Contains(string(raw), "\n") || !time.Now().Before(deadline) {
			return string(raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Concurrent notices into panes of one tmux server each reach their own
// pane exactly once, and leave no buffer of theirs on the server. tmux's
// named buffers are the server's, so two homes whose coordinators share a
// server type through one buffer store; a name shared between calls would
// let one call paste the other's text and the other find no buffer at all.
//
// Each pane runs cat appending what is typed into it to a file of its own,
// so what reached each host's input is read back rather than stated. The
// calls start together, several at once, so their loads and pastes
// interleave on the server.
func TestConcurrentNoticesIntoOneServerEachReachTheirOwnPaneOnce(t *testing.T) {
	t.Parallel()
	const panes = 6
	server := startCatPanes(t, panes)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, panes)
	for i := range panes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = agents.TypeIntoPane(t.Context(), server.pane(i), fmt.Sprintf("A notice for pane %d.", i))
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("the notice into pane %d failed: %v", i, err)
		}
	}

	for i := range panes {
		want := fmt.Sprintf("A notice for pane %d.", i)
		if typed := server.awaitTyped(t, i); typed != want+"\n" {
			t.Errorf("pane %d was typed %q, want %q once", i, typed, want)
		}
	}
	if buffers := server.tmux(t, "list-buffers", "-F", "#{buffer_name}"); strings.Contains(buffers, "assistant-wake") {
		t.Errorf("the server still holds notice buffers: %q", buffers)
	}
}

// A notice whose paste fails leaves no buffer of its own on the server: the
// load runs, the paste names a pane the server does not have, and the call
// deletes the buffer it loaded before it answers with the failure.
func TestANoticeWhosePasteFailsLeavesNoBufferBehind(t *testing.T) {
	t.Parallel()
	tmux := standin.Tmux(t)
	socket := filepath.Join(standin.SocketDir(t), "s")
	if raw, err := exec.Command(tmux, "-S", socket, "new-session", "-d", "--", "/bin/sh", "-c", "exec cat > /dev/null").CombinedOutput(); err != nil {
		t.Fatalf("starting the server: %v: %s", err, raw)
	}
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	err := agents.TypeIntoPane(t.Context(), agents.Pane{Tmux: tmux, Socket: socket, ID: "%99"}, "A notice for no pane.")
	if err == nil {
		t.Fatal("a notice into a pane the server does not have was answered as typed")
	}
	raw, lerr := exec.Command(tmux, "-S", socket, "list-buffers", "-F", "#{buffer_name}").CombinedOutput()
	if lerr != nil {
		t.Fatalf("listing the server's buffers: %v: %s", lerr, raw)
	}
	if strings.Contains(string(raw), "assistant-wake") {
		t.Fatalf("the failed notice left its buffer on the server: %q (it failed with %v)", raw, err)
	}
}

// refusedFor asserts that err is a PaneRefusal for the reason want names,
// and for no other.
func refusedFor(t *testing.T, err error, want agents.PaneRefusal) {
	t.Helper()
	var refused *agents.PaneRefusal
	if !errors.As(err, &refused) || *refused != want {
		t.Fatalf("the notice answered %v, want the refusal %+v with nothing typed", err, want)
	}
}

// A notice into a pane whose window synchronizes its panes is refused, and
// reaches neither that pane nor its sibling: tmux types a key sent to one
// such pane into every pane of the window, so the Enter would submit
// whatever the sibling's input held, in a pane nothing read. Once
// synchronizing is off, the same notice is typed into the pane named once,
// as the positive control, and a marker then typed into the sibling is all
// its file holds, so nothing reached it before, not even an empty line.
func TestANoticeIntoASynchronizedPaneIsRefusedAndReachesNoPane(t *testing.T) {
	t.Parallel()
	server := startCatPanes(t, 1)
	sibling := filepath.Join(filepath.Dir(server.socket), "sibling")
	server.ids = append(server.ids, server.tmux(t, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", server.ids[0],
		"--", "/bin/sh", "-c", "exec cat >> "+sibling))
	server.files = append(server.files, sibling)
	server.tmux(t, "set-option", "-w", "-t", server.ids[0], "synchronize-panes", "on")

	const notice, marker = "A notice for the first pane.", "A marker for the sibling."
	refusedFor(t, agents.TypeIntoPane(t.Context(), server.pane(0), notice), agents.PaneRefusal{Pane: server.ids[0], Synchronized: true})
	server.tmux(t, "set-option", "-w", "-t", server.ids[0], "synchronize-panes", "off")
	if err := agents.TypeIntoPane(t.Context(), server.pane(0), notice); err != nil {
		t.Fatal(err)
	}
	if typed := server.awaitTyped(t, 0); typed != notice+"\n" {
		t.Fatalf("the pane named was typed %q, want %q once, after the refusal typed nothing", typed, notice)
	}
	if err := agents.TypeIntoPane(t.Context(), server.pane(1), marker); err != nil {
		t.Fatal(err)
	}
	if typed := server.awaitTyped(t, 1); typed != marker+"\n" {
		t.Fatalf("the synchronized sibling was typed %q before its own marker, want only the marker %q", typed, marker)
	}
	if buffers := server.tmux(t, "list-buffers", "-F", "#{buffer_name}"); strings.Contains(buffers, "assistant-wake") {
		t.Errorf("the refusal left its buffer on the server: %q", buffers)
	}
}

// A notice into a pane in copy mode is refused with nothing typed: tmux
// routes a key sent to a pane in a mode to the mode, so the Enter would
// leave the notice typed and never submitted while the call answered
// success. The pane's mode is read back first, so the test is not passing
// on a pane that never entered it, and once the pane leaves copy mode the
// same notice is submitted once, as the positive control.
func TestANoticeIntoAPaneInCopyModeIsRefusedWithNothingTyped(t *testing.T) {
	t.Parallel()
	server := startCatPanes(t, 1)
	server.tmux(t, "copy-mode", "-t", server.ids[0])
	if mode := server.tmux(t, "display-message", "-p", "-t", server.ids[0], "#{pane_in_mode}"); mode != "1" {
		t.Fatalf("the pane did not enter copy mode: pane_in_mode is %q", mode)
	}
	const notice = "A notice for a pane in copy mode."
	refusedFor(t, agents.TypeIntoPane(t.Context(), server.pane(0), notice), agents.PaneRefusal{Pane: server.ids[0], InMode: true})
	server.tmux(t, "send-keys", "-t", server.ids[0], "-X", "cancel")
	if err := agents.TypeIntoPane(t.Context(), server.pane(0), notice); err != nil {
		t.Fatal(err)
	}
	if typed := server.awaitTyped(t, 0); typed != notice+"\n" {
		t.Fatalf("the pane was typed %q, want %q submitted once, after the refusal in copy mode typed nothing", typed, notice)
	}
}
