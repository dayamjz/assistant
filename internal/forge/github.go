package forge

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// DefaultGitHubBinary is the provider command line this adapter runs when no
// other is named.
const DefaultGitHubBinary = "gh"

// ghAuthExitStatus is the exit status this adapter reads as an authentication
// failure, and it is the one signal that tells ReasonUnauthenticated from
// ReasonRejected. gh documents this status; what is checkable here is the
// mapping, not the provider's choice of status.
//
// Reading a status rather than searching a message is deliberate: a message is
// prose that changes between releases and translations, and a search over one
// either misses an authentication failure or claims one that did not happen.
// The cost of the narrow signal is stated rather than implied away: an
// authentication failure the provider reports with a different status arrives
// as ReasonRejected carrying the provider's own message, which is a worse
// label but not a wrong claim.
const ghAuthExitStatus = 4

// prFields are the pull request fields this adapter asks for.
//
// mergeable is here and the provider's merge-state summary is not. That
// summary folds the status of the head's checks into its answer, so reading it
// would blend the two questions PRD section 5 keeps apart, and a caller could
// no longer tell "the branch conflicts" from "the checks have not finished".
const prFields = "number,url,title,state,mergeable,headRefName,headRefOid,baseRefName,isDraft"

// checkFields are the fields a check read asks for. The head commit is asked
// for alongside the checks so the answer says which commit the checks belong
// to, which is what lets a caller recognize a check list left over from an
// earlier head.
const checkFields = "headRefOid,statusCheckRollup"

// GitHub is the provider adapter over the gh command line. It is safe for
// concurrent use: every field is fixed at construction and every call runs its
// own process.
type GitHub struct {
	settings settings
	env      []string
	redact   vcs.Redactor
}

var _ Provider = (*GitHub)(nil)

// NewGitHub returns an adapter that talks to GitHub through gh.
//
// redact is required. PRD section 8 gives credential removal to a redact
// module, which is internal/redact, and this package writes no second
// implementation of it, per P14; every piece of provider text that reaches a
// Refusal passes through what is supplied here. A nil Redactor panics, because
// no state of the world produces one and an adapter that silently reported
// provider text unfiltered is the failure this argument exists to prevent.
//
// The adapter must be given a directory, a repository, or both. Given neither
// it refuses, because a provider left to resolve a repository from this
// process's own working directory would be answering about a repository the
// run never chose. A repository specifier is checked here and refused unless
// it is owner/name, which is what keeps a URL, and so a credential, off every
// command line this package builds.
//
// A refusal names the requirement rather than the value it was given. The
// repository specifier is the one argument a caller could pass a credentialed
// URL as, and not echoing it means that path does not depend on the Redactor
// being handed the right pattern.
func NewGitHub(redact vcs.Redactor, opts ...Option) (*GitHub, error) {
	if redact == nil {
		panic("forge: NewGitHub requires a Redactor")
	}
	s := settings{bin: DefaultGitHubBinary, maxOut: DefaultMaxOutput, grace: DefaultProviderGrace}
	for _, opt := range opts {
		opt(&s)
	}
	if s.repo != "" && !validRepository(s.repo) {
		return nil, &argumentError{
			what:   "repository",
			reason: "must be owner/name, with no scheme, no userinfo, and no path beyond the two segments",
		}
	}
	if s.dir == "" && s.repo == "" {
		return nil, &argumentError{
			what:   "adapter",
			reason: "needs a working directory, a repository, or both, so the repository it addresses is the one the run chose",
		}
	}
	return &GitHub{
		settings: s,
		env:      environment(baseEnvironment(&s), providerEnv),
		redact:   redact,
	}, nil
}

// validRepository reports whether spec is owner/name written in the characters
// GitHub allows in each, and nothing else. A scheme, userinfo, a host, or a
// third path segment all fail it, and so does a segment that would be read as
// an option.
func validRepository(spec string) bool {
	owner, name, ok := strings.Cut(spec, "/")
	if !ok {
		return false
	}
	return validRepositorySegment(owner) && validRepositorySegment(name)
}

func validRepositorySegment(seg string) bool {
	if seg == "" || len(seg) > 100 || strings.HasPrefix(seg, "-") {
		return false
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Find returns the open pull request whose head is the named branch.
//
// It reads the branch's open pull requests and asks for two, so a branch
// carrying more than one is recognized rather than resolved by taking the
// first. That case is ReasonAmbiguous: which of two pull requests a run owns
// is not a question this package may answer by picking.
func (g *GitHub) Find(ctx context.Context, head string) (PullRequest, bool, error) {
	if err := g.checkRef("head branch", head); err != nil {
		return PullRequest{}, false, err
	}
	const op = "find"
	out, err := g.run(ctx, op, "", "pr", "list", "--head="+head, "--state=open", "--limit=2", "--json=number")
	if err != nil {
		return PullRequest{}, false, err
	}
	var list []struct {
		Number int `json:"number"`
	}
	if err := g.decode(op, out, &list); err != nil {
		return PullRequest{}, false, err
	}
	switch {
	case len(list) == 0:
		return PullRequest{}, false, nil
	case len(list) > 1:
		return PullRequest{}, false, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     op,
			Detail: "the branch has more than one open pull request, so which one this run owns is not decidable here",
		}
	case list[0].Number <= 0:
		return PullRequest{}, false, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider reported a pull request with no usable number",
		}
	}
	pr, err := g.Get(ctx, list[0].Number)
	if err != nil {
		return PullRequest{}, false, err
	}
	return pr, true, nil
}

// Get returns where a pull request stands and whether the provider can merge
// it. Whether its checks passed is a separate call and a separate answer.
func (g *GitHub) Get(ctx context.Context, number int) (PullRequest, error) {
	if err := checkNumber(number); err != nil {
		return PullRequest{}, err
	}
	const op = "get"
	out, err := g.run(ctx, op, "", "pr", "view", strconv.Itoa(number), "--json="+prFields)
	if err != nil {
		return PullRequest{}, err
	}
	var raw ghPullRequest
	if err := g.decode(op, out, &raw); err != nil {
		return PullRequest{}, err
	}
	if raw.Number != number {
		// The answer describes a different pull request than the one this call
		// asked about, so nothing in it can be reported as the answer.
		return PullRequest{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "asked about pull request " + strconv.Itoa(number) +
				" and the provider answered about " + strconv.Itoa(raw.Number),
		}
	}
	return raw.pullRequest(), nil
}

// Open opens a pull request. The body travels on standard input rather than on
// the command line, because a body written for a reviewer who was not present
// is the one input a stage can make large and an argument list has a ceiling
// this package neither sets nor can raise.
func (g *GitHub) Open(ctx context.Context, spec OpenSpec) (PullRequest, error) {
	if err := g.checkRef("head branch", spec.Head); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkRef("base branch", spec.Base); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkLine("title", spec.Title); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkBody(spec.Body); err != nil {
		return PullRequest{}, err
	}
	const op = "open"
	args := []string{"pr", "create", "--head=" + spec.Head, "--base=" + spec.Base,
		"--title=" + spec.Title, "--body-file=-"}
	if spec.Draft {
		args = append(args, "--draft")
	}
	if _, err := g.run(ctx, op, spec.Body, args...); err != nil {
		return PullRequest{}, err
	}
	// The create call answers with a URL rather than with the fields this
	// package models, so the pull request is read back through the one parser
	// that produces a PullRequest instead of a second one built out of a URL.
	pr, found, err := g.Find(ctx, spec.Head)
	if err != nil {
		return PullRequest{}, err
	}
	if !found {
		return PullRequest{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider accepted the pull request and then reported no open pull request for the head branch",
		}
	}
	return pr, nil
}

// UpdateBody replaces a pull request's body and returns it as the provider
// then reports it. Nothing else about the pull request is changed.
func (g *GitHub) UpdateBody(ctx context.Context, number int, body string) (PullRequest, error) {
	if err := checkNumber(number); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkBody(body); err != nil {
		return PullRequest{}, err
	}
	if _, err := g.run(ctx, "update-body", body, "pr", "edit", strconv.Itoa(number), "--body-file=-"); err != nil {
		return PullRequest{}, err
	}
	return g.Get(ctx, number)
}

// Checks returns the checks registered on a pull request's head, and the
// commit they were read against.
//
// An empty list is an answer: no check is registered. It is returned as one
// rather than as a failure, and what it means for the run is decided by
// ChecksReport.Evaluate, which does not treat it as green.
func (g *GitHub) Checks(ctx context.Context, number int) (ChecksReport, error) {
	if err := checkNumber(number); err != nil {
		return ChecksReport{}, err
	}
	const op = "checks"
	out, err := g.run(ctx, op, "", "pr", "view", strconv.Itoa(number), "--json="+checkFields)
	if err != nil {
		return ChecksReport{}, err
	}
	var raw struct {
		HeadRefOid string         `json:"headRefOid"`
		Rollup     []ghRollupNode `json:"statusCheckRollup"`
	}
	if err := g.decode(op, out, &raw); err != nil {
		return ChecksReport{}, err
	}
	if raw.HeadRefOid == "" {
		// Provider.Checks promises a report that names the commit its checks
		// were read against, and ChecksReport.HeadCommit is what lets a caller
		// tell a current check list from one left over on an earlier head, so
		// an answer that cannot be placed on a commit is not the answer that
		// was asked for and none of it is reported.
		//
		// This holds that contract rather than covering a shape gh is expected
		// to produce. gh emits every field a --json read names, headRefOid is
		// among the fields checkFields asks for, and it is not a nullable
		// field, so no test in this package states an answer that reaches
		// here: there is none the real command could return. What the guard
		// buys is that the contract holds however this read is later changed,
		// including a change to the field set above.
		return ChecksReport{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider reported checks without naming the commit they were run against",
		}
	}
	report := ChecksReport{HeadCommit: raw.HeadRefOid}
	for _, node := range raw.Rollup {
		report.Runs = append(report.Runs, node.checkRun())
	}
	return report, nil
}

// ghPullRequest is the pull request shape gh puts on the wire for prFields.
type ghPullRequest struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Mergeable   string `json:"mergeable"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	IsDraft     bool   `json:"isDraft"`
}

// pullRequest maps the wire shape onto this package's model. An unrecognized
// state and an unrecognized mergeability each become the unknown value of
// their type rather than being guessed at.
func (p ghPullRequest) pullRequest() PullRequest {
	return PullRequest{
		Number:       p.Number,
		URL:          p.URL,
		Title:        p.Title,
		State:        pullRequestState(p.State),
		Mergeability: mergeability(p.Mergeable),
		Head:         p.HeadRefName,
		HeadCommit:   p.HeadRefOid,
		Base:         p.BaseRefName,
		Draft:        p.IsDraft,
	}
}

func pullRequestState(s string) PullRequestState {
	switch s {
	case "OPEN":
		return PullRequestStateOpen
	case "MERGED":
		return PullRequestStateMerged
	case "CLOSED":
		return PullRequestStateClosed
	default:
		return PullRequestStateUnknown
	}
}

func mergeability(s string) Mergeability {
	switch s {
	case "MERGEABLE":
		return MergeabilityMergeable
	case "CONFLICTING":
		return MergeabilityConflicted
	default:
		return MergeabilityUnknown
	}
}

// ghRollupNode is one entry of the check rollup gh puts on the wire. The
// rollup carries two kinds of entry, distinguished by __typename, and they
// report their state in different fields with different vocabularies, so both
// sets of fields are read here and the kind decides which are meaningful.
type ghRollupNode struct {
	Typename string `json:"__typename"`
	// A check run reports a status and, once it has one, a conclusion.
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	// A commit status reports one state under a context name.
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"targetUrl"`
}

// checkRun maps one rollup entry onto this package's model.
//
// An entry of a kind this package does not know is CheckStateUnrecognized,
// which is not settled, so it keeps the caller waiting. That is the same
// answer an unrecognized state gets and for the same reason: an entry nobody
// here can read is not evidence that anything finished.
func (n ghRollupNode) checkRun() CheckRun {
	switch n.Typename {
	case "CheckRun":
		state, reported := checkRunState(n.Status, n.Conclusion)
		return CheckRun{Name: n.Name, State: state, URL: n.DetailsURL, Reported: reported}
	case "StatusContext":
		state, reported := statusContextState(n.State)
		return CheckRun{Name: n.Context, State: state, URL: n.TargetURL, Reported: reported}
	default:
		name := n.Name
		if name == "" {
			name = n.Context
		}
		return CheckRun{Name: name, State: CheckStateUnrecognized, Reported: n.Typename}
	}
}

// checkRunState maps a check run's status and conclusion onto a CheckState,
// and returns the provider's own words alongside it when the pair was not
// recognized.
//
// A status this adapter reads as not yet completed is pending whatever else
// the entry carries, because no conclusion has arrived to read. Completion
// hands the answer to the conclusion, and a completion carrying a conclusion
// this adapter does not know, including none at all, is unrecognized rather
// than assumed to be one of the ones it does.
func checkRunState(status, conclusion string) (CheckState, string) {
	switch status {
	case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
		return CheckStatePending, ""
	case "COMPLETED":
		// Every conclusion below is settled: the provider has published it and
		// is not going to replace it.
		switch conclusion {
		case "SUCCESS":
			return CheckStateSucceeded, ""
		case "SKIPPED":
			return CheckStateSkipped, ""
		case "NEUTRAL":
			return CheckStateNeutral, ""
		case "CANCELLED":
			return CheckStateCancelled, ""
		case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED", "STALE":
			// Each of these is a finished check that did not pass. They are
			// one state here because a caller does the same thing with all of
			// them: stop waiting, and fix or report.
			return CheckStateFailed, ""
		default:
			return CheckStateUnrecognized, "COMPLETED/" + conclusion
		}
	default:
		return CheckStateUnrecognized, status
	}
}

// statusContextState maps a commit status's state onto a CheckState. A commit
// status has no cancelled state to report, so none is produced here.
func statusContextState(state string) (CheckState, string) {
	switch state {
	case "SUCCESS":
		return CheckStateSucceeded, ""
	case "FAILURE", "ERROR":
		return CheckStateFailed, ""
	case "PENDING", "EXPECTED":
		return CheckStatePending, ""
	default:
		return CheckStateUnrecognized, state
	}
}

// run performs one provider invocation and returns its standard output.
//
// Every failure is a *Refusal, and the reason turns on whether the provider
// answered. Four cases are ReasonUnavailable, because in none of them did it:
// a process that never started, a call the context ended, a process that ended
// without reporting an exit status of its own, and a call whose output was
// still held open at the grace deadline and so may be missing bytes the
// provider wrote. The first three carry no answer at all; the fourth is
// refused rather than reported for the reason DefaultProviderGrace states.
//
// A status the provider did report is its answer. The authentication status is
// ReasonUnauthenticated and any other non-zero status is ReasonRejected, both
// carrying the provider's own message after redaction. Output past the
// configured bound is ReasonOversizeAnswer, and the output is discarded rather
// than truncated.
func (g *GitHub) run(ctx context.Context, op, stdin string, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+1)
	full = append(full, args...)
	if g.settings.repo != "" {
		full = append(full, "--repo="+g.settings.repo)
	}
	res := runProvider(ctx, &g.settings, g.env, stdin, full)
	if res.startErr != nil {
		return nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: "the provider command line could not be run",
			Cause:  res.startErr,
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: "the call ended before the provider answered",
			Cause:  err,
		}
	}
	if res.code < 0 {
		// A process that started and then reported no status of its own was
		// ended by something else, so it never answered the request. Reading
		// that as a rejection would tell a caller the provider refused
		// something it was never in a position to refuse, and the remedy for
		// the two is different.
		return nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: g.redact.Redact(exitDetail(res.code, res.stderr)),
		}
	}
	if res.code != 0 {
		reason := ReasonRejected
		if res.code == ghAuthExitStatus {
			reason = ReasonUnauthenticated
		}
		return nil, &Refusal{
			Reason: reason,
			Op:     op,
			Detail: g.redact.Redact(exitDetail(res.code, res.stderr)),
		}
	}
	if errors.Is(res.waitErr, exec.ErrWaitDelay) {
		// The provider exited, but something still held its output pipes at
		// the grace deadline, so the bytes collected are not known to be all
		// of the ones it wrote.
		return nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: "the provider's output was still held open when the grace period ran out, so what was collected may be short of what it wrote",
			Cause:  res.waitErr,
		}
	}
	if res.over {
		return nil, &Refusal{
			Reason: ReasonOversizeAnswer,
			Op:     op,
			Detail: "the provider printed more than the configured output bound, so its answer was discarded rather than read in part",
		}
	}
	return res.stdout, nil
}

// decode reads a provider answer into v.
//
// The decoder's own message is redacted into Detail, because a decoder quotes
// what it was reading: a numeric literal it could not fit into the field it
// was decoding into arrives inside its message verbatim, and this package's
// own tests demonstrate that. No decoding error is carried as a Cause, so
// Detail is the one route provider text takes into a refusal and every byte of
// it has been through the Redactor.
func (g *GitHub) decode(op string, out []byte, v any) error {
	if err := json.Unmarshal(out, v); err != nil {
		return &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider's answer could not be read: " + g.redact.Redact(err.Error()),
		}
	}
	return nil
}

// checkRef refuses a branch name that could not be passed to the provider as
// written. The refusal happens before the provider is invoked.
func (g *GitHub) checkRef(what, name string) error {
	switch {
	case name == "":
		return g.badArgument(what, name, "is required")
	case strings.HasPrefix(name, "-"):
		return g.badArgument(what, name, "would be read as an option")
	case strings.ContainsAny(name, "\x00\n\r \t"):
		return g.badArgument(what, name, "contains a byte a branch name cannot carry")
	}
	return nil
}

// checkLine refuses a one-line value that is empty or that carries a line
// break, which would make one argument read as several lines wherever it is
// later displayed.
func (g *GitHub) checkLine(what, value string) error {
	switch {
	case value == "":
		return g.badArgument(what, value, "is required")
	case strings.ContainsAny(value, "\x00\n\r"):
		return g.badArgument(what, value, "contains a line break or a null byte")
	}
	return nil
}

// checkBody refuses a body that cannot survive being written to the provider.
// A body may be empty and may be long; it travels on standard input, so it is
// bounded by nothing here.
func (g *GitHub) checkBody(body string) error {
	if strings.ContainsRune(body, '\x00') {
		return g.badArgument("body", "", "contains a null byte")
	}
	return nil
}

// checkNumber refuses a pull request number that identifies nothing.
func checkNumber(number int) error {
	if number <= 0 {
		return &argumentError{
			what:   "pull request number",
			value:  strconv.Itoa(number),
			reason: "must be positive",
		}
	}
	return nil
}

// badArgument builds an argument refusal with the offending value redacted. A
// caller may pass anything as a branch name or a title, including something it
// read out of a remote URL, so the value is filtered on the way into the error
// exactly as provider text is.
func (g *GitHub) badArgument(what, value, reason string) error {
	return &argumentError{what: what, value: g.redact.Redact(value), reason: reason}
}
