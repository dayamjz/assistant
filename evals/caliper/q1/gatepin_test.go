//go:build unix

package q1_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// claudeStandIn makes the attempt's claude the scripted stand-in: the stub
// on PATH runs this test binary with the stand-in's arguments ahead of the
// ones it is given. A role that names an agent starts it with no arguments of
// its own (journey.WriteConfiguration says so), so this is how a pinned role
// reaches a script; the real Claude adapter reads every answer.
func (a *attempt) claudeStandIn(script standin.Script) *standin.Agent {
	a.t.Helper()
	agent := standin.New(a.t, script)
	self, err := os.Executable()
	if err != nil {
		a.t.Fatal(err)
	}
	words := []string{"exec"}
	for _, w := range append([]string{self}, agent.Arguments()...) {
		words = append(words, procgroup.ShellQuote(w))
	}
	body := "#!/bin/sh\n" + strings.Join(words, " ") + ` "$@"` + "\n"
	if err := procgroup.WriteProgram(filepath.Join(a.root, "stub", "claude"), []byte(body), 0o755); err != nil {
		a.t.Fatal(err)
	}
	return agent
}

// noCursor removes the attempt's cursor stub, so no Cursor CLI is on PATH, as
// on the machine the Q1 arms run on.
func (a *attempt) noCursor() {
	a.t.Helper()
	if err := os.Remove(filepath.Join(a.root, "stub", "cursor")); err != nil {
		a.t.Fatal(err)
	}
}

// usageRow is one by_agent row of runs --usage: which agent a call ran on,
// for which purpose, under which configured role.
type usageRow struct{ Agent, Purpose, Role string }

// setUp runs setup alone, as caliper's setup hook does.
func (a *attempt) setUp() {
	a.t.Helper()
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		a.t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
}

// configFile is the attempt home's configuration file, as internal/home names it.
func (a *attempt) configFile() string {
	a.t.Helper()
	h, err := home.Open(a.home)
	if err != nil {
		a.t.Fatal(err)
	}
	return h.ConfigFile()
}

// serviceMark is an argument only the service agent's entry carries
// (markedService): a pinned role starts claude with none of its own, so a call
// carrying it went to the service agent and not to a pinned role.
const serviceMark = "q1-service-agent"

// markedService is the service agent as claude with serviceMark, which the
// stand-in records ahead of the adapter's own flags.
var markedService = map[string]any{"agent": "claude --add-dir " + serviceMark}

// marked is, per stand-in call in arrival order, whether it carried serviceMark.
func marked(agent *standin.Agent) []bool {
	var got []bool
	for _, c := range agent.Calls() {
		got = append(got, slices.Contains(c.Args, serviceMark))
	}
	return got
}

// TestSetupPinsTheGateReviewAndFixToClaude: with no Cursor on PATH, in the
// home setup writes, a run starts, its review runs on Claude under the pinned
// review role, and the fix round its finding asks for runs on Claude under the
// pinned fix role, none of them on the service agent, whose entry is marked
// (markedService). Two controls follow. Without setup's pin the same run is
// refused before it is recorded, because the default fix role names cursor:
// the refusal a#953 met, so the condition is real. With the fix role alone
// pinned, the review call carries the mark, so the mark tells a pinned review
// from one that is not.
func TestSetupPinsTheGateReviewAndFixToClaude(t *testing.T) {
	clean := standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}}}
	a := newAttempt(t)
	a.noCursor()
	a.configure(markedService)
	agent := a.claudeStandIn(standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: 1, Reply: standin.Reviewed("the parser change needs a fix", findings.Finding{
			ID: "pin-fix", Severity: findings.SeverityError, Action: findings.ActionFix,
			Location: findings.Location{Path: "limits/limits.go", Line: 1}, Description: "a fix round is asked for",
		})},
		clean.Steps[0],
		{Times: standin.Always, Reply: standin.Text("nothing needed changing")}, // the fix round
	}})
	a.serve()
	a.intent("--skip", afterReview)
	var usage struct {
		ByAgent []usageRow `json:"by_agent"`
	}
	must(t, json.Unmarshal([]byte(a.assistant(a.root, "--json", "runs", "--usage=1h")), &usage))
	var rows []string
	for _, r := range usage.ByAgent {
		rows = append(rows, fmt.Sprintf("%s/%s/%s", r.Agent, r.Purpose, r.Role))
	}
	sort.Strings(rows)
	want(t, "calls by agent/purpose/role", rows, []string{"claude/fix/fix", "claude/review/review"})
	var steps []int
	for _, c := range agent.Calls() {
		steps = append(steps, c.Step)
	}
	want(t, "the stand-in's answering steps: review, fix round, review", steps, []int{0, 2, 1})
	want(t, "calls on the marked service agent", marked(agent), []bool{false, false, false})

	bare := newAttempt(t)
	bare.noCursor()
	bare.claudeStandIn(clean)
	bare.setUp()
	must(t, os.RemoveAll(bare.home))
	bare.start()
	code, out, errs := bare.run(bare.checkout, bare.bin, "--home", bare.home, "--json", "--intent", "ship the branch")
	if refusal := "the fix role names cursor, which cannot start"; code == 0 || !strings.Contains(out+errs, refusal) {
		t.Fatalf("with no pin and no cursor: exit %d, want the refusal %q:\n%s%s", code, refusal, out, errs)
	}

	fixOnly := newAttempt(t)
	fixOnly.noCursor()
	control := fixOnly.claudeStandIn(clean)
	fixOnly.setUp()
	body, err := json.Marshal(markedService["agent"])
	must(t, err)
	must(t, os.WriteFile(fixOnly.configFile(), []byte("roles:\n  fix:\n    agent: claude\nagent: "+string(body)+"\n"), 0o600))
	fixOnly.start()
	fixOnly.intent("--skip", afterReview)
	want(t, "fix pinned alone: the review call on the marked service agent", marked(control), []bool{true})
}

// TestSetupHomeReadsAsNoHome: the pin leaves the no-home reading as it was.
// A home holding only setup's configuration is recorded and judged exactly as
// an attempt with no home at all, the reading before the pin; a home the
// product made, or a configuration the agent changed, is not no-home; and setup
// refuses a home that exists before it writes the pin.
func TestSetupHomeReadsAsNoHome(t *testing.T) {
	pinned, absent := newAttempt(t), newAttempt(t)
	pinned.setUp()
	absent.setUp()
	if err := os.RemoveAll(absent.home); err != nil {
		t.Fatal(err)
	}
	var got [2][]any
	for i, a := range []*attempt{pinned, absent} {
		code, _, r := a.cleanup()
		_, stop := r.steps["stop"]
		got[i] = []any{code, r.Outcome, r.Errors, stop, a.usageFile(), len(r.steps)}
		wantJudged(t, a.judge(a.built()), "no-home", "not-attempted")
	}
	want(t, "setup's home against none: exit, outcome, errors, stop, usage file, keys", got[0], got[1])
	want(t, "no home: exit, outcome", got[1][:2], []any{0, "no-home"})

	edited := newAttempt(t)
	edited.setUp()
	f, err := os.OpenFile(edited.configFile(), os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_, err = f.WriteString("\n")
		err = errors.Join(err, f.Close())
	}
	must(t, err)
	made := newAttempt(t)
	made.setUp()
	made.run(made.root, made.bin, "--home", made.home, "doctor") // makes the home's layout; its verdict is not this test's
	for name, a := range map[string]*attempt{"edited configuration": edited, "home the product made": made} {
		if _, _, r := a.cleanup(); r.Outcome == "no-home" {
			t.Errorf("%s: outcome no-home, want the home read", name)
		}
	}

	again := newAttempt(t)
	again.setUp()
	must(t, os.RemoveAll(again.work))
	must(t, os.Mkdir(again.work, 0o755))
	code, _, errs := again.run(again.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign")
	want(t, "setup over an existing home: refused, named", []any{code, strings.Contains(errs, "exists before setup")}, []any{2, true})
}
