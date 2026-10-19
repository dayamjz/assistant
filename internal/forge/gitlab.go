package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/dayamjz/assistant/internal/vcs"
)

// GitLabTokenVariable is the process-environment variable the GitLab adapter
// reads a personal or project access token from. It is not a configuration
// key: a repository document cannot set it, and nothing here reads GITLAB_HOST
// or any other variable that would retarget the request.
const GitLabTokenVariable = "GITLAB_TOKEN"

// gitLabPage is the page size requested of every list. GitLab's own ceiling
// on per_page is 100. A page that comes back full without an empty
// X-Next-Page is refused rather than read as the whole answer.
const gitLabPage = 100

// GitLabOption configures a GitLab adapter. Options are fixed at construction.
type GitLabOption func(*gitLabSettings)

type gitLabSettings struct {
	repo     string
	head     string
	endpoint string
}

// WithGitLabEndpoint names the API base a GitLab adapter sends requests to.
// The default is https://gitlab.com/api/v4.
//
// The only other value accepted is an http URL on a loopback host, which is
// how a test points the adapter at a stand-in. Any other host, any URL with
// userinfo, and any scheme other than https (or http on loopback) is refused
// at construction. The service does not pass this option. An operator
// environment cannot retarget the adapter by setting it either: this function
// is the only seam, and it does not read the environment.
func WithGitLabEndpoint(endpoint string) GitLabOption {
	return func(s *gitLabSettings) {
		if endpoint != "" {
			s.endpoint = strings.TrimRight(endpoint, "/")
		}
	}
}

// WithGitLabRepository names the project to address, as the path GitLab uses
// (group/project, or a nested group/subgroup/project). It is checked at
// construction. Empty leaves the host's Open to supply it.
func WithGitLabRepository(repo string) GitLabOption {
	return func(s *gitLabSettings) { s.repo = repo }
}

// WithGitLabHeadRepository names the project a merge request's source branch
// lives in when that is not the project the adapter addresses. Empty, the
// default, is the addressed project itself.
func WithGitLabHeadRepository(repo string) GitLabOption {
	return func(s *gitLabSettings) { s.head = repo }
}

// GitLab is the provider adapter over GitLab's HTTP API. It is safe for
// concurrent use: every call builds its own request, every configured field
// is fixed at construction, and the confirmation cache is held under mu.
type GitLab struct {
	repo     string
	head     string
	endpoint string
	redact   vcs.Redactor

	mu       sync.Mutex
	upstream glIdentity
	fork     glIdentity
}

// glIdentity is a project the provider has already reported as the specifier
// it was asked about. id is GitLab's numeric project id, which a fork's
// create sends as target_project_id and a fork's Find filters on.
type glIdentity struct {
	id int
	ok bool
}

var _ Provider = (*GitLab)(nil)

// gitlabHTTP does not follow redirects. A 3xx is the provider sending the
// caller somewhere else, and following it would drop the host pin.
var gitlabHTTP = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// NewGitLab returns an adapter that talks to GitLab's HTTP API.
//
// redact is required, on NewGitHub's terms. The adapter must be given a
// repository: a GitLab request addresses a project by the path on its URL,
// and an empty path would be the API root rather than a project the run chose.
func NewGitLab(redact vcs.Redactor, opts ...GitLabOption) (*GitLab, error) {
	if redact == nil {
		panic("forge: NewGitLab requires a Redactor")
	}
	s := gitLabSettings{endpoint: gitLabAPI}
	for _, opt := range opts {
		opt(&s)
	}
	if err := checkGitLabEndpoint(s.endpoint); err != nil {
		return nil, err
	}
	if s.repo != "" && !validGitLabRepository(s.repo) {
		return nil, &argumentError{
			what:   "repository",
			reason: "must be a GitLab project path, with no scheme, no userinfo, and no empty or relative segment",
		}
	}
	if s.head != "" {
		switch {
		case s.repo == "":
			return nil, &argumentError{
				what:   "head repository",
				reason: "needs the repository the merge request is proposed to, because a head repository alone names no base",
			}
		case !validGitLabRepository(s.head):
			return nil, &argumentError{
				what:   "head repository",
				reason: "must be a GitLab project path, with no scheme, no userinfo, and no empty or relative segment",
			}
		case strings.EqualFold(s.head, s.repo):
			return nil, &argumentError{
				what:   "head repository",
				reason: "names the repository the merge request is proposed to, so it is not a fork of it",
			}
		}
	}
	if s.repo == "" {
		return nil, &argumentError{
			what:   "adapter",
			reason: "needs a repository, so the project it addresses is the one the run chose",
		}
	}
	return &GitLab{repo: s.repo, head: s.head, endpoint: s.endpoint, redact: redact}, nil
}

// checkGitLabEndpoint refuses an endpoint that would send a request anywhere
// but gitlab.com, except an http stand-in on a loopback host.
func checkGitLabEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return &argumentError{what: "endpoint", reason: "must be an absolute URL"}
	}
	if u.User != nil {
		return &argumentError{what: "endpoint", reason: "must not carry userinfo"}
	}
	host := u.Hostname()
	switch {
	case strings.EqualFold(host, GitLabHostname) && u.Scheme == "https":
		return nil
	case u.Scheme == "http" && loopback(host):
		return nil
	default:
		return &argumentError{
			what:   "endpoint",
			reason: "must be https://" + GitLabHostname + " or an http stand-in on a loopback host",
		}
	}
}

func loopback(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

// Find returns the open merge request whose source branch is the named
// branch. On a fork it keeps only a merge request whose source project is
// that fork, because any project proposing to the upstream can carry the
// same branch name.
func (g *GitLab) Find(ctx context.Context, head string) (PullRequest, bool, error) {
	if err := g.checkRef("head branch", head); err != nil {
		return PullRequest{}, false, err
	}
	const op = "find"
	query := url.Values{"state": {"opened"}, "source_branch": {head}}
	if g.head == "" {
		query.Set("per_page", "2")
		body, hdr, err := g.do(ctx, op, http.MethodGet, g.projectPath(g.repo)+"/merge_requests", query, nil)
		if err != nil {
			return PullRequest{}, false, err
		}
		list, err := g.decodeMergeRequests(op, body)
		if err != nil {
			return PullRequest{}, false, err
		}
		if pageIncomplete(hdr, len(list), 2) || len(list) > 1 {
			return PullRequest{}, false, &Refusal{
				Reason: ReasonAmbiguous,
				Op:     op,
				Detail: "the branch has more than one open merge request, so which one this run owns is not decidable here",
			}
		}
		if len(list) == 0 {
			return PullRequest{}, false, nil
		}
		if list[0].IID <= 0 {
			return PullRequest{}, false, &Refusal{
				Reason: ReasonMalformed,
				Op:     op,
				Detail: "the provider reported a merge request with no usable number",
			}
		}
		pr, err := g.Get(ctx, list[0].IID)
		if err != nil {
			return PullRequest{}, false, err
		}
		return pr, true, nil
	}
	ident, err := g.project(ctx, op, g.head, "pushes to")
	if err != nil {
		return PullRequest{}, false, err
	}
	query.Set("per_page", strconv.Itoa(gitLabPage))
	body, hdr, err := g.do(ctx, op, http.MethodGet, g.projectPath(g.repo)+"/merge_requests", query, nil)
	if err != nil {
		return PullRequest{}, false, err
	}
	list, err := g.decodeMergeRequests(op, body)
	if err != nil {
		return PullRequest{}, false, err
	}
	if pageIncomplete(hdr, len(list), gitLabPage) {
		return PullRequest{}, false, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     op,
			Detail: "the branch name carries as many open merge requests as one read returns, so whether one of them is this run's could not be settled",
		}
	}
	var mine []int
	for _, entry := range list {
		if entry.SourceProjectID == ident.id {
			mine = append(mine, entry.IID)
		}
	}
	switch {
	case len(mine) == 0:
		return PullRequest{}, false, nil
	case len(mine) > 1:
		return PullRequest{}, false, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     op,
			Detail: "the branch has more than one open merge request from " + g.head + ", so which one this run owns is not decidable here",
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

// Get returns where a merge request stands and whether the provider can merge
// it. Whether its checks passed is a separate call.
func (g *GitLab) Get(ctx context.Context, number int) (PullRequest, error) {
	raw, err := g.fetchMergeRequest(ctx, "get", number)
	if err != nil {
		return PullRequest{}, err
	}
	return raw.pullRequest(), nil
}

// Open opens a merge request. The description travels in the JSON body.
// Nothing is created when the project confirmation refuses.
//
// A fork's merge request is created on the source project, with the upstream's
// numeric id as target_project_id. That is the create request GitLab's API
// documents; it has no owner:branch head the way gh's pr create does.
func (g *GitLab) Open(ctx context.Context, spec OpenSpec) (PullRequest, error) {
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
	upstream, err := g.confirm(ctx, "confirm-repository", g.repo, "addresses", &g.upstream)
	if err != nil {
		return PullRequest{}, err
	}
	source := g.repo
	payload := glCreate{
		SourceBranch: spec.Head,
		TargetBranch: spec.Base,
		Title:        spec.Title,
		Description:  spec.Body,
		Draft:        spec.Draft,
	}
	if g.head != "" {
		if _, err := g.confirm(ctx, "confirm-head-repository", g.head, "pushes to", &g.fork); err != nil {
			return PullRequest{}, err
		}
		source = g.head
		payload.TargetProjectID = upstream.id
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return PullRequest{}, &Refusal{Reason: ReasonUnavailable, Op: "open", Detail: "the merge request could not be encoded"}
	}
	if _, _, err := g.do(ctx, "open", http.MethodPost, g.projectPath(source)+"/merge_requests", nil, body); err != nil {
		return PullRequest{}, err
	}
	pr, found, err := g.Find(ctx, spec.Head)
	if err != nil {
		return PullRequest{}, err
	}
	if !found {
		return PullRequest{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     "open",
			Detail: "the provider accepted the merge request and then reported no open merge request for the source branch",
		}
	}
	return pr, nil
}

// UpdateBody replaces a merge request's description and returns it as the
// provider then reports it. It is a write, so the project is confirmed first.
func (g *GitLab) UpdateBody(ctx context.Context, number int, body string) (PullRequest, error) {
	if err := checkNumber(number); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkBody(body); err != nil {
		return PullRequest{}, err
	}
	if _, err := g.confirm(ctx, "confirm-repository", g.repo, "addresses", &g.upstream); err != nil {
		return PullRequest{}, err
	}
	payload, err := json.Marshal(glUpdate{Description: body})
	if err != nil {
		return PullRequest{}, &Refusal{Reason: ReasonUnavailable, Op: "update-body", Detail: "the description could not be encoded"}
	}
	path := g.projectPath(g.repo) + "/merge_requests/" + strconv.Itoa(number)
	if _, _, err := g.do(ctx, "update-body", http.MethodPut, path, nil, payload); err != nil {
		return PullRequest{}, err
	}
	return g.Get(ctx, number)
}

// Checks returns the pipelines and commit statuses registered on the merge
// request's head, and the commit they were read against.
//
// Pipelines whose sha is not that head are left out: the list endpoint returns
// pipelines for older heads of the same merge request, and one of those is not
// a check on the commit the caller just pushed. A page that comes back full
// is refused rather than judged as a partial list. An empty result is an
// answer, and ChecksReport.Evaluate does not treat it as green.
func (g *GitLab) Checks(ctx context.Context, number int) (ChecksReport, error) {
	mr, err := g.fetchMergeRequest(ctx, "checks", number)
	if err != nil {
		return ChecksReport{}, err
	}
	if mr.SHA == "" {
		return ChecksReport{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     "checks",
			Detail: "the provider reported checks without naming the commit they were run against",
		}
	}
	base := g.projectPath(g.repo) + "/merge_requests/" + strconv.Itoa(number)
	query := url.Values{"per_page": {strconv.Itoa(gitLabPage)}}
	pipeBody, pipeHdr, err := g.do(ctx, "checks", http.MethodGet, base+"/pipelines", query, nil)
	if err != nil {
		return ChecksReport{}, err
	}
	var pipes []glPipeline
	if err := g.decode("checks", pipeBody, &pipes); err != nil {
		return ChecksReport{}, err
	}
	if pageIncomplete(pipeHdr, len(pipes), gitLabPage) {
		return ChecksReport{}, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     "checks",
			Detail: "the pipeline list filled the page, so whether every check was read could not be settled",
		}
	}
	statusBody, statusHdr, err := g.do(ctx, "checks", http.MethodGet,
		g.projectPath(g.repo)+"/repository/commits/"+url.PathEscape(mr.SHA)+"/statuses", query, nil)
	if err != nil {
		return ChecksReport{}, err
	}
	var statuses []glStatus
	if err := g.decode("checks", statusBody, &statuses); err != nil {
		return ChecksReport{}, err
	}
	if pageIncomplete(statusHdr, len(statuses), gitLabPage) {
		return ChecksReport{}, &Refusal{
			Reason: ReasonAmbiguous,
			Op:     "checks",
			Detail: "the commit status list filled the page, so whether every check was read could not be settled",
		}
	}
	report := ChecksReport{HeadCommit: mr.SHA}
	for _, pipe := range pipes {
		if pipe.SHA != mr.SHA {
			continue
		}
		state, reported := gitLabPipelineState(pipe.Status)
		report.Runs = append(report.Runs, CheckRun{
			Name:     "pipeline/" + strconv.Itoa(pipe.ID),
			State:    state,
			URL:      pipe.WebURL,
			Reported: reported,
		})
	}
	for _, status := range statuses {
		if status.SHA != "" && status.SHA != mr.SHA {
			continue
		}
		state, reported := gitLabCommitState(status.Status)
		report.Runs = append(report.Runs, CheckRun{
			Name:     status.Name,
			State:    state,
			URL:      status.TargetURL,
			Reported: reported,
		})
	}
	return report, nil
}

// Merge merges a merge request bound to head. The binding is GitLab's sha
// parameter: the host rejects the merge unless the source branch's HEAD is
// that commit. An empty head is refused before any request is sent. The
// project's own merge method applies; this adapter does not send squash or a
// method, because the merge endpoint has no method flag and the project
// setting is the host's decision.
func (g *GitLab) Merge(ctx context.Context, number int, head string) (PullRequest, error) {
	if err := checkNumber(number); err != nil {
		return PullRequest{}, err
	}
	if err := g.checkLine("head commit", head); err != nil {
		return PullRequest{}, err
	}
	if strings.HasPrefix(head, "-") || strings.ContainsAny(head, " \t/") {
		return PullRequest{}, g.badArgument("head commit", head, "is not a commit identifier")
	}
	if _, err := g.confirm(ctx, "confirm-repository", g.repo, "addresses", &g.upstream); err != nil {
		return PullRequest{}, err
	}
	path := g.projectPath(g.repo) + "/merge_requests/" + strconv.Itoa(number) + "/merge"
	query := url.Values{"sha": {head}}
	if _, _, err := g.do(ctx, "merge", http.MethodPut, path, query, nil); err != nil {
		return PullRequest{}, err
	}
	return g.Get(ctx, number)
}

func (g *GitLab) fetchMergeRequest(ctx context.Context, op string, number int) (glMergeRequest, error) {
	if err := checkNumber(number); err != nil {
		return glMergeRequest{}, err
	}
	path := g.projectPath(g.repo) + "/merge_requests/" + strconv.Itoa(number)
	body, _, err := g.do(ctx, op, http.MethodGet, path, nil, nil)
	if err != nil {
		return glMergeRequest{}, err
	}
	var raw glMergeRequest
	if err := g.decode(op, body, &raw); err != nil {
		return glMergeRequest{}, err
	}
	if raw.IID != number {
		return glMergeRequest{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "asked about merge request " + strconv.Itoa(number) +
				" and the provider answered about " + strconv.Itoa(raw.IID),
		}
	}
	return raw, nil
}

// confirm is confirmRepository for one specifier. The lock is held across the
// request so two writes do not both probe, and only a match is remembered.
func (g *GitLab) confirm(ctx context.Context, op, spec, role string, slot *glIdentity) (glIdentity, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if slot.ok {
		return *slot, nil
	}
	ident, err := g.project(ctx, op, spec, role)
	if err != nil {
		return glIdentity{}, err
	}
	*slot = ident
	return ident, nil
}

// project asks which project spec names and refuses unless the answer's
// path_with_namespace is spec. The comparison ignores letter case, on
// confirmRepository's trade. It is not a lock: a rename between this answer
// and a later write is outside it.
func (g *GitLab) project(ctx context.Context, op, spec, role string) (glIdentity, error) {
	body, _, err := g.do(ctx, op, http.MethodGet, g.projectPath(spec), nil, nil)
	if err != nil {
		return glIdentity{}, err
	}
	var raw glProject
	if err := g.decode(op, body, &raw); err != nil {
		return glIdentity{}, err
	}
	if !strings.EqualFold(raw.PathWithNamespace, spec) {
		return glIdentity{}, &Refusal{
			Reason: ReasonWrongRepository,
			Op:     op,
			Detail: "this run " + role + " " + spec + " and the provider reports that specifier as " +
				g.redact.Redact(raw.PathWithNamespace) + ", so nothing was written",
		}
	}
	if raw.ID <= 0 {
		return glIdentity{}, &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider reported the project without a usable id",
		}
	}
	return glIdentity{id: raw.ID, ok: true}, nil
}

func (g *GitLab) projectPath(spec string) string {
	return "/projects/" + url.PathEscape(spec)
}

func (g *GitLab) do(ctx context.Context, op, method, path string, query url.Values, payload []byte) ([]byte, http.Header, error) {
	full := g.endpoint + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	full = g.redact.Redact(full)
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader([]byte(g.redact.Redact(string(payload))))
	}
	req, err := http.NewRequestWithContext(ctx, method, full, reader)
	if err != nil {
		return nil, nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: "the provider request could not be built",
			Cause:  err,
		}
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := os.Getenv(GitLabTokenVariable); token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := gitlabHTTP.Do(req)
	if err != nil {
		reason := ReasonUnavailable
		detail := "the provider did not answer"
		if ctx.Err() != nil {
			detail = "the call ended before the provider answered"
			err = ctx.Err()
		}
		return nil, nil, &Refusal{Reason: reason, Op: op, Detail: detail, Cause: err}
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, DefaultMaxOutput+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     op,
			Detail: "the provider's answer could not be read",
			Cause:  err,
		}
	}
	if int64(len(buf)) > DefaultMaxOutput {
		return nil, nil, &Refusal{
			Reason: ReasonOversizeAnswer,
			Op:     op,
			Detail: "the provider printed more than the configured output bound, so its answer was discarded rather than read in part",
		}
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return buf, resp.Header, nil
	case http.StatusUnauthorized:
		return nil, nil, &Refusal{
			Reason: ReasonUnauthenticated,
			Op:     op,
			Detail: g.redact.Redact(boundText(string(buf))),
		}
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return nil, nil, &Refusal{
			Reason: ReasonRejected,
			Op:     op,
			Detail: "the provider redirected the request and the redirect was not followed",
		}
	default:
		return nil, nil, &Refusal{
			Reason: ReasonRejected,
			Op:     op,
			Detail: g.redact.Redact(boundText(string(buf))),
		}
	}
}

func (g *GitLab) decode(op string, out []byte, v any) error {
	if err := json.Unmarshal(out, v); err != nil {
		return &Refusal{
			Reason: ReasonMalformed,
			Op:     op,
			Detail: "the provider's answer could not be read: " + g.redact.Redact(err.Error()),
		}
	}
	return nil
}

func (g *GitLab) decodeMergeRequests(op string, out []byte) ([]glMergeRequest, error) {
	var list []glMergeRequest
	if err := g.decode(op, out, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func boundText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxStderr {
		return s
	}
	return s[:maxStderr] + stderrTruncatedMarker
}

func pageIncomplete(h http.Header, n, page int) bool {
	if n > page {
		return true
	}
	if n < page {
		return false
	}
	next, ok := h[http.CanonicalHeaderKey("X-Next-Page")]
	if !ok || len(next) == 0 || strings.TrimSpace(next[0]) != "" {
		return true
	}
	return false
}

func (g *GitLab) checkRef(what, name string) error {
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

func (g *GitLab) checkLine(what, value string) error {
	switch {
	case value == "":
		return g.badArgument(what, value, "is required")
	case strings.ContainsAny(value, "\x00\n\r"):
		return g.badArgument(what, value, "contains a line break or a null byte")
	}
	return nil
}

func (g *GitLab) checkBody(body string) error {
	if strings.ContainsRune(body, '\x00') {
		return g.badArgument("body", "", "contains a null byte")
	}
	return nil
}

func (g *GitLab) badArgument(what, value, reason string) error {
	return &argumentError{what: what, value: g.redact.Redact(value), reason: reason}
}

type glProject struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

type glCreate struct {
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Draft           bool   `json:"draft"`
	TargetProjectID int    `json:"target_project_id,omitempty"`
}

type glUpdate struct {
	Description string `json:"description"`
}

// glMergeRequest is the merge request shape GitLab's API returns. The fields
// this adapter does not read are absent here; encoding/json ignores them on
// the wire, and the stand-in still sends them.
type glMergeRequest struct {
	IID             int    `json:"iid"`
	SourceProjectID int    `json:"source_project_id"`
	Title           string `json:"title"`
	State           string `json:"state"`
	Draft           bool   `json:"draft"`
	WebURL          string `json:"web_url"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	SHA             string `json:"sha"`
	MergeStatus     string `json:"merge_status"`
}

func (m glMergeRequest) pullRequest() PullRequest {
	return PullRequest{
		Number:       m.IID,
		URL:          m.WebURL,
		Title:        m.Title,
		State:        gitLabPullRequestState(m.State),
		Mergeability: gitLabMergeability(m.MergeStatus),
		Head:         m.SourceBranch,
		HeadCommit:   m.SHA,
		Base:         m.TargetBranch,
		Draft:        m.Draft,
	}
}

func gitLabPullRequestState(s string) PullRequestState {
	switch s {
	case "opened":
		return PullRequestStateOpen
	case "merged":
		return PullRequestStateMerged
	case "closed":
		return PullRequestStateClosed
	default:
		// "locked" is a short-lived transitional state in GitLab's own
		// description of the field, and anything else is unrecognized. Neither
		// is reported as open, merged, or closed.
		return PullRequestStateUnknown
	}
}

// gitLabMergeability reads merge_status and not detailed_merge_status.
// GitLab's own documentation says the detailed field accounts for pipelines,
// approvals, drafts, and unresolved discussions, which is the blend this
// package keeps off Mergeability. merge_status is the deprecated coarser
// field: can_be_merged, cannot_be_merged, and the unchecked family.
// cannot_be_merged is read as a conflict because that is the value GitLab's
// documentation says makes has_conflicts true. unchecked, checking, and
// cannot_be_merged_recheck are unknown, so a caller re-reads.
func gitLabMergeability(s string) Mergeability {
	switch s {
	case "can_be_merged":
		return MergeabilityMergeable
	case "cannot_be_merged":
		return MergeabilityConflicted
	default:
		return MergeabilityUnknown
	}
}

type glPipeline struct {
	ID     int    `json:"id"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

type glStatus struct {
	SHA       string `json:"sha"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	TargetURL string `json:"target_url"`
}

// gitLabPipelineState maps a pipeline status onto a CheckState. The words are
// the ones GitLab's pipeline documentation lists. manual is pending: the
// pipeline is waiting for a person to play a job and has not published a
// conclusion. A word outside the list, including the two-L spelling
// "cancelled", is unrecognized and not settled.
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
	case "skipped":
		return CheckStateSkipped, ""
	default:
		return CheckStateUnrecognized, status
	}
}

// gitLabCommitState maps a commit status. GitLab's commit-status API lists
// pending, running, success, failed, and canceled. skipped is accepted
// because a status payload can carry it; any other word waits.
func gitLabCommitState(status string) (CheckState, string) {
	switch status {
	case "pending", "running":
		return CheckStatePending, ""
	case "success":
		return CheckStateSucceeded, ""
	case "failed":
		return CheckStateFailed, ""
	case "canceled":
		return CheckStateCancelled, ""
	case "skipped":
		return CheckStateSkipped, ""
	default:
		return CheckStateUnrecognized, status
	}
}

// GitLabHost is the Host that opens GitLab adapters. It holds what NewGitLab
// takes apart from the repository.
type GitLabHost struct {
	redact vcs.Redactor
	opts   []GitLabOption
}

var _ Host = (*GitLabHost)(nil)

// NewGitLabHost returns the Host that opens GitLab adapters. redact is
// required, on NewGitLab's terms. An option naming a repository or a head
// repository is accepted and then overridden by Open and OpenFork.
func NewGitLabHost(redact vcs.Redactor, opts ...GitLabOption) *GitLabHost {
	return &GitLabHost{redact: redact, opts: append([]GitLabOption(nil), opts...)}
}

// Open returns the GitLab adapter addressing repository.
func (h *GitLabHost) Open(repository string) (Provider, error) {
	if repository == "" {
		return nil, &argumentError{
			what:   "repository",
			reason: "is required, because a provider that resolves one for itself would address a repository this run never named",
		}
	}
	return h.open(repository, "")
}

// OpenFork returns the GitLab adapter addressing repository whose merge
// requests are proposed from fork.
func (h *GitLabHost) OpenFork(repository, fork string) (Provider, error) {
	if repository == "" {
		return nil, &argumentError{
			what:   "repository",
			reason: "is required, because a provider that resolves one for itself would address a repository this run never named",
		}
	}
	if fork == "" {
		return nil, &argumentError{
			what:   "head repository",
			reason: "is required, because a merge request from a fork this run did not name would be proposed from a repository nobody chose",
		}
	}
	return h.open(repository, fork)
}

func (h *GitLabHost) open(repository, head string) (Provider, error) {
	opts := make([]GitLabOption, 0, len(h.opts)+2)
	opts = append(opts, h.opts...)
	opts = append(opts, WithGitLabRepository(repository), WithGitLabHeadRepository(head))
	adapter, err := NewGitLab(h.redact, opts...)
	if err != nil {
		return nil, err
	}
	return adapter, nil
}
