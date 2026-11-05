//go:build unix

package agents_test

import (
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
	tmux := standin.Tmux(t)
	dir := standin.SocketDir(t)
	socket := filepath.Join(dir, "s")
	const panes = 6
	typed := func(i int) string { return filepath.Join(dir, fmt.Sprintf("typed%d", i)) }
	tmuxRun := func(args ...string) string {
		t.Helper()
		raw, err := exec.Command(tmux, append([]string{"-S", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %q: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	tmuxRun("new-session", "-d", "-x", "120", "-y", "30", "--", "/bin/sh", "-c", "exec cat >> "+typed(0))
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	ids := []string{tmuxRun("display-message", "-p", "#{pane_id}")}
	for i := 1; i < panes; i++ {
		ids = append(ids, tmuxRun("new-window", "-d", "-P", "-F", "#{pane_id}", "--", "/bin/sh", "-c", "exec cat >> "+typed(i)))
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, panes)
	for i := range panes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pane := agents.Pane{Tmux: tmux, Socket: socket, ID: ids[i]}
			errs[i] = agents.TypeIntoPane(t.Context(), pane, fmt.Sprintf("A notice for pane %d.", i))
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
		deadline := time.Now().Add(10 * time.Second)
		var lines []string
		for {
			raw, _ := os.ReadFile(typed(i))
			lines = strings.FieldsFunc(string(raw), func(r rune) bool { return r == '\n' })
			if len(lines) > 0 || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(lines) != 1 || lines[0] != want {
			t.Errorf("pane %d was typed %q, want %q once", i, lines, want)
		}
	}
	if buffers := tmuxRun("list-buffers", "-F", "#{buffer_name}"); strings.Contains(buffers, "assistant-wake") {
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
