// Package forgetest is a stand-in provider command that answers over the
// wire. A test points a forge.GitHubHost at it, and the real GitHub adapter
// reads what it writes, so a shape the adapter could not read from the real
// gh is a shape a test cannot hand it here either. It is internal/judge's
// judgetest for the code host, and it exists for the same reason that package
// does: a double that states forge.PullRequest values directly can state one
// the adapter's own parser never produces, and a test over such a value holds
// nothing about the product.
//
// Its replies are built from what gh puts on standard output for the field
// sets the adapter asks for. PullRequest is the pr view answer for
// forge.GitHub's pull request fields, with gh's own key order and vocabulary
// and every field the adapter ignores alongside the ones it reads; Repository
// is the repo view answer the adapter's write confirmation asks for;
// MergeMethods is the repo view a merge reads the permitted methods through,
// Merged the answer to an accepted pr merge, and Checks the check rollup for
// a head; Exit is
// a provider that ran, answered, and refused, with the status and message gh
// would leave; and Unauthenticated is the exit status the adapter reads as
// an authentication failure. Raw is the one reply that writes arbitrary
// bytes, and it exists for the unreadable answers a test needs the adapter to
// refuse.
//
// The stand-in is the test binary re-executed, on internal/agents/standin's
// terms, which keeps it free of a build step and identical on every platform
// this module targets. A consuming package calls Main first thing in its
// TestMain, and New refuses with a clear failure when it did not.
//
// What it refuses is what gh refuses and nothing else: a flag the subcommand
// does not define exits with gh's own message, so a vector the real command
// rejects is not one a test can pass over. It is deliberately narrower than
// internal/forge's own private stand-in, which also plants a credential
// tripwire and models a provider the system kills or one that leaves a
// descendant holding its pipes; those are that package's contract tests and
// stay there, so internal/forge's own tests have not moved onto this package.
//
// Its consumers are the packages that need a merged, open or closed pull
// request on the wire without a code host: internal/tasks' cleanup decision
// first. One copy is why it is a package.
package forgetest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/vcs"
)

const (
	// dirVar names the directory holding the script and the recorded
	// calls. Its presence is how a re-executed test binary knows it is the
	// stand-in.
	dirVar      = "FORGETEST_DIR"
	scriptFile  = "script.json"
	callsFile   = "calls.json"
	exitNoReply = 97
)

// Main runs this process as the stand-in provider when it was started as
// one, and returns otherwise. A package whose tests build a host through New
// calls it first thing in TestMain:
//
//	func TestMain(m *testing.M) {
//		forgetest.Main()
//		os.Exit(m.Run())
//	}
//
// It never returns when this process is the stand-in, and it always returns
// when it is not, so calling it unconditionally costs a test binary one
// environment read.
func Main() {
	mainCalled = true
	dir := os.Getenv(dirVar)
	if dir == "" {
		return
	}
	os.Exit(serve(dir, os.Args[1:]))
}

// mainCalled records that Main ran in this process, which is what New
// checks: a binary whose TestMain skipped it would re-execute itself as the
// stand-in and run its tests there instead of answering.
var mainCalled bool

// Response is one scripted answer: the bytes the provider puts on each
// stream and the status it exits with. Build one with the constructors
// below.
type Response struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

// Script maps an operation to the answers the stand-in gives for it, in
// order; the last entry answers every call past the ones listed, so a test
// states a second answer only where the second call differs. The operations
// are the Op constants, which name what the adapter's argument vector asks
// for; a call whose operation has no entry exits with a status the adapter
// reports as a rejection and a message saying so.
type Script map[Op][]Response

// Op names the operation an argument vector performs, read the way gh reads
// it: the subcommand pair, and for a pull request read the field set that
// tells a pull request read from a check read.
type Op string

// The operations the GitHub adapter performs.
const (
	// OpRepository is the repo view the adapter confirms its repository
	// through before a write.
	OpRepository Op = "repository"
	// OpList is the pr list a Find begins with.
	OpList Op = "list"
	// OpView is the pr view a Get, and the second half of a Find, reads.
	OpView Op = "view"
	// OpChecks is the pr view asking for the check rollup.
	OpChecks Op = "checks"
	// OpCreate is pr create.
	OpCreate Op = "create"
	// OpEdit is pr edit.
	OpEdit Op = "edit"
	// OpMerge is pr merge.
	OpMerge Op = "merge"
	// OpMergeMethods is the repo view a merge reads the repository's
	// permitted merge methods through.
	OpMergeMethods Op = "merge-methods"
	// OpOther is anything else, which the adapter never sends.
	OpOther Op = "other"
)

// PullRequestView is what a test says a pull request looks like, in gh's own
// vocabulary: State is OPEN, MERGED or CLOSED, and Mergeable is MERGEABLE,
// CONFLICTING or UNKNOWN. A word outside those sets is written as given, so a
// test can state what the adapter does with a state it does not recognize.
type PullRequestView struct {
	Number     int
	State      string
	Mergeable  string
	Head       string
	HeadCommit string
	Base       string
	Title      string
	URL        string
	Draft      bool
}

// PullRequest is gh's answer to a pr view for the adapter's pull request
// field set, rendered with gh's key order and every field the adapter asks
// for. The adapter's reader is what turns it into a forge.PullRequest.
func PullRequest(v PullRequestView) Response {
	body, _ := json.Marshal(struct {
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		HeadRefOid  string `json:"headRefOid"`
		IsDraft     bool   `json:"isDraft"`
		Mergeable   string `json:"mergeable"`
		Number      int    `json:"number"`
		State       string `json:"state"`
		Title       string `json:"title"`
		URL         string `json:"url"`
	}{v.Base, v.Head, v.HeadCommit, v.Draft, v.Mergeable, v.Number, v.State, v.Title, v.URL})
	return Response{Stdout: string(body) + "\n"}
}

// Repository is gh's answer to the repo view the adapter's write
// confirmation asks for: the specifier the provider resolves the adapter's
// own to.
func Repository(nameWithOwner string) Response {
	body, _ := json.Marshal(struct {
		NameWithOwner string `json:"nameWithOwner"`
	}{nameWithOwner})
	return Response{Stdout: string(body) + "\n"}
}

// MergeMethods is gh's answer to the repo view a merge asks for: which merge
// methods the repository permits.
func MergeMethods(mergeCommit, squash, rebase bool) Response {
	body, _ := json.Marshal(struct {
		MergeCommitAllowed bool `json:"mergeCommitAllowed"`
		RebaseMergeAllowed bool `json:"rebaseMergeAllowed"`
		SquashMergeAllowed bool `json:"squashMergeAllowed"`
	}{mergeCommit, rebase, squash})
	return Response{Stdout: string(body) + "\n"}
}

// Merged is gh's answer to a pr merge the host accepted. gh reports the
// merge on standard error and nothing the adapter reads, which re-reads the
// pull request afterwards.
func Merged(number int) Response {
	return Response{Stderr: fmt.Sprintf("✓ Merged pull request #%d\n", number)}
}

// CheckRunView is one check run as a test states it, in gh's vocabulary:
// Status is COMPLETED, IN_PROGRESS, QUEUED and so on, and Conclusion is
// SUCCESS, FAILURE and so on once completed.
type CheckRunView struct {
	Name       string
	Status     string
	Conclusion string
}

// Checks is gh's answer to the pr view for the check rollup: the head commit
// the checks were read against and one CheckRun entry per run, with every
// field gh emits for one.
func Checks(headCommit string, runs ...CheckRunView) Response {
	type node struct {
		Typename     string `json:"__typename"`
		CompletedAt  string `json:"completedAt"`
		Conclusion   string `json:"conclusion"`
		DetailsURL   string `json:"detailsUrl"`
		Name         string `json:"name"`
		StartedAt    string `json:"startedAt"`
		Status       string `json:"status"`
		WorkflowName string `json:"workflowName"`
	}
	nodes := make([]node, 0, len(runs))
	for _, r := range runs {
		nodes = append(nodes, node{Typename: "CheckRun", CompletedAt: "2026-04-01T10:03:11Z", Conclusion: r.Conclusion,
			DetailsURL: "https://github.com/owner/name/actions/runs/42/job/99", Name: r.Name,
			StartedAt: "2026-04-01T10:01:00Z", Status: r.Status, WorkflowName: "CI"})
	}
	body, _ := json.Marshal(struct {
		HeadRefOid        string `json:"headRefOid"`
		StatusCheckRollup []node `json:"statusCheckRollup"`
	}{headCommit, nodes})
	return Response{Stdout: string(body) + "\n"}
}

// Exit is a provider that ran, answered, and refused: it writes message on
// standard error and exits with status, which must be non-zero. The adapter
// reads it as forge.ReasonRejected carrying the message, except for the
// authentication status, which Unauthenticated states.
func Exit(status int, message string) Response {
	if status == 0 {
		panic("forgetest: Exit takes a non-zero status; a zero status is an answer, not a refusal")
	}
	return Response{Stderr: message + "\n", Exit: status}
}

// Unauthenticated is the exit gh reports for a caller the host does not
// authenticate, which the adapter reads as forge.ReasonUnauthenticated.
func Unauthenticated() Response { return Exit(4, "gh: not logged in") }

// Raw is a reply of exactly these bytes on standard output and a zero exit,
// for the answers a test needs the adapter to refuse as unreadable.
func Raw(stdout string) Response { return Response{Stdout: stdout} }

// Call is one recorded invocation of the stand-in.
type Call struct {
	// Op is the operation the argument vector asked for.
	Op Op `json:"op"`
	// Args is the vector as the adapter sent it.
	Args []string `json:"args"`
	// Stdin is what arrived on standard input, which carries a body.
	Stdin string `json:"stdin"`
}

// Provider is the stand-in, standing behind a forge.GitHubHost. It is safe
// for concurrent use; the stand-in processes serialize their own records.
type Provider struct {
	dir  string
	host *forge.GitHubHost
	mu   sync.Mutex
}

// New writes script where the stand-in reads it and returns a Provider
// whose Host opens GitHub adapters over it. It fails the test when Main was
// not called from the consuming package's TestMain, because a stand-in that
// is never served answers nothing and every read would look like an absent
// gh.
func New(t testing.TB, script Script) *Provider {
	t.Helper()
	if !mainCalled {
		t.Fatalf("forgetest: this test binary's TestMain does not call forgetest.Main, so the stand-in provider cannot be served")
	}
	dir := t.TempDir()
	if script == nil {
		script = Script{}
	}
	blob, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("forgetest: encoding the script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, scriptFile), blob, 0o600); err != nil {
		t.Fatalf("forgetest: writing the script: %v", err)
	}
	p := &Provider{dir: dir}
	p.host = forge.NewGitHubHost(redactor,
		forge.WithBinary(os.Args[0]),
		forge.WithBaseEnvironment([]string{dirVar + "=" + dir}),
		forge.WithDirectory(dir),
	)
	return p
}

// Host is the forge.Host every adapter over this stand-in is opened from.
func (p *Provider) Host() *forge.GitHubHost { return p.host }

// Rescript replaces the script for every call from now on. The stand-in
// reads the script on each call, so a test that learns what the code host
// should say only after it has built the subject, a commit identifier it
// made after New, writes it here.
func (p *Provider) Rescript(t testing.TB, script Script) {
	t.Helper()
	if script == nil {
		script = Script{}
	}
	blob, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("forgetest: encoding the script: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.WriteFile(filepath.Join(p.dir, scriptFile), blob, 0o600); err != nil {
		t.Fatalf("forgetest: writing the script: %v", err)
	}
}

// Calls returns every invocation the stand-in recorded, in arrival order.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return readCalls(p.dir)
}

// redactor is the Redactor every adapter is built with, of the shape
// internal/vcs describes: it replaces the userinfo of a URL carrying a
// scheme. internal/redact is not imported, so a consumer of this package is
// not held to that module's build.
var redactor = vcs.RedactorFunc(func(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if at := strings.IndexAny(rest, "@/ "); at >= 0 && rest[at] == '@' {
			return s[:i+3] + "REDACTED@" + rest[at+1:]
		}
	}
	return s
})

// subcommandFlags is which flags each subcommand the adapter runs accepts,
// written down from gh's own help rather than from what the adapter sends.
var subcommandFlags = map[string]map[string]bool{
	"pr": {
		"--head": true, "--state": true, "--limit": true, "--json": true,
		"--base": true, "--title": true, "--body-file": true, "--draft": true,
		"--repo": true, "--match-head-commit": true, "--merge": true, "--squash": true, "--rebase": true,
	},
	"repo": {"--json": true},
}

// opOf reads the operation off an argument vector the way gh does.
func opOf(args []string) Op {
	if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
		for _, a := range args {
			if strings.HasPrefix(a, "--json=") && strings.Contains(a, "MergeAllowed") {
				return OpMergeMethods
			}
		}
		return OpRepository
	}
	if len(args) < 2 || args[0] != "pr" {
		return OpOther
	}
	switch args[1] {
	case "list":
		return OpList
	case "create":
		return OpCreate
	case "edit":
		return OpEdit
	case "merge":
		return OpMerge
	case "view":
		for _, a := range args {
			if strings.HasPrefix(a, "--json=") && strings.Contains(a, "statusCheckRollup") {
				return OpChecks
			}
		}
		return OpView
	default:
		return OpOther
	}
}

// unsupportedFlag reports the first flag in args the subcommand does not
// accept, which gh refuses before it does anything.
func unsupportedFlag(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	accepted, known := subcommandFlags[args[0]]
	if !known {
		return "", false
	}
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name, _, _ := strings.Cut(a, "=")
		if !accepted[name] {
			return name, true
		}
	}
	return "", false
}

// serve is the stand-in process: it records the call, finds the scripted
// answer, writes it, and exits as it says.
func serve(dir string, args []string) int {
	if flag, bad := unsupportedFlag(args); bad {
		_, _ = fmt.Fprintf(os.Stderr, "unknown flag: %s\n", flag)
		return 1
	}
	op := opOf(args)
	raw, _ := io.ReadAll(os.Stdin)
	stdin := string(raw)
	calls := readCalls(dir)
	seen := 0
	for _, c := range calls {
		if c.Op == op {
			seen++
		}
	}
	calls = append(calls, Call{Op: op, Args: args, Stdin: stdin})
	blob, err := json.Marshal(calls)
	if err != nil {
		return exitNoReply
	}
	if err := os.WriteFile(filepath.Join(dir, callsFile), blob, 0o600); err != nil {
		return exitNoReply
	}
	text, err := os.ReadFile(filepath.Join(dir, scriptFile))
	var script Script
	if err != nil || json.Unmarshal(text, &script) != nil {
		_, _ = fmt.Fprintln(os.Stderr, "stand-in provider: no script")
		return exitNoReply
	}
	answers := script[op]
	if len(answers) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "stand-in provider: no scripted answer for %s\n", op)
		return 1
	}
	answer := answers[len(answers)-1]
	if seen < len(answers) {
		answer = answers[seen]
	}
	_, _ = os.Stdout.WriteString(answer.Stdout)
	_, _ = os.Stderr.WriteString(answer.Stderr)
	return answer.Exit
}

func readCalls(dir string) []Call {
	raw, err := os.ReadFile(filepath.Join(dir, callsFile))
	if err != nil {
		return nil
	}
	var calls []Call
	if json.Unmarshal(raw, &calls) != nil {
		return nil
	}
	return calls
}
