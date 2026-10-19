package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/vcs"
)

// GitLabTokenVariable is the environment variable internal/service reads the
// GitLab access token from, once, when it builds its GitLabHost, and the one
// assistant doctor reads to report whether a token is set. It is not a
// configuration key, so no document a repository controls can supply one,
// and this package never reads the environment itself: the token reaches an
// adapter only through WithGitLabToken.
const GitLabTokenVariable = "GITLAB_TOKEN"

// gitLabAPI is the base every GitLab request is built from. It is a constant
// rather than a setting for the reason GH_HOST is pinned for gh: a GitLab
// specifier is a path, and the host a request lands on has to be the
// adapter's and not something the environment chose. WithGitLabEndpoint is
// the one way to change it, and it accepts nothing but this value and a
// loopback stand-in.
const gitLabAPI = "https://" + GitLabHostname + "/api/v4"

// DefaultGitLabTimeout bounds one GitLab request, from sending it to reading
// the last byte of its answer. It is there for the reason
// DefaultProviderGrace is there for gh: a caller's checks_timeout bounds how
// long a run waits on checks only if each read it makes ends, and a request
// to a host that accepts a connection and never answers would otherwise not.
const DefaultGitLabTimeout = 30 * time.Second

// gitLabPage is the page size every list request asks for, which is the
// largest GitLab's API documents for per_page.
const gitLabPage = 100

// gitLabMaxPages bounds how many pages one pipeline read follows. A read that
// is still receiving full pages at the bound is refused rather than judged
// on the part it read.
const gitLabMaxPages = 10

// GitLabOption configures a GitLabHost. Options are fixed for the host's
// lifetime, so every adapter it opens runs under the same configuration.
type GitLabOption func(*gitLabSettings)

type gitLabSettings struct {
	endpoint string
	token    string
	timeout  time.Duration
}

// WithGitLabToken is the access token every request carries, in GitLab's
// PRIVATE-TOKEN header and nowhere else: not in a URL, not in a body, and not
// in a refusal. Empty, which is the default, is no token, and an adapter
// without one refuses every request before sending it.
func WithGitLabToken(token string) GitLabOption {
	return func(s *gitLabSettings) { s.token = token }
}

// WithGitLabEndpoint names the API base requests are sent to. The default is
// https://gitlab.com/api/v4, and the only other value NewGitLabHost accepts is
// an http URL on a loopback address, which is how labtest is reached. The
// service never passes it.
func WithGitLabEndpoint(endpoint string) GitLabOption {
	return func(s *gitLabSettings) {
		if endpoint != "" {
			s.endpoint = strings.TrimRight(endpoint, "/")
		}
	}
}

// WithGitLabTimeout bounds one request. A value of zero or less leaves
// DefaultGitLabTimeout in place, because a zero timeout is net/http's way of
// asking for none.
func WithGitLabTimeout(d time.Duration) GitLabOption {
	return func(s *gitLabSettings) {
		if d > 0 {
			s.timeout = d
		}
	}
}

// GitLabHost is the Host that opens GitLab adapters over GitLab's REST API,
// version 4. It holds what is settled once - the endpoint, the token, the
// redactor and the one HTTP client every adapter it opens sends through - so
// an adapter for one run differs from an adapter for another in the project
// it addresses and nothing else.
//
// It is safe for concurrent use: its fields are fixed at construction and
// Open builds a new adapter rather than sharing one.
type GitLabHost struct {
	endpoint string
	token    string
	redact   vcs.Redactor
	client   *http.Client
}

var _ Host = (*GitLabHost)(nil)

// NewGitLabHost returns the Host that opens GitLab adapters.
//
// redact is required, on NewGitHub's terms, and a nil Redactor panics for the
// reason given there. An endpoint other than the default and a loopback
// stand-in is refused, so no option can send a request, or the token it
// carries, to another host.
//
// The client follows no redirect. A redirect is the host sending the request
// somewhere else, and following one would carry the token to wherever it
// pointed; a redirected request is refused instead, and where it pointed is
// not repeated in the refusal.
func NewGitLabHost(redact vcs.Redactor, opts ...GitLabOption) (*GitLabHost, error) {
	if redact == nil {
		panic("forge: NewGitLabHost requires a Redactor")
	}
	s := gitLabSettings{endpoint: gitLabAPI, timeout: DefaultGitLabTimeout}
	for _, opt := range opts {
		opt(&s)
	}
	if err := checkGitLabEndpoint(s.endpoint); err != nil {
		return nil, err
	}
	return &GitLabHost{
		endpoint: s.endpoint,
		token:    s.token,
		redact:   redact,
		client: &http.Client{
			Timeout: s.timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// checkGitLabEndpoint refuses an endpoint that is neither gitLabAPI nor an
// http URL on a loopback address with no userinfo.
func checkGitLabEndpoint(raw string) error {
	if raw == gitLabAPI {
		return nil
	}
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "http" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return &argumentError{
		what:   "GitLab endpoint",
		reason: "must be " + gitLabAPI + " or an http stand-in on a loopback address, so no request reaches another host",
	}
}

// Probe reports whether this host was given a token, which is the one
// requirement whose absence every request otherwise refuses with. It sends
// nothing, so it does not establish that the token is valid, that it reaches
// any project, or that gitlab.com answers; each of those surfaces as a
// refusal from the call that needs it. A host with no token is a *Refusal
// with ReasonUnauthenticated.
func (h *GitLabHost) Probe() error {
	if h.token == "" {
		return h.noToken("probe")
	}
	return nil
}

// noToken is the refusal every request answers with before anything is sent
// when the host holds no token.
func (h *GitLabHost) noToken(op string) error {
	return &Refusal{
		Reason: ReasonUnauthenticated,
		Op:     op,
		Detail: "no GitLab access token was configured (" + GitLabTokenVariable + " in the service's environment), so nothing was sent",
	}
}

// Open returns the GitLab adapter addressing repository, a specifier as
// GitLabRepository writes it: gitlab.com/ and a project path. Anything else
// is refused, including an empty one, for the reason GitHubHost.Open gives.
// It performs no request.
func (h *GitLabHost) Open(repository string) (Provider, error) {
	path, ok := gitLabPath(repository)
	if !ok {
		return nil, &argumentError{
			what:   "repository",
			reason: "must be " + gitLabPrefix + " and a project path of two or more segments, as GitLabRepository writes it",
		}
	}
	return &gitLab{host: h, path: path}, nil
}

// OpenFork returns the GitLab adapter addressing repository whose merge
// requests are proposed from fork, both as GitLabRepository writes them. Both
// are refused when they are not, and a fork naming repository itself is
// refused, on Host.OpenFork's terms. It performs no request.
func (h *GitLabHost) OpenFork(repository, fork string) (Provider, error) {
	path, ok := gitLabPath(repository)
	if !ok {
		return nil, &argumentError{
			what:   "repository",
			reason: "must be " + gitLabPrefix + " and a project path of two or more segments, as GitLabRepository writes it",
		}
	}
	forkPath, ok := gitLabPath(fork)
	if !ok {
		return nil, &argumentError{
			what:   "head repository",
			reason: "must be " + gitLabPrefix + " and a project path of two or more segments, as GitLabRepository writes it",
		}
	}
	if strings.EqualFold(path, forkPath) {
		return nil, &argumentError{
			what:   "head repository",
			reason: "names the repository the merge request is proposed to, so it is not a fork of it",
		}
	}
	return &gitLab{host: h, path: path, fork: forkPath}, nil
}

// gitLab is the provider adapter for one GitLab project. It is safe for
// concurrent use: every call builds its own request, its configured fields
// are fixed when it is opened, and the confirmations it remembers are held
// under mu.
type gitLab struct {
	host *GitLabHost
	// path is the project the adapter addresses, without the host.
	path string
	// fork is the project a merge request's source branch lives in when it
	// is not path, and empty otherwise.
	fork string

	// mu guards the two confirmations, and is held across the confirming
	// request so two concurrent writes do not both send one.
	mu       sync.Mutex
	upstream gitLabIdentity
	source   gitLabIdentity
}

// gitLabIdentity is a project the host has reported under the path the
// adapter asked about. id is GitLab's numeric project identifier, which a
// merge request names its source and target projects by.
type gitLabIdentity struct {
	id int
	ok bool
}

var _ Provider = (*gitLab)(nil)

// Find returns the open merge request whose source branch is head, on
// Provider.Find's terms.
//
// GitLab lists a project's merge requests by their target, so a source
// branch name alone does not identify one: any fork proposing to this
// project can carry a branch of that name. An adapter that names no fork
// keeps only the merge requests whose source project is their target project,
// which GitLab reports on each entry and which needs no further request; an
// adapter that names a fork keeps only those whose source project is the
// fork, whose identifier it reads first through the same confirmation a write
// makes. More than one kept is ReasonAmbiguous, and so is a list that fills
// one page, because the one this run owns may be on the next.
func (g *gitLab) Find(ctx context.Context, head string) (PullRequest, bool, error) {
	const op = "find"
	if err := g.checkRef("head branch", head); err != nil {
		return PullRequest{}, false, err
	}
	sourceID := 0
	if g.fork != "" {
		ident, err := g.confirm(ctx, "confirm-head-repository", g.fork, "pushes to", &g.source)
		if err != nil {
			return PullRequest{}, false, err
		}
		sourceID = ident.id
	}
	query := url.Values{
		"state":         {"opened"},
		"source_branch": {g.host.redact.Redact(head)},
		"per_page":      {strconv.Itoa(gitLabPage)},
	}
	body, err := g.do(ctx, op, http.MethodGet, g.project(g.path)+"/merge_requests", query, nil)
	if err != nil {
		return PullRequest{}, false, err
	}
	var list []gitLabMergeRequest
	if err := g.decode(op, body, &list); err != nil {
		return PullRequest{}, false, err
	}
	if len(list) >= gitLabPage {
		return PullRequest{}, false, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     op,
			Detail: "the branch name carries as many open merge requests as one read returns, so whether one of them is this run's could not be settled",
		}
	}
	var mine []int
	for _, mr := range list {
		ours := mr.SourceProjectID != 0 && mr.SourceProjectID == mr.TargetProjectID
		if g.fork != "" {
			ours = mr.SourceProjectID == sourceID
		}
		if ours && mr.SourceBranch == head {
			mine = append(mine, mr.IID)
		}
	}
	switch {
	case len(mine) == 0:
		return PullRequest{}, false, nil
	case len(mine) > 1:
		return PullRequest{}, false, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     op,
			Detail: "the branch has more than one open merge request from the project it is pushed to, so which one this run owns is not decidable here",
		}
	case mine[0] <= 0:
		return PullRequest{}, false, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider reported a merge request with no usable number",
		}
	}
	pr, err := g.Get(ctx, mine[0])
	if err != nil {
		return PullRequest{}, false, err
	}
	return pr, true, nil
}

// Get returns where a merge request stands and whether GitLab can merge it.
// Whether its pipelines passed is Checks' question.
func (g *gitLab) Get(ctx context.Context, number int) (PullRequest, error) {
	mr, err := g.mergeRequest(ctx, "get", number)
	if err != nil {
		return PullRequest{}, err
	}
	return mr.pullRequest(), nil
}

// Open opens a merge request and returns it as GitLab reports it in its
// answer to the create.
//
// The create is sent to the project the source branch lives in, which is the
// fork for an adapter that names one, with the addressed project's numeric
// identifier as target_project_id; that is the create request GitLab's API
// documents, and it has no owner:branch head of the kind gh takes. It is
// preceded by the confirmation of every project it names, the addressed one
// first, and nothing is sent when either refuses.
//
// GitLab's create takes no draft field, so a draft is opened the way GitLab
// documents marking one: with "Draft: " in front of the title.
//
// The answer must be a merge request on the branch and base asked for, and
// one that is not is ReasonMalformed. The merge request was created on the
// host by then; the refusal says that its answer cannot be relied on, and
// the next Find reads what is there.
func (g *gitLab) Open(ctx context.Context, spec OpenSpec) (PullRequest, error) {
	const op = "open"
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
	upstream, err := g.confirm(ctx, "confirm-repository", g.path, "addresses", &g.upstream)
	if err != nil {
		return PullRequest{}, err
	}
	source := g.path
	request := gitLabCreate{
		SourceBranch: spec.Head,
		TargetBranch: spec.Base,
		Title:        spec.Title,
		Description:  spec.Body,
	}
	if spec.Draft {
		request.Title = "Draft: " + spec.Title
	}
	if g.fork != "" {
		if _, err := g.confirm(ctx, "confirm-head-repository", g.fork, "pushes to", &g.source); err != nil {
			return PullRequest{}, err
		}
		source = g.fork
		request.TargetProjectID = upstream.id
	}
	body, err := g.do(ctx, op, http.MethodPost, g.project(source)+"/merge_requests", nil, request.redacted(g.host.redact))
	if err != nil {
		return PullRequest{}, err
	}
	var created gitLabMergeRequest
	if err := g.decode(op, body, &created); err != nil {
		return PullRequest{}, err
	}
	if created.IID <= 0 || created.SourceBranch != spec.Head || created.TargetBranch != spec.Base {
		return PullRequest{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider accepted the merge request and answered with one that is not on the branch and base this run asked for",
		}
	}
	return created.pullRequest(), nil
}

// UpdateBody replaces a merge request's description and returns the merge
// request as GitLab reports it in its answer. Nothing else is sent. It is a
// write, so it is preceded by the confirmation of the addressed project.
func (g *gitLab) UpdateBody(ctx context.Context, number int, body string) (PullRequest, error) {
	const op = "update-body"
	if err := checkNumber(number); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkBody(body); err != nil {
		return PullRequest{}, err
	}
	if _, err := g.confirm(ctx, "confirm-repository", g.path, "addresses", &g.upstream); err != nil {
		return PullRequest{}, err
	}
	payload := gitLabUpdate{Description: g.host.redact.Redact(body)}
	answer, err := g.do(ctx, op, http.MethodPut, g.mergeRequestPath(number), nil, payload)
	if err != nil {
		return PullRequest{}, err
	}
	mr, err := g.readMergeRequest(op, number, answer)
	if err != nil {
		return PullRequest{}, err
	}
	return mr.pullRequest(), nil
}

// Checks returns the pipelines on a merge request's head commit, and that
// commit, on Provider.Checks' terms.
//
// The head is the merge request's sha as GitLab reports it, read first. The
// pipelines are the merge request's own pipeline list, followed page by page
// up to gitLabMaxPages, and only the ones GitLab reports on that sha are
// kept: the list carries the pipelines of every earlier head too, and one of
// those is not a check on the commit the run pushed.
//
// Three readings differ from the GitHub adapter's, and each is the strict
// one. A pipeline is the whole of a project's CI for a commit, not one job,
// so a pipeline GitLab reports skipped ran nothing and is not read as a
// check at all, and a head whose only pipelines were skipped has none, which
// ChecksReport.Evaluate does not treat as green; the GitHub adapter's skipped
// check is one job of several and passes. The commit status list is not read,
// because GitLab's documentation of it shows a pipeline's own jobs there,
// each with an allow_failure field, so reading it as checks would block on a
// job the project said may fail. And a pipeline list still returning
// full pages at the bound is ReasonAmbiguous rather than a verdict over the
// pages read.
//
// What that costs is named rather than implied away: a commit status that
// appears in no pipeline on this merge request's list is not read, and
// whether GitLab files every status an external system posts under such a
// pipeline is not verified here. A pipeline a person replaced with a newer
// one on the same commit is still read, so a failed one keeps the verdict
// failed until the head moves. Merged-result pipelines are kept by ref
// rather than by sha, because GitLab runs them on the temporary merge
// commit; an earlier pipeline on the source branch is still dropped when its
// ref is that branch and its sha is not the head.
func (g *gitLab) Checks(ctx context.Context, number int) (ChecksReport, error) {
	const op = "checks"
	mr, err := g.mergeRequest(ctx, op, number)
	if err != nil {
		return ChecksReport{}, err
	}
	if mr.SHA == "" {
		return ChecksReport{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider reported the merge request without naming the commit its head stands at",
		}
	}
	report := ChecksReport{HeadCommit: mr.SHA}
	for page := 1; ; page++ {
		query := url.Values{"per_page": {strconv.Itoa(gitLabPage)}, "page": {strconv.Itoa(page)}}
		body, err := g.do(ctx, op, http.MethodGet, g.mergeRequestPath(number)+"/pipelines", query, nil)
		if err != nil {
			return ChecksReport{}, err
		}
		var pipelines []gitLabPipeline
		if err := g.decode(op, body, &pipelines); err != nil {
			return ChecksReport{}, err
		}
		for _, p := range pipelines {
			if !gitLabPipelineCounts(mr, p) {
				continue
			}
			state, reported := gitLabPipelineState(p.Status)
			report.Runs = append(report.Runs, CheckRun{
				Name:     "pipeline " + strconv.Itoa(p.ID),
				State:    state,
				URL:      p.WebURL,
				Reported: g.host.redact.Redact(reported),
			})
		}
		if len(pipelines) < gitLabPage {
			return report, nil
		}
		if page == gitLabMaxPages {
			return ChecksReport{}, &Refusal{
				Reason: ReasonAmbiguous,
				Op:     op,
				Detail: "the merge request carries more pipelines than " + strconv.Itoa(gitLabMaxPages) +
					" full pages, so whether every pipeline on its head was read could not be settled",
			}
		}
	}
}

// Merge merges a merge request bound to head, on Provider.Merge's terms.
//
// The binding is the merge endpoint's sha parameter, which GitLab documents
// as making the merge fail unless the source branch's head is that commit.
// Nothing else is sent: no merge method, no squash choice, and no request to
// merge later once a pipeline succeeds, so the project's own settings decide
// how the merge is made and the merge is asked for now or not at all. It is a
// write, so it is preceded by the confirmation of the addressed project.
//
// It returns the merge request as GitLab reports it in its answer, and
// whether that reads merged is the caller's question, which
// internal/service's read after an autonomy merge answers.
func (g *gitLab) Merge(ctx context.Context, number int, head string) (PullRequest, error) {
	const op = "merge"
	if err := checkNumber(number); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkLine("head commit", head); err != nil {
		return PullRequest{}, err
	}
	if strings.HasPrefix(head, "-") || strings.ContainsAny(head, " \t") {
		return PullRequest{}, g.badArgument("head commit", head, "is not a commit identifier")
	}
	if _, err := g.confirm(ctx, "confirm-repository", g.path, "addresses", &g.upstream); err != nil {
		return PullRequest{}, err
	}
	answer, err := g.do(ctx, op, http.MethodPut, g.mergeRequestPath(number)+"/merge", nil, gitLabMerge{SHA: head})
	if err != nil {
		return PullRequest{}, err
	}
	mr, err := g.readMergeRequest(op, number, answer)
	if err != nil {
		return PullRequest{}, err
	}
	return mr.pullRequest(), nil
}

// mergeRequest reads one merge request of the addressed project.
func (g *gitLab) mergeRequest(ctx context.Context, op string, number int) (gitLabMergeRequest, error) {
	if err := checkNumber(number); err != nil {
		return gitLabMergeRequest{}, err
	}
	body, err := g.do(ctx, op, http.MethodGet, g.mergeRequestPath(number), nil, nil)
	if err != nil {
		return gitLabMergeRequest{}, err
	}
	return g.readMergeRequest(op, number, body)
}

// readMergeRequest decodes an answer that describes one merge request, and
// refuses one describing a different merge request than number, because
// nothing in it can be reported as the answer to the request that was made.
func (g *gitLab) readMergeRequest(op string, number int, body []byte) (gitLabMergeRequest, error) {
	var mr gitLabMergeRequest
	if err := g.decode(op, body, &mr); err != nil {
		return gitLabMergeRequest{}, err
	}
	if mr.IID != number {
		return gitLabMergeRequest{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "asked about merge request " + strconv.Itoa(number) +
				" and the provider answered about " + strconv.Itoa(mr.IID),
		}
	}
	return mr, nil
}

// confirm is confirmRepository for one GitLab project: before a write that
// names path, and before a read that needs its identifier, GitLab is asked
// which project path names, and the call goes ahead only when the answer's
// path_with_namespace is path, compared ignoring letter case on
// confirmRepository's trade. Its gaps are confirmRepository's: it runs before
// a write and not before every read, it is what GitLab reported when it was
// asked and not a lock against a later rename, and it does not establish that
// a fork is a fork of the addressed project, which GitLab decides when the
// create arrives. Only a confirmation that held is remembered.
func (g *gitLab) confirm(ctx context.Context, op, path, role string, slot *gitLabIdentity) (gitLabIdentity, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if slot.ok {
		return *slot, nil
	}
	body, err := g.do(ctx, op, http.MethodGet, g.project(path), nil, nil)
	if err != nil {
		return gitLabIdentity{}, err
	}
	var raw struct {
		ID                int    `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := g.decode(op, body, &raw); err != nil {
		return gitLabIdentity{}, err
	}
	if !strings.EqualFold(raw.PathWithNamespace, path) {
		return gitLabIdentity{}, &Refusal{
			Reason: ReasonWrongRepository,
			Op:     op,
			Detail: "this run " + role + " " + gitLabPrefix + path + " and the provider reports that specifier as " +
				g.host.redact.Redact(raw.PathWithNamespace) + ", so nothing was written",
		}
	}
	if raw.ID <= 0 {
		return gitLabIdentity{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider reported the project without a usable identifier",
		}
	}
	*slot = gitLabIdentity{id: raw.ID, ok: true}
	return *slot, nil
}

// project is the API path of a project, addressed by its path with every
// slash encoded, which is how GitLab's API takes a path in place of an
// identifier.
func (g *gitLab) project(path string) string {
	return "/projects/" + url.PathEscape(path)
}

// mergeRequestPath is the API path of one of the addressed project's merge
// requests.
func (g *gitLab) mergeRequestPath(number int) string {
	return g.project(g.path) + "/merge_requests/" + strconv.Itoa(number)
}

// do sends one request and returns the body of a successful answer.
//
// Every failure is a *Refusal. A host with no token refuses before anything
// is sent, as ReasonUnauthenticated. A request that never got an answer -
// it could not be sent, the context or the request's timeout ended it, or its
// body could not be read whole - is ReasonUnavailable. An answer over
// DefaultMaxOutput is ReasonOversizeAnswer and is discarded rather than read
// in part. A 401 is ReasonUnauthenticated, a redirect is ReasonRejected
// without where it pointed, and every other status outside 2xx is
// ReasonRejected carrying the status and GitLab's own message, redacted and
// bounded on the terms provider text is bounded everywhere in this package.
//
// Telling 401 from a permission refusal rests on the status GitLab answers
// with and on nothing else, as gh's exit status does for the GitHub adapter;
// a refusal GitLab answers 401 for a reason other than authentication reads
// as ReasonUnauthenticated, with its message in Detail.
//
// payload is encoded as JSON. Its strings are the caller's to redact before
// they reach here, because a field is redacted as the text it is and an
// encoded body is not that text.
func (g *gitLab) do(ctx context.Context, op, method, path string, query url.Values, payload any) ([]byte, error) {
	if g.host.token == "" {
		return nil, g.host.noToken(op)
	}
	target := g.host.endpoint + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, &Refusal{Reason: ReasonUnavailable, Op: op, Detail: "the request could not be encoded", Cause: err}
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, &Refusal{Reason: ReasonUnavailable, Op: op, Detail: "the request could not be built", Cause: err}
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("PRIVATE-TOKEN", g.host.token)
	resp, err := g.host.client.Do(req)
	if err != nil {
		detail := "the provider did not answer"
		if ctx.Err() != nil {
			detail = "the call ended before the provider answered"
			err = ctx.Err()
		}
		return nil, &Refusal{Reason: ReasonUnavailable, Op: op, Detail: detail, Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, DefaultMaxOutput+1))
	if err != nil {
		return nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: "the provider's answer could not be read whole, so none of it was",
			Cause:  err,
		}
	}
	if int64(len(body)) > DefaultMaxOutput {
		return nil, &Refusal{
			Reason: ReasonOversizeAnswer,
			Op:     op,
			Detail: "the provider answered with more than the configured output bound, so its answer was discarded rather than read in part",
		}
	}
	switch status := resp.StatusCode; {
	case status >= 200 && status < 300:
		return body, nil
	case status == http.StatusUnauthorized:
		return nil, &Refusal{Reason: ReasonUnauthenticated, Op: op, Detail: g.statusDetail(status, body)}
	case status >= 300 && status < 400:
		return nil, &Refusal{
			Reason: ReasonRejected,
			Op:     op,
			Detail: "the provider answered HTTP " + strconv.Itoa(status) + ", a redirect, and it was not followed",
		}
	default:
		return nil, &Refusal{Reason: ReasonRejected, Op: op, Detail: g.statusDetail(status, body)}
	}
}

// statusDetail renders a refused answer's status and message, bounded the way
// a provider's standard error is and redacted.
func (g *gitLab) statusDetail(status int, body []byte) string {
	text := &capWriter{limit: maxStderr, truncate: true}
	_, _ = text.Write(bytes.TrimSpace(body))
	detail := "the provider answered HTTP " + strconv.Itoa(status)
	if msg := text.text(); msg != "" {
		detail += ": " + msg
	}
	return g.host.redact.Redact(detail)
}

// decode reads an answer into v, on the GitHub adapter's decode terms: the
// decoder's message is redacted into Detail and nothing is carried as a
// Cause.
func (g *gitLab) decode(op string, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider's answer could not be read: " + g.host.redact.Redact(err.Error()),
		}
	}
	return nil
}

// checkRef refuses a branch name on the GitHub adapter's terms.
func (g *gitLab) checkRef(what, name string) error {
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

// checkLine refuses a one-line value on the GitHub adapter's terms.
func (g *gitLab) checkLine(what, value string) error {
	switch {
	case value == "":
		return g.badArgument(what, value, "is required")
	case strings.ContainsAny(value, "\x00\n\r"):
		return g.badArgument(what, value, "contains a line break or a null byte")
	}
	return nil
}

// checkBody refuses a body on the GitHub adapter's terms.
func (g *gitLab) checkBody(body string) error {
	if strings.ContainsRune(body, '\x00') {
		return g.badArgument("body", "", "contains a null byte")
	}
	return nil
}

// badArgument builds an argument refusal with the offending value redacted.
func (g *gitLab) badArgument(what, value, reason string) error {
	return &argumentError{what: what, value: g.host.redact.Redact(value), reason: reason}
}

// gitLabCreate is the body of a merge request create.
type gitLabCreate struct {
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	TargetProjectID int    `json:"target_project_id,omitempty"`
}

// redacted returns the create with every text field passed through r, which
// is the outbound redaction runExact applies to gh's argument vector and
// standard input, applied to the fields this request carries.
func (c gitLabCreate) redacted(r vcs.Redactor) gitLabCreate {
	c.SourceBranch = r.Redact(c.SourceBranch)
	c.TargetBranch = r.Redact(c.TargetBranch)
	c.Title = r.Redact(c.Title)
	c.Description = r.Redact(c.Description)
	return c
}

// gitLabUpdate is the body of a description update.
type gitLabUpdate struct {
	Description string `json:"description"`
}

// gitLabMerge is the body of a merge, which binds it to a head and asks
// nothing else.
type gitLabMerge struct {
	SHA string `json:"sha"`
}

// gitLabMergeRequest is the part of GitLab's merge request object this
// adapter reads. The fields it does not read are absent here, and the
// decoder skips them on the wire.
type gitLabMergeRequest struct {
	IID             int    `json:"iid"`
	SourceProjectID int    `json:"source_project_id"`
	TargetProjectID int    `json:"target_project_id"`
	Title           string `json:"title"`
	State           string `json:"state"`
	Draft           bool   `json:"draft"`
	WebURL          string `json:"web_url"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	SHA             string `json:"sha"`
	MergeStatus     string `json:"merge_status"`
	HasConflicts    bool   `json:"has_conflicts"`
}

// pullRequest maps the wire shape onto this package's model. An unrecognized
// state and an unrecognized mergeability each become the unknown value of
// their type.
func (m gitLabMergeRequest) pullRequest() PullRequest {
	return PullRequest{
		Number:       m.IID,
		URL:          m.WebURL,
		Title:        m.Title,
		State:        gitLabState(m.State),
		Mergeability: gitLabMergeability(m.MergeStatus, m.HasConflicts),
		Head:         m.SourceBranch,
		HeadCommit:   m.SHA,
		Base:         m.TargetBranch,
		Draft:        m.Draft,
	}
}

// gitLabState maps a merge request's state. GitLab's "locked" and every word
// this adapter does not know are PullRequestStateUnknown, so neither is read
// as open, merged or closed.
func gitLabState(s string) PullRequestState {
	switch s {
	case "opened":
		return PullRequestStateOpen
	case "merged":
		return PullRequestStateMerged
	case "closed":
		return PullRequestStateClosed
	default:
		return PullRequestStateUnknown
	}
}

// gitLabMergeability reads mergeability from two fields of the merge request
// and not from detailed_merge_status. GitLab documents that field as
// accounting for pipelines, approvals, drafts and discussions, which is the
// blend Mergeability keeps out, so it is not asked for.
//
// has_conflicts is the conflict answer when GitLab's own pairing holds:
// it documents has_conflicts as false unless merge_status is
// cannot_be_merged, so can_be_merged with has_conflicts true is unknown
// rather than conflicted. Otherwise true is MergeabilityConflicted
// whatever else the merge request carries. Mergeable is merge_status's
// can_be_merged with no conflict reported. Everything else - GitLab's
// unchecked, checking, cannot_be_merged_recheck, a cannot_be_merged with no
// conflict reported, and a word this adapter does not know - is
// MergeabilityUnknown, so a caller re-reads rather than concluding.
//
// merge_status is a field GitLab documents as deprecated in favour of the
// detailed one. If it stops being sent it decodes as empty, which is unknown,
// so a merge request is never read mergeable on its absence.
func gitLabMergeability(status string, conflicts bool) Mergeability {
	switch {
	case status == "can_be_merged" && conflicts:
		return MergeabilityUnknown
	case conflicts:
		return MergeabilityConflicted
	case status == "can_be_merged":
		return MergeabilityMergeable
	default:
		return MergeabilityUnknown
	}
}

// gitLabPipelineCounts reports whether a pipeline from the merge request's
// pipeline list is a check this head owes. Skipped pipelines ran nothing.
// A pipeline on the source branch's current head counts; an earlier commit
// on that branch in the same list does not. Merged-result pipelines run on
// a temporary merge commit GitLab names under refs/merge-requests/, not on
// the head sha, and count when their ref is not the source branch.
func gitLabPipelineCounts(mr gitLabMergeRequest, p gitLabPipeline) bool {
	if p.Status == "skipped" {
		return false
	}
	if p.SHA == mr.SHA {
		return true
	}
	if p.Ref == mr.SourceBranch {
		return false
	}
	return true
}

// gitLabPipeline is the part of a pipeline list entry this adapter reads.
type gitLabPipeline struct {
	ID     int    `json:"id"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// gitLabPipelineState maps a pipeline's status onto a CheckState, and returns
// GitLab's own word beside it when the word was not recognized.
//
// The words mapped are ones GitLab's pipeline documentation lists, and a word
// it lists that is not mapped here is treated as one it does not. A pipeline
// that has not concluded - created, waiting_for_resource, preparing,
// pending, running, scheduled - is pending, and so is manual: the pipeline
// is waiting for a person to start a job and has published no conclusion.
// GitLab spells its cancelled status "canceled", and that is
// CheckStateCancelled, settled and not a pass; any other spelling is a word
// this adapter does not know. skipped never reaches here, because Checks
// does not read a skipped pipeline as a check. Every other word is
// CheckStateUnrecognized, which is not settled, so the caller keeps waiting.
func gitLabPipelineState(status string) (CheckState, string) {
	switch status {
	case "created", "waiting_for_resource", "preparing", "pending", "running", "scheduled", "manual":
		return CheckStatePending, ""
	case "success":
		return CheckStateSucceeded, ""
	case "failed":
		return CheckStateFailed, ""
	case "canceled":
		return CheckStateCancelled, ""
	default:
		return CheckStateUnrecognized, status
	}
}
