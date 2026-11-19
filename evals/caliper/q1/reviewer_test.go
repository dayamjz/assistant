//go:build unix

package q1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// reviewer is one review-server.py process (README "Reviewer process") a
// test started with startReviewer.
type reviewer struct {
	t       *testing.T
	dir     string // its TMPDIR, the directory its socket is in
	log     string // its request log, beside dir as run-full.sh keeps it outside
	pid     int    // 0 until its socket answers
	done    chan error
	cancel  context.CancelFunc
	stderr  strings.Builder // read only once done has answered
	stopped bool
}

// firstLine sends the first line written to it on line, once, and drops the
// rest. Only the one copier goroutine procgroup.Run starts writes to it.
type firstLine struct {
	buf  []byte
	sent bool
	line chan<- string
}

func (f *firstLine) Write(p []byte) (int, error) {
	if !f.sent {
		f.buf = append(f.buf, p...)
		if i := bytes.IndexByte(f.buf, '\n'); i >= 0 {
			f.sent = true
			f.line <- string(f.buf[:i])
		}
	}
	return len(p), nil
}

// startReviewer starts review-server.py as run-full.sh's start_reviewer does:
// token on its standard input and in neither its environment nor its argv,
// a 0700 TMPDIR of its own holding its socket, a request log outside it, and
// env, where PATH names the claude it runs. It returns once the socket answers. The test's cleanup
// stops it (stop), so every test that starts one checks its teardown.
func startReviewer(t *testing.T, token string, env ...string) *reviewer {
	t.Helper()
	dir, err := standin.TempDir("q1-rev")
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.Remove(dir + ".requests.jsonl") })
	must(t, os.Chmod(dir, 0o700))
	r := &reviewer{t: t, dir: dir, log: dir + ".requests.jsonl", done: make(chan error, 1)}
	line := make(chan string, 1)
	cmd := procgroup.Command("python3", filepath.Join(spec, "review-server.py"), r.socket(), r.log)
	cmd.Env = append([]string{"TMPDIR=" + dir}, env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(token), &firstLine{line: line}, &r.stderr
	var ctx context.Context
	ctx, r.cancel = context.WithTimeout(context.Background(), 10*time.Minute)
	go func() { r.done <- procgroup.Run(ctx, cmd, 5*time.Second) }()
	t.Cleanup(r.stop)
	select {
	case pid := <-line:
		r.pid, err = strconv.Atoi(pid)
		must(t, err)
	case err := <-r.done:
		r.stopped = true
		t.Fatalf("review-server.py ended before its socket answered: %v\n%s", err, r.stderr.String())
	case <-time.After(30 * time.Second):
		t.Fatal("review-server.py's socket did not answer within 30s")
	}
	return r
}

// socket is where the reviewer process listens.
func (r *reviewer) socket() string { return filepath.Join(r.dir, "socket") }

// stop ends the reviewer process as run-full.sh's stop_reviewer does, with
// SIGTERM, and fails the test unless it exits 0 within 30s leaving nothing
// in its directory: no socket and no review's HOME. Stopping twice is once.
func (r *reviewer) stop() {
	r.t.Helper()
	if r.stopped {
		return
	}
	r.stopped = true
	defer r.cancel()
	if r.pid != 0 {
		if err := syscall.Kill(r.pid, syscall.SIGTERM); err != nil {
			r.t.Errorf("SIGTERM to the reviewer process %d: %v", r.pid, err)
		}
	}
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Errorf("the reviewer process's exit after SIGTERM: %v\n%s", err, r.stderr.String())
		}
	case <-time.After(30 * time.Second):
		r.cancel()
		<-r.done
		r.t.Errorf("the reviewer process %d had not ended 30s after SIGTERM", r.pid)
	}
	left, err := os.ReadDir(r.dir)
	must(r.t, err)
	if len(left) != 0 {
		r.t.Errorf("the reviewer process left %v in %s", left, r.dir)
	}
}

// printStartRecord is this binary run with Q1_TEST_START_RECORD=pid: it
// prints how many times the process's start record (startRecord) holds what
// it reads on standard input, then TMPDIR=, the control that the read reached
// the environment, never the record itself. Its exit status is 2 when either
// cannot be read.
func printStartRecord(pid string) int {
	n, err := strconv.Atoi(pid)
	needle, readErr := io.ReadAll(os.Stdin)
	var record []byte
	if err == nil && readErr == nil && len(needle) > 0 {
		record, err = startRecord(n)
	}
	if err != nil || readErr != nil || len(needle) == 0 {
		fmt.Fprintln(os.Stderr, "start record of", pid, err, readErr, len(needle))
		return 2
	}
	fmt.Println(bytes.Count(record, needle), bytes.Count(record, []byte("TMPDIR=")))
	return 0
}

// TestReviewHookWithoutItsReviewerProcessCannotStart (a#968): a review with no
// reviewer process to run it is never skipped and never a pass. The push is
// refused cannot-start, naming why and where: setup gave the hook no socket,
// the process is gone (stopped before the push), or it ended mid-review, which
// ends the reviewer it started with it. The reviewer's stand-in is asked only
// in the last, and no case moves the remote.
func TestReviewHookWithoutItsReviewerProcessCannotStart(t *testing.T) {
	for _, c := range []struct{ name, why string }{
		{"not configured", "no reviewer process: Q1_REVIEWER_SOCKET was unset or empty when setup installed this hook"},
		{"gone", "the reviewer process at %s does not answer"},
		{"ended mid-review", "the reviewer process at %s started no review: it ended without answering"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var extra []string
			if c.name == "not configured" {
				extra = []string{"Q1_REVIEWER_SOCKET="} // the last value wins (os/exec Cmd.Env)
			}
			a, agent, seen := reviewArm(t, standin.Script{Steps: []standin.Step{{Reply: standin.Hang()}}}, extra...)
			why := c.why
			if strings.Contains(why, "%s") {
				why = fmt.Sprintf(why, a.reviewer.socket())
			}
			if c.name == "gone" {
				a.reviewer.stop()
			}
			pushed := make(chan [2]any, 1)
			go func() {
				code, _, errs, err := a.execute(a.checkout, "git", "push", "-q", "../remote.git", "feature/unlimited")
				pushed <- [2]any{fmt.Sprintf("exit %d, %v", code, err), errs}
			}()
			calls := 0
			if c.name == "ended mid-review" {
				calls = 1
				for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
					if len(agent.Calls()) > 0 {
						break // the reviewer is holding its call
					}
					if time.Now().After(deadline) {
						t.Fatal("no reviewer started within 60s of the push")
					}
				}
				a.reviewer.stop()
			}
			got := <-pushed
			want(t, "push refused cannot-start, naming why", []any{got[0], strings.Contains(got[1].(string),
				"Review rejected this push (cannot-start)"), strings.Contains(got[1].(string), why)}, []any{"exit 1, <nil>", true, true})
			want(t, "entries", verdicts(a.reviewEntries()), []string{"cannot-start rejected"})
			want(t, "reviewer calls", len(agent.Calls()), calls)
			want(t, "remote tip", a.remoteTip(), "")
			for _, s := range reviewersSeen(t, seen) {
				if !gone(s.pid, 30*time.Second) {
					t.Errorf("reviewer %d still runs after its reviewer process ended", s.pid)
				}
			}
		})
	}
}

// ask sends the reviewer process one request as any client on the machine
// can, the agent included, and returns its answer line.
func (r *reviewer) ask(t *testing.T, request map[string]any) string {
	t.Helper()
	conn, err := net.Dial("unix", r.socket())
	must(t, err)
	defer func() { _ = conn.Close() }()
	data, err := json.Marshal(request)
	must(t, err)
	_, err = conn.Write(append(data, '\n'))
	must(t, err)
	answer, err := io.ReadAll(conn)
	must(t, err)
	return string(answer)
}

// requestsChecked is reviewer_requests.py on the reviewer process's log and the
// attempt's arm: its exit and what it printed.
func (a *attempt) requestsChecked() (int, map[string]any) {
	a.t.Helper()
	code, out, errs := a.run(a.root, "python3", filepath.Join(spec, "reviewer_requests.py"), a.reviewer.log,
		filepath.Join(a.tmpdir, "caliper-q1-review"))
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		a.t.Fatalf("reviewer_requests.py: exit %d, %v\n%s%s", code, err, out, errs)
	}
	return code, got
}

// TestReviewerRequestsFlagAnAttemptThatAskedTheReviewerItself (coordinator,
// a#971): the agent runs as the operator's user and its hook names the
// socket, so it can have the reviewer review something before it pushes. Each
// request the reviewer process accepts is logged, and reviewer_requests.py
// holds the log against the hooks' entries. A push the hook reviewed is one
// request and one hook review, which passes (the positive control); one more
// request naming the attempt's HOME, sent straight to the socket, makes that
// attempt contaminated and the check exit 1; and one naming no attempt is
// counted apart, and fails it too.
func TestReviewerRequestsFlagAnAttemptThatAskedTheReviewerItself(t *testing.T) {
	a, agent, _ := reviewArm(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text(approve)}}})
	if code, errs := a.push(); code != 0 {
		t.Fatalf("the approved push was refused: exit %d\n%s", code, errs)
	}
	code, got := a.requestsChecked()
	want(t, "after the hook's one review: exit, report", []any{code, got}, []any{0, map[string]any{
		"attempts":     map[string]any{a.name(): map[string]any{"requests": 1.0, "hook_reviews": 1.0}},
		"contaminated": []any{}, "unlogged": []any{}, "requests_naming_no_attempt": 0.0}})
	answer := a.reviewer.ask(t, map[string]any{"tree": a.checkout, "prompt": "is this ready?", "limit": 30, "root": a.root})
	want(t, "the direct request was reviewed: answer holds argv, reviewer calls", []any{strings.Contains(answer, `"argv"`),
		len(agent.Calls())}, []any{true, 2})
	code, got = a.requestsChecked()
	want(t, "after a request no hook made: exit, contaminated, counts", []any{code, got["contaminated"], got["attempts"]},
		[]any{1, []any{a.name()}, map[string]any{a.name(): map[string]any{"requests": 2.0, "hook_reviews": 1.0}}})
	a.reviewer.ask(t, map[string]any{"root": "/elsewhere"})
	code, got = a.requestsChecked()
	want(t, "after a request naming no attempt: exit, its count", []any{code, got["requests_naming_no_attempt"]}, []any{1, 1.0})
}
