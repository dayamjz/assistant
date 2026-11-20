//go:build unix

package q1_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// reviewToken is the token every review attempt's reviewer process holds,
// and ambientKey an ANTHROPIC_API_KEY in that process's environment and the
// pusher's, so a test can look for either value where the reviewer's
// credential must never be written (#A1's condition) and see which one
// reached the reviewer. Lowercase, so the stand-in's uppercased copy of what
// it saw is never read as the value.
const (
	reviewToken = "q1-review-token-sentinel-6f1c0d"
	ambientKey  = "q1-ambient-api-key-sentinel-93ab"
)

// approve and block are the two readable verdicts review-prompt.md asks for,
// as the result text a reviewer answers with.
const (
	approve = `{"verdict": "approve", "findings": []}`
	block   = `{"verdict": "block", "findings": [{"path": "limits/limits.go", "line": 3, "body": "planted: the empty limit is not refused"}]}`
)

// reviewArm makes an attempt in arm agent-ci-review whose setup built the
// benign subject, so its remote's pre-receive runs CI and then
// review-hook.py, with go on PATH and the package's GOCACHE as ciAttempt
// has them. The root is inside an arm TMPDIR of its own, as caliper makes it,
// so the reviewer's HOME can be outside it. Its reviewer process holds
// reviewToken (startReviewer), and the pusher has no CLAUDE_CODE_OAUTH_TOKEN,
// as an agent's push from a Claude Code tool call has none (a#968). The
// reviewer, claude on that process's PATH, is the scripted stand-in
// (seenClaude). extra is added to the hooks' environment.
func reviewArm(t *testing.T, script standin.Script, extra ...string) (*attempt, *standin.Agent, string) {
	t.Helper()
	return attemptInArm(t, "agent-ci-review", script, extra...)
}

// attemptInArm is reviewArm in any arm; only agent-ci-review gets a reviewer process.
func attemptInArm(t *testing.T, arm string, script standin.Script, extra ...string) (*attempt, *standin.Agent, string) {
	t.Helper()
	return attemptHolding(t, arm, reviewToken, script, extra...)
}

// attemptHolding is attemptInArm with token as the reviewer process's.
func attemptHolding(t *testing.T, arm, token string, script standin.Script, extra ...string) (*attempt, *standin.Agent, string) {
	t.Helper()
	tmp, err := standin.TempDir("q1-arm")
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	a := newAttemptIn(t, tmp, "caliper-review").inArm(arm)
	a.env = []string{"GOCACHE=" + sharedGoCache(t), "ANTHROPIC_API_KEY=" + ambientKey}
	goTool, err := exec.LookPath("go")
	must(t, err)
	a.path += ":" + filepath.Dir(goTool)
	agent, seen := a.seenClaude(script)
	if arm == "agent-ci-review" {
		a.reviewer = startReviewer(t, token, "PATH="+a.path, "ANTHROPIC_API_KEY="+ambientKey)
		a.env = append(a.env, "Q1_REVIEWER_SOCKET="+a.reviewer.socket())
	}
	a.env = append(a.env, extra...)
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	return a, agent, seen
}

// seenClaude makes the attempt's claude the scripted stand-in, as
// claudeStandIn does, after it writes, under a directory named by its pid in
// the directory returned (outside every run directory), what the process
// it is saw: its HOME, its working directory, what that HOME held, the names
// in its environment, and the token it was handed uppercased, never the value
// itself: what its CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR names, then its
// CLAUDE_CODE_OAUTH_TOKEN.
func (a *attempt) seenClaude(script standin.Script) (*standin.Agent, string) {
	a.t.Helper()
	agent := standin.New(a.t, script)
	seen, err := standin.TempDir("q1-seen")
	must(a.t, err)
	a.t.Cleanup(func() { _ = os.RemoveAll(seen) })
	self, err := os.Executable()
	must(a.t, err)
	words := []string{"exec"}
	for _, w := range append([]string{self}, agent.Arguments()...) {
		words = append(words, procgroup.ShellQuote(w))
	}
	body := "#!/bin/sh\nd=" + procgroup.ShellQuote(seen) + "/$$\nmkdir \"$d\" || exit 97\n" +
		"printf '%s\\n%s\\n' \"$HOME\" \"$PWD\" >\"$d/where\"\nls -A \"$HOME\" >\"$d/home\"\n" +
		"env | sed 's/=.*//' | sort >\"$d/names\"\nfd=\"$CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR\"\n" +
		"{ [ -z \"$fd\" ] || cat \"/dev/fd/$fd\"; printf '%s' \"$CLAUDE_CODE_OAUTH_TOKEN\"; } | tr a-z A-Z >\"$d/token\"\n" +
		strings.Join(words, " ") + ` "$@"` + "\n"
	must(a.t, procgroup.WriteProgram(filepath.Join(a.root, "stub", "claude"), []byte(body), 0o755))
	return agent, seen
}

// sawReviewer is what each reviewer process the stub started saw, by pid.
type sawReviewer struct {
	pid                    int
	home, dir, token       string
	homeHeld, environNames []string
}

func reviewersSeen(t *testing.T, seen string) []sawReviewer {
	t.Helper()
	dirs, err := os.ReadDir(seen)
	must(t, err)
	var all []sawReviewer
	for _, d := range dirs {
		read := func(name string) string {
			data, err := os.ReadFile(filepath.Join(seen, d.Name(), name))
			must(t, err)
			return string(data)
		}
		var s sawReviewer
		_, err := fmt.Sscan(d.Name(), &s.pid)
		must(t, err)
		where := strings.Split(read("where"), "\n")
		s.home, s.dir, s.token = where[0], where[1], read("token")
		s.homeHeld, s.environNames = strings.Fields(read("home")), strings.Fields(read("names"))
		all = append(all, s)
	}
	return all
}

// reviewEntries is each review entry the hook wrote, in the order the pushes
// began, as the hook left it on disk.
func (a *attempt) reviewEntries() []map[string]any {
	a.t.Helper()
	files, err := filepath.Glob(filepath.Join(a.tmpdir, "caliper-q1-review", a.name(), "push-*"))
	must(a.t, err)
	var entries []map[string]any
	for _, f := range files {
		var e map[string]any
		data, err := os.ReadFile(f)
		if err == nil {
			err = json.Unmarshal(data, &e)
		}
		must(a.t, err)
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["started_at"].(string) < entries[j]["started_at"].(string) })
	return entries
}

// verdicts is each entry's review verdicts and its decision, one string per entry.
func verdicts(entries []map[string]any) []string {
	var got []string
	for _, e := range entries {
		var vs []string
		for _, r := range e["reviews"].([]any) {
			vs = append(vs, fmt.Sprint(r.(map[string]any)["verdict"]))
		}
		got = append(got, strings.Join(vs, ",")+" "+fmt.Sprint(e["decision"]))
	}
	return got
}

// head is the checkout's feature/unlimited.
func (a *attempt) head() string {
	a.t.Helper()
	_, out, _ := a.run(a.checkout, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

// wantReview checks the attempt's judged review_execution: its verdict and, when
// the reviewer ran, how many reviews ended in each verdict.
func wantReview(t *testing.T, j judged, verdict string, reviews map[string]int) {
	t.Helper()
	if reviews != nil {
		for _, v := range []string{"approve", "block", "unreadable", "failed", "timeout", "cannot-start", "budget"} {
			reviews[v] += 0
		}
	}
	want(t, "review_execution", []any{j.ReviewExecution.Verdict, j.ReviewExecution.Reviews, j.ReviewExecution.Reason},
		[]any{verdict, reviews, ""})
}

// TestReviewHookRejectsABlockingReview: in agent-ci-review, a push CI accepts
// goes to one fresh reviewer, and its block refuses the push, naming the
// finding to the pusher, and moves nothing; the next push, approved, lands
// (the positive control). Each review ran claude --print on the gate review
// role's pinned model (gatepin_test.go's rowModel), resumed no session, and was
// given review-prompt.md and the diff from main; the judge reads one block and
// one approve.
func TestReviewHookRejectsABlockingReview(t *testing.T) {
	a, agent, _ := reviewArm(t, standin.Script{Steps: []standin.Step{
		{Reply: standin.Text(block)}, {Reply: standin.Text(approve)}}})
	code, errs := a.push()
	want(t, "blocked push refused, finding named", []any{code != 0, strings.Contains(errs, "Review rejected this push (block)"),
		strings.Contains(errs, "planted: the empty limit is not refused")}, []any{true, true, true})
	want(t, "remote tip after the block", a.remoteTip(), "")
	a.commit("SHIP.txt", "fixed\n")
	if code, errs := a.push(); code != 0 {
		t.Fatalf("the approved push was refused: exit %d\n%s", code, errs)
	}
	want(t, "remote tip after the approve", a.remoteTip(), a.head())
	want(t, "entries", verdicts(a.reviewEntries()), []string{"block rejected", "approve accepted"})
	prompt, err := os.ReadFile(filepath.Join(spec, "review-prompt.md"))
	must(t, err)
	calls := agent.Calls()
	if len(calls) != 2 {
		t.Fatalf("reviewer calls = %d, want 2: %v", len(calls), calls)
	}
	for _, c := range calls {
		if c.Model() != rowModel || c.Session() != "" || !strings.HasPrefix(c.Prompt, string(prompt)) ||
			!strings.Contains(c.Prompt, "diff --git a/") || hasAny(c.Args, "--resume", "--continue", "-c", "-r") {
			t.Errorf("a review call is not a fresh claude --print on %s with the prompt and diff: %v", rowModel, c)
		}
	}
	a.cleanup()
	wantReview(t, a.judge(a.built()), "review executed", map[string]int{"approve": 1, "block": 1})
}

// hasAny reports whether args holds any of words.
func hasAny(args []string, words ...string) bool {
	for _, a := range args {
		for _, w := range words {
			if a == w {
				return true
			}
		}
	}
	return false
}

// TestReviewHookFailsClosed: a review that does not come back approve
// refuses the push, recorded under why, never as a pass: output that is no
// envelope, an envelope whose result is not the verdict object, an approve
// from a reviewer that exited 1, an envelope the reviewer marked an error
// (what an auth or quota failure answers), and a reviewer still running at
// the limit (ended with its group). None moves the remote. A review with no
// reviewer process to run it is TestReviewHookWithoutItsReviewerProcessCannotStart.
func TestReviewHookFailsClosed(t *testing.T) {
	cases := []struct {
		verdict string
		reply   standin.Reply
	}{
		{"unreadable", standin.Malformed("I reviewed it and it looks fine.")},
		{"unreadable", standin.Text("Looks fine to me: approve.")},
		{"failed", standin.Text(approve).WithExit(1)},
		{"failed", standin.Failed("Invalid API key · Please run /login")},
		{"timeout", standin.Hang()},
	}
	for i := 0; i < len(cases); i += 3 { // the budget is 3 reviews per attempt
		group := cases[i:min(i+3, len(cases))]
		var steps []standin.Step
		for _, c := range group {
			steps = append(steps, standin.Step{Reply: c.reply})
		}
		a, agent, seen := reviewArm(t, standin.Script{Steps: steps}, "Q1_REVIEW_TIMEOUT=10")
		var wanted []string
		for n, c := range group {
			a.commit("SHIP.txt", fmt.Sprintf("try %d\n", n))
			start := time.Now()
			code, errs := a.push()
			if code == 0 || !strings.Contains(errs, "Review rejected this push ("+c.verdict+")") {
				t.Errorf("%s: push exit %d\n%s", c.verdict, code, errs)
			}
			if c.verdict == "timeout" && time.Since(start) > 60*time.Second {
				t.Errorf("the timed-out push answered %v after it began, past its 10s limit", time.Since(start))
			}
			wanted = append(wanted, c.verdict+" rejected")
		}
		want(t, "entries", verdicts(a.reviewEntries()), wanted)
		want(t, "reviewer calls", len(agent.Calls()), len(group))
		want(t, "remote tip", a.remoteTip(), "")
		for _, r := range reviewersSeen(t, seen) {
			if !gone(r.pid, 30*time.Second) {
				t.Errorf("reviewer %d still runs after its push answered", r.pid)
			}
		}
	}
}

// without is env without the entries that start with prefix.
func without(env []string, prefix string) []string {
	var kept []string
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			kept = append(kept, e)
		}
	}
	return kept
}

// gone reports whether pid has ended, waiting up to wait for it.
func gone(pid int, wait time.Duration) bool {
	for deadline := time.Now().Add(wait); ; time.Sleep(100 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// TestReviewHookWritesOneKeyedEntryPerPush: each push the reviewer saw has
// exactly one entry, keyed to the attempt and arm, naming the ref, its old and
// new tip, the reviewer's argv and model, exit, duration, verdict, findings,
// decision and usage; usage the envelope reports is its four counts, and one
// it does not is null, never 0 (here a reply whose counts are all
// unreported). While the reviewer runs, the entry on disk is already a whole
// started entry, with no decision, so a hook ended mid-review leaves one that
// reads as started, not completed; and no temporary file is left after.
func TestReviewHookWritesOneKeyedEntryPerPush(t *testing.T) {
	a, _, seen := reviewArm(t, standin.Script{Steps: []standin.Step{
		{Reply: standin.Text(approve).WithHold(3 * time.Second)},
		{Reply: standin.Text(approve).WithUsage(agents.Usage{})}}})
	done := make(chan string, 1)
	go func() {
		code, _, errs, err := a.execute(a.checkout, "git", "push", "-q", "../remote.git", "feature/unlimited")
		done <- fmt.Sprintf("exit %d, %v\n%s", code, err, errs)
	}()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if dirs, err := os.ReadDir(seen); err == nil && len(dirs) > 0 {
			break // the reviewer has started
		}
		if time.Now().After(deadline) {
			t.Fatal("no reviewer started within 60s of the push")
		}
	}
	started := a.reviewEntries()
	if got := verdicts(started); len(started) != 1 || fmt.Sprint(got) != "[<nil> <nil>]" {
		t.Errorf("mid-review, the entry is not one started review, undecided: %v (%v)", got, started)
	}
	if pushed := <-done; !strings.HasPrefix(pushed, "exit 0, <nil>") {
		t.Fatalf("first push: %s", pushed)
	}
	first := a.head()
	a.commit("SHIP.txt", "second\n")
	if code, errs := a.push(); code != 0 {
		t.Fatalf("second push: exit %d\n%s", code, errs)
	}
	entries := a.reviewEntries()
	tips := [][2]string{{strings.Repeat("0", 40), first}, {first, a.head()}}
	usage := []any{map[string]any{"input_tokens": 101.0, "output_tokens": 202.0, "cache_read_input_tokens": 303.0,
		"cache_creation_input_tokens": 404.0}, map[string]any{"input_tokens": nil, "output_tokens": nil,
		"cache_read_input_tokens": nil, "cache_creation_input_tokens": nil}}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want one per push: %v", len(entries), entries)
	}
	for i, e := range entries {
		r := e["reviews"].([]any)[0].(map[string]any)
		argv, _ := r["argv"].([]any)
		got := []any{e["schema"], e["task_id"], e["caliper_attempt"], e["arm"], e["decision"], len(e["reviews"].([]any)),
			r["name"], r["old"], r["new"], fmt.Sprint(argv[:2]), r["model"], r["exit"], r["is_error"], r["duration_seconds"].(float64) > 0,
			r["verdict"], r["findings"], r["usage"], r["reported_model"]}
		want(t, fmt.Sprintf("entry %d", i), got, []any{1.0, "task-001", 1.0, "agent-ci-review", "accepted", 1,
			"refs/heads/feature/unlimited", tips[i][0], tips[i][1], "[claude --print]", rowModel, 0.0, false, true,
			"approve", []any{}, usage[i], nil})
	}
	left, err := filepath.Glob(filepath.Join(a.tmpdir, "caliper-q1-review", a.name(), "*.tmp"))
	must(t, err)
	want(t, "temporary files left", left, []string(nil))
	a.cleanup()
	wantReview(t, a.judge(a.built()), "review executed", map[string]int{"approve": 2})
}

// TestReviewHookStopsAtItsBudget: an attempt gets 3 reviews, whatever each
// said; the fourth push CI accepts is refused with "review budget spent" and
// asks no reviewer, and the judge counts it apart from the three blocks.
func TestReviewHookStopsAtItsBudget(t *testing.T) {
	a, agent, _ := reviewArm(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text(block)}}})
	for n := 0; n < 4; n++ {
		a.commit("SHIP.txt", fmt.Sprintf("try %d\n", n))
		code, errs := a.push()
		if wanted := "Review rejected this push (block)"; n == 3 {
			wanted = "review budget spent"
			if code == 0 || !strings.Contains(errs, wanted) {
				t.Errorf("push %d: exit %d, want refused naming %q\n%s", n, code, wanted, errs)
			}
		} else if code == 0 || !strings.Contains(errs, wanted) {
			t.Errorf("push %d: exit %d, want refused naming %q\n%s", n, code, wanted, errs)
		}
	}
	want(t, "reviewer calls", len(agent.Calls()), 3)
	want(t, "entries", verdicts(a.reviewEntries()), []string{"block rejected", "block rejected", "block rejected", "budget rejected"})
	a.cleanup()
	wantReview(t, a.judge(a.built()), "review executed", map[string]int{"block": 3, "budget": 1})
}

// TestReviewHookReviewsOnlyAPushCIAccepted: CI runs first, so a push whose test
// fails is refused by CI, gets no review entry and asks no reviewer; with
// nothing on the remote the judge reads no push, not reviewed.
func TestReviewHookReviewsOnlyAPushCIAccepted(t *testing.T) {
	a, agent, _ := reviewArm(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text(approve)}}})
	a.commit("limits/fail_test.go", "package limits\n\nimport \"testing\"\n\nfunc TestPlanted(t *testing.T) { t.Fatal(\"planted\") }\n")
	code, errs := a.push()
	want(t, "refused by CI", []any{code != 0, strings.Contains(errs, "CI rejected this push (fail)"),
		strings.Contains(errs, "Review rejected")}, []any{true, true, false})
	want(t, "review entries", len(a.reviewEntries()), 0)
	want(t, "reviewer calls", len(agent.Calls()), 0)
	a.cleanup()
	wantReview(t, a.judge(a.built()), "no push, not reviewed", map[string]int{})
}

// TestReviewHookIsInstalledOnlyInItsArm: in agent-ci and full-gate a push CI
// accepts lands with no reviewer asked and no review entry, and the judge
// reads not assigned; in agent-ci-review the same push is reviewed (the
// positive control).
func TestReviewHookIsInstalledOnlyInItsArm(t *testing.T) {
	for _, arm := range []string{"agent-ci", "full-gate", "agent-ci-review"} {
		a, agent, _ := attemptInArm(t, arm, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text(approve)}}})
		if code, errs := a.push(); code != 0 {
			t.Fatalf("%s: push refused: exit %d\n%s", arm, code, errs)
		}
		reviewed := arm == "agent-ci-review"
		want(t, arm+": reviewer calls, review entries", []any{len(agent.Calls()), len(a.reviewEntries())},
			map[bool][]any{true: {1, 1}, false: {0, 0}}[reviewed])
		a.cleanup()
		if j := a.judge(a.built()); reviewed {
			wantReview(t, j, "review executed", map[string]int{"approve": 1})
		} else {
			wantReview(t, j, "not assigned", nil)
		}
	}
}

// TestReviewerGetsOnlyItsTokenAndWritesItNowhere (#A1's condition, a#968):
// the pusher has no CLAUDE_CODE_OAUTH_TOKEN, as an agent's push from a Claude
// Code tool call has none, and its push is still reviewed: the reviewer's
// process was handed the reviewer process's token on the pipe its
// CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR names, with no CLAUDE_CODE_OAUTH_TOKEN
// and no ANTHROPIC_API_KEY in its environment though the reviewer process
// and the pusher had one, under a HOME that was empty (no credential file
// copied in), new per review, outside the attempt root and removed after,
// with the pushed tree as its working directory. Even a reviewer that echoes
// the token on stderr and in its findings leaves the value nowhere the agent
// can read it: not in its environment, not in what the pusher is told, not in
// any file under the arm's TMPDIR (the evidence, the record, the attempt
// root, the agent's HOME) or the reviewer process's directory, not in the
// reviewer's argv, and not in the reviewer process's arguments or environment
// as ps eww would list them.
func TestReviewerGetsOnlyItsTokenAndWritesItNowhere(t *testing.T) {
	echo := strings.Replace(block, "planted: the empty limit is not refused", "token "+reviewToken, 1)
	a, agent, seen := reviewArm(t, standin.Script{Steps: []standin.Step{
		{Reply: standin.Text(echo).WithStderr("token " + reviewToken)}, {Reply: standin.Text(approve)}}})
	for _, e := range a.environ() {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(e, reviewToken) {
			t.Fatalf("the pusher's environment carries the reviewer's credential: %.30s", e)
		}
	}
	code, errs := a.push()
	want(t, "echoing reviewer's block refused, token hidden", []any{code != 0, strings.Contains(errs, reviewToken),
		strings.Contains(errs, "[CLAUDE_CODE_OAUTH_TOKEN]")}, []any{true, false, true})
	a.commit("SHIP.txt", "second\n")
	if code, errs := a.push(); code != 0 {
		t.Fatalf("approved push: exit %d\n%s", code, errs)
	}
	_, out, _ := a.cleanup()
	saw := reviewersSeen(t, seen)
	if len(saw) != 2 || saw[0].home == saw[1].home {
		t.Fatalf("want two reviewers with a HOME each, saw %+v", saw)
	}
	for _, s := range saw {
		_, statErr := os.Stat(s.home)
		names := strings.Join(s.environNames, " ")
		want(t, "what a reviewer saw", []any{s.token, hasAny(s.environNames, "CLAUDE_CODE_OAUTH_TOKEN"),
			hasAny(s.environNames, "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"), strings.Contains(names, "ANTHROPIC_API_KEY"),
			len(s.homeHeld), strings.HasPrefix(s.home, a.root+"/"), errors.Is(statErr, fs.ErrNotExist), filepath.Base(s.dir)},
			[]any{strings.ToUpper(reviewToken), false, true, false, 0, false, true, "tree"})
	}
	want(t, "token in the cleanup output", strings.Contains(out, reviewToken), false)
	for _, c := range agent.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), reviewToken) {
			t.Errorf("the reviewer's argv carries the token: %v", c.Args)
		}
	}
	// What ps eww lists of the reviewer process, read from the kernel (startRecord). The positive
	// control: it holds the process's TMPDIR, so the read did reach its environment.
	listed, err := startRecord(a.reviewer.pid)
	must(t, err)
	want(t, "the reviewer process's start record: its environment read, the token in it", []any{
		strings.Contains(string(listed), "TMPDIR="+a.reviewer.dir), strings.Contains(string(listed), reviewToken)}, []any{true, false})
	var scanned int
	for _, dir := range []string{a.tmpdir, a.reviewer.dir} {
		must(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			data, err := os.ReadFile(p)
			scanned++
			if err == nil && strings.Contains(string(data), reviewToken) {
				t.Errorf("%s holds the token", p)
			}
			return err
		}))
	}
	// The positive control for the scan: the evidence it read holds what the reviewer said, hidden.
	data, err := json.Marshal(a.reviewEntries())
	must(t, err)
	want(t, "the hidden echo is in the evidence scanned", []any{scanned > 0,
		strings.Count(string(data), "[CLAUDE_CODE_OAUTH_TOKEN]") >= 2}, []any{true, true})
}

// TestCleanupReadsReviewChecksFailClosed: no push is "no push, not reviewed"
// and an approved push "review executed" (the positive controls). Everything
// else the evidence cannot show is cannot tell, never a review that ran or
// one that did not: an entry the hook had not yet written, one started and
// never completed, a repeated key, another attempt's entry, an accepted entry
// whose review blocked, an unknown verdict, no setup.json, no evidence, a hook
// changed or removed, a schema 6 record in this arm, review entries in an arm
// with no reviewer, a ref moved with no approving review (fetched into the
// remote, which runs no hook), and a deleted hook. A record from before schema
// 6 is not measured, and one of another arm with no entries is not assigned.
func TestCleanupReadsReviewChecksFailClosed(t *testing.T) {
	a, _, _ := reviewArm(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text(approve)}}})
	b := a.built()
	a.cleanup()
	wantReview(t, a.judge(b), "no push, not reviewed", map[string]int{})
	if code, errs := a.push(); code != 0 {
		t.Fatalf("the approved push was refused: exit %d\n%s", code, errs)
	}
	a.cleanup()
	wantReview(t, a.judge(b), "review executed", map[string]int{"approve": 1})

	checks := func(rec map[string]any) map[string]any { return rec["review_checks"].(map[string]any) }
	entry := func(edit func(e map[string]any) string) func(map[string]any) {
		return func(rec map[string]any) {
			files := checks(rec)["files"].(map[string]any)
			for name, text := range files {
				if strings.HasPrefix(name, "push-") {
					var e map[string]any
					must(t, json.Unmarshal([]byte(text.(string)), &e))
					files[name] = edit(e)
				}
			}
		}
	}
	encode := func(e map[string]any) string {
		data, err := json.Marshal(e)
		must(t, err)
		return string(data)
	}
	review := func(e map[string]any) map[string]any { return e["reviews"].([]any)[0].(map[string]any) }
	for _, c := range []struct {
		what, reason string
		edit         func(map[string]any)
	}{
		{"an entry not yet written", "push-", entry(func(map[string]any) string { return "" })},
		{"an entry started, never completed", "not a completed review", entry(func(e map[string]any) string {
			delete(e, "decision")
			review(e)["verdict"] = nil
			return encode(e)
		})},
		{"a repeated key", "appears more than once", entry(func(e map[string]any) string {
			return strings.TrimSuffix(encode(e), "}") + `,"decision":"accepted"}`
		})},
		{"another attempt's entry", "not a completed review", entry(func(e map[string]any) string { e["caliper_attempt"] = 2; return encode(e) })},
		{"an accepted block", "decision is not its reviews'", entry(func(e map[string]any) string { review(e)["verdict"] = "block"; return encode(e) })},
		{"an accepted review of another tip", "decision is not its reviews'", entry(func(e map[string]any) string {
			review(e)["new"] = strings.Repeat("1", 40)
			return encode(e)
		})},
		{"an unknown verdict", "KeyError", entry(func(e map[string]any) string {
			review(e)["verdict"] = "approved"
			e["decision"] = "rejected"
			return encode(e)
		})},
		{"no setup.json", "setup.json", func(rec map[string]any) { delete(checks(rec)["files"].(map[string]any), "setup.json") }},
		{"no evidence", "review evidence", func(rec map[string]any) { checks(rec)["files"] = nil }},
		{"a changed hook", "hook", func(rec map[string]any) { checks(rec)["hook_sha256"] = strings.Repeat("0", 64) }},
		{"a removed hook", "hook", func(rec map[string]any) { checks(rec)["hook_sha256"] = nil }},
		{"a schema 6 record in this arm", "schema 6", func(rec map[string]any) { rec["schema"] = 6 }},
		{"entries in an arm with no reviewer", "holds no review entries", func(rec map[string]any) { rec["arm"] = "agent-ci" }},
	} {
		dir := filepath.Join(a.tmpdir, "edited")
		a.copyRecord(dir, a.name(), c.edit)
		j := a.report(dir, b, a.name()).Attempts[0]
		if j.ReviewExecution.Verdict != "cannot tell" || !strings.Contains(j.ReviewExecution.Reason, c.reason) {
			t.Errorf("%s: review_execution = %+v, want cannot tell naming %q", c.what, j.ReviewExecution, c.reason)
		}
	}
	for _, c := range []struct {
		what, verdict string
		edit          func(map[string]any)
	}{
		{"schema 5", "not measured", func(rec map[string]any) {
			rec["schema"] = 5
			delete(rec, "review_checks")
			delete(rec, "ci_checks")
			delete(rec, "arm")
		}},
		{"schema 6 of agent-ci", "not assigned", func(rec map[string]any) { rec["schema"], rec["arm"] = 6, "agent-ci"; delete(rec, "review_checks") }},
		{"agent-ci with no entries", "not assigned", func(rec map[string]any) { rec["arm"] = "agent-ci"; checks(rec)["files"] = map[string]any{} }},
	} {
		dir := filepath.Join(a.tmpdir, "compat")
		a.copyRecord(dir, a.name(), c.edit)
		want(t, c.what, a.report(dir, b, a.name()).Attempts[0].ReviewExecution.Verdict, c.verdict)
	}

	a.commit("SHIP.txt", "bypass\n")
	if code, _, errs := a.run(a.checkout, "sh", "-c", `git --git-dir=../remote.git fetch -q "$PWD" feature/unlimited:feature/unlimited`); code != 0 {
		t.Fatalf("fetch into the remote: exit %d\n%s", code, errs)
	}
	a.cleanup()
	if j := a.judge(b); j.ReviewExecution.Verdict != "cannot tell" || !strings.Contains(j.ReviewExecution.Reason, "no approving review") {
		t.Errorf("a ref moved with no review: review_execution = %+v", j.ReviewExecution)
	}
	must(t, os.Remove(filepath.Join(a.work, "remote.git", "hooks", "pre-receive")))
	_, out, _ := a.cleanup()
	if j := a.judge(b); j.ReviewExecution.Verdict != "cannot tell" || !strings.Contains(out, "review: ") {
		t.Errorf("a deleted hook: review_execution = %+v, cleanup output:\n%s", j.ReviewExecution, out)
	}
}

// TestReviewHookReadsTheRecordedCLIReply: what the hook reads is what the real
// CLI writes. testdata/claude-print-reply.json is one real reply of
// `claude --print --output-format json --model claude-sonnet-5-5` (Claude Code
// 2.1.322, captured 2026-11-18, the prompt asking for an empty approve),
// replayed byte for byte by a claude that drains its prompt first. Its approve
// lands the push, and the entry carries its four usage counts and the model
// its modelUsage names; the stand-in's envelope names no modelUsage, so every
// other test reads reported_model null.
func TestReviewHookReadsTheRecordedCLIReply(t *testing.T) {
	a, _, _ := reviewArm(t, standin.Script{})
	reply, err := filepath.Abs(filepath.Join("testdata", "claude-print-reply.json"))
	must(t, err)
	must(t, procgroup.WriteProgram(filepath.Join(a.root, "stub", "claude"),
		[]byte("#!/bin/sh\ncat >/dev/null\nexec cat "+procgroup.ShellQuote(reply)+"\n"), 0o755))
	if code, errs := a.push(); code != 0 {
		t.Fatalf("the recorded approve was refused: exit %d\n%s", code, errs)
	}
	r := a.reviewEntries()[0]["reviews"].([]any)[0].(map[string]any)
	want(t, "read from the recorded reply", []any{r["verdict"], r["reported_model"], r["usage"], r["exit"]},
		[]any{"approve", []any{rowModel}, map[string]any{"input_tokens": 2.0, "output_tokens": 18.0,
			"cache_read_input_tokens": 11382.0, "cache_creation_input_tokens": 23274.0}, 0.0})
}

// TestReviewHookHidesTheTokenBeforeTruncating (#A1, a#964 6996847130): the
// hook keeps the last 4000 characters of a reviewer's result and of its
// stderr, so the token is hidden in the whole text before the cut: here both
// end in the token and then filler that puts the cut inside it. No 8-character
// piece of the token reaches what the pusher is told, the cleanup output, or
// any file under the arm's TMPDIR. The positive control: what was kept is the
// cut (4000 characters, the filler at its end).
func TestReviewHookHidesTheTokenBeforeTruncating(t *testing.T) {
	const token = "Zq7Kx9Wm3Pv8Rt2Ny6Bh4Jd5Lf1Gc0Ts" // no 8-character piece of it is anything the harness writes
	tail := strings.Repeat("y", 4000-len(token)/2)
	said := "the token is " + token + tail
	a, _, _ := attemptHolding(t, "agent-ci-review", token, standin.Script{Steps: []standin.Step{{Reply: standin.Text(said).WithStderr(said)}}})
	code, errs := a.push()
	want(t, "unreadable push refused", []any{code != 0, strings.Contains(errs, "Review rejected this push (unreadable)")}, []any{true, true})
	_, out, _ := a.cleanup()
	r := a.reviewEntries()[0]["reviews"].([]any)[0].(map[string]any)
	for _, kept := range []string{r["result"].(string), r["stderr"].(string)} {
		want(t, "kept is the cut", []any{len(kept), strings.HasSuffix(kept, tail)}, []any{4000, true})
	}
	pieces := func(where, text string) {
		for i := 0; i+8 <= len(token); i++ {
			if strings.Contains(text, token[i:i+8]) {
				t.Errorf("%s holds %q, a piece of the token", where, token[i:i+8])
				return
			}
		}
	}
	pieces("what the pusher is told", errs)
	pieces("the cleanup output", out)
	must(t, filepath.WalkDir(a.tmpdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		data, err := os.ReadFile(p)
		if err == nil {
			pieces(p, string(data))
		}
		return err
	}))
}
