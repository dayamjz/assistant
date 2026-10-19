// Package labtest is a stand-in GitLab API. A test points a forge.GitLabHost
// at its URL, and the real GitLab adapter reads what it writes, so a shape
// the adapter could not read from GitLab's HTTP API is a shape a test cannot
// hand it here either. It is forgetest for GitLab, and it speaks JSON rather
// than a command line because that is what the GitLab adapter sends.
//
// Its replies are built from the shapes GitLab's documented API returns for
// the requests the adapter makes: a project, a merge request, a pipeline list,
// a commit-status list, and the error objects those endpoints answer with.
// Fields the adapter does not read are sent beside the ones it does. Nothing
// here contacts gitlab.com.
package labtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Op names the request the adapter made, read off the method and path.
type Op string

const (
	// OpProject is GET /projects/:id, the read that confirms a project path.
	OpProject Op = "project"
	// OpList is GET /projects/:id/merge_requests.
	OpList Op = "list"
	// OpView is GET one merge request.
	OpView Op = "view"
	// OpCreate is POST a merge request.
	OpCreate Op = "create"
	// OpUpdate is PUT a merge request's description.
	OpUpdate Op = "update"
	// OpPipelines is GET a merge request's pipelines.
	OpPipelines Op = "pipelines"
	// OpStatuses is GET a commit's statuses.
	OpStatuses Op = "statuses"
	// OpMerge is PUT a merge request's merge.
	OpMerge Op = "merge"
	// OpOther is anything else, which the adapter never sends.
	OpOther Op = "other"
)

// Response is one scripted HTTP answer. Status 0 is 200.
type Response struct {
	Status  int
	Body    string
	Headers map[string]string
}

// Script maps an operation to the answers the stand-in gives for it, in
// order. The last entry answers every call past the ones listed.
type Script map[Op][]Response

// Project is GitLab's answer to GET /projects/:id. pathWithNamespace is what
// the adapter holds the specifier to, and id is the numeric id a fork's
// create and Find use. The other fields are ones the documented project
// object carries and this adapter does not read.
func Project(id int, pathWithNamespace string) Response {
	name := pathWithNamespace
	if i := strings.LastIndex(pathWithNamespace, "/"); i >= 0 {
		name = pathWithNamespace[i+1:]
	}
	body, _ := json.Marshal(struct {
		ID                int    `json:"id"`
		Description       string `json:"description"`
		Name              string `json:"name"`
		Path              string `json:"path"`
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		WebURL            string `json:"web_url"`
		Visibility        string `json:"visibility"`
	}{
		ID: id, Name: name, Path: name, PathWithNamespace: pathWithNamespace,
		DefaultBranch: "main",
		HTTPURLToRepo: "https://gitlab.com/" + pathWithNamespace + ".git",
		WebURL:        "https://gitlab.com/" + pathWithNamespace,
		Visibility:    "private",
	})
	return Response{Body: string(body) + "\n"}
}

// MergeRequestView is what a test says a merge request looks like, in
// GitLab's own vocabulary. State is opened, merged, closed, or locked.
// MergeStatus is can_be_merged, cannot_be_merged, unchecked, checking, or
// cannot_be_merged_recheck. A word outside those sets is written as given.
type MergeRequestView struct {
	IID             int
	ProjectID       int
	SourceProjectID int
	TargetProjectID int
	Title           string
	State           string
	Draft           bool
	SourceBranch    string
	TargetBranch    string
	SHA             string
	MergeStatus     string
	WebURL          string
}

// MergeRequest is one merge request object as GitLab returns it, including
// detailed_merge_status, which the adapter does not read.
func MergeRequest(v MergeRequestView) Response {
	if v.WebURL == "" {
		v.WebURL = "https://gitlab.com/group/name/-/merge_requests/" + itoa(v.IID)
	}
	if v.ProjectID == 0 {
		v.ProjectID = 3
	}
	if v.SourceProjectID == 0 {
		v.SourceProjectID = v.ProjectID
	}
	if v.TargetProjectID == 0 {
		v.TargetProjectID = v.ProjectID
	}
	body, _ := json.Marshal(mergeRequestJSON(v))
	return Response{Body: string(body) + "\n"}
}

// MergeRequests is a list of merge requests, the shape of the list endpoint.
func MergeRequests(views ...MergeRequestView) Response {
	rows := make([]mergeRequestBody, 0, len(views))
	for _, v := range views {
		if v.ProjectID == 0 {
			v.ProjectID = 3
		}
		if v.SourceProjectID == 0 {
			v.SourceProjectID = v.ProjectID
		}
		if v.TargetProjectID == 0 {
			v.TargetProjectID = v.ProjectID
		}
		rows = append(rows, mergeRequestJSON(v))
	}
	body, _ := json.Marshal(rows)
	return Response{Body: string(body) + "\n"}
}

type mergeRequestBody struct {
	ID                  int    `json:"id"`
	IID                 int    `json:"iid"`
	ProjectID           int    `json:"project_id"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	State               string `json:"state"`
	Draft               bool   `json:"draft"`
	WorkInProgress      bool   `json:"work_in_progress"`
	SourceBranch        string `json:"source_branch"`
	TargetBranch        string `json:"target_branch"`
	SHA                 string `json:"sha"`
	MergeStatus         string `json:"merge_status"`
	DetailedMergeStatus string `json:"detailed_merge_status"`
	WebURL              string `json:"web_url"`
	SourceProjectID     int    `json:"source_project_id"`
	TargetProjectID     int    `json:"target_project_id"`
	HasConflicts        bool   `json:"has_conflicts"`
}

func mergeRequestJSON(v MergeRequestView) mergeRequestBody {
	return mergeRequestBody{
		ID: v.IID + 100, IID: v.IID, ProjectID: v.ProjectID, Title: v.Title,
		Description: "a body the adapter does not read back from a create",
		State:       v.State, Draft: v.Draft, WorkInProgress: v.Draft,
		SourceBranch: v.SourceBranch, TargetBranch: v.TargetBranch, SHA: v.SHA,
		MergeStatus: v.MergeStatus, DetailedMergeStatus: "mergeable",
		WebURL: v.WebURL, SourceProjectID: v.SourceProjectID, TargetProjectID: v.TargetProjectID,
		HasConflicts: v.MergeStatus == "cannot_be_merged",
	}
}

// PipelineView is one pipeline on the documented short list: id, sha, ref,
// status. web_url is included because the fuller pipeline object carries it
// and the list example in the documentation is abbreviated to four fields.
type PipelineView struct {
	ID     int
	SHA    string
	Ref    string
	Status string
	WebURL string
}

// Pipelines is the merge request pipeline list.
func Pipelines(rows ...PipelineView) Response {
	type pipe struct {
		ID     int    `json:"id"`
		SHA    string `json:"sha"`
		Ref    string `json:"ref"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	}
	out := make([]pipe, 0, len(rows))
	for _, r := range rows {
		out = append(out, pipe{r.ID, r.SHA, r.Ref, r.Status, r.WebURL})
	}
	body, _ := json.Marshal(out)
	return Response{Body: string(body) + "\n"}
}

// StatusView is one commit status.
type StatusView struct {
	ID        int
	SHA       string
	Name      string
	Status    string
	TargetURL string
}

// Statuses is the commit status list.
func Statuses(rows ...StatusView) Response {
	type row struct {
		ID           int    `json:"id"`
		SHA          string `json:"sha"`
		Ref          string `json:"ref"`
		Name         string `json:"name"`
		Status       string `json:"status"`
		TargetURL    string `json:"target_url"`
		Description  string `json:"description"`
		AllowFailure bool   `json:"allow_failure"`
	}
	out := make([]row, 0, len(rows))
	for _, r := range rows {
		out = append(out, row{ID: r.ID, SHA: r.SHA, Ref: "fm/work", Name: r.Name, Status: r.Status,
			TargetURL: r.TargetURL, Description: "the external check"})
	}
	body, _ := json.Marshal(out)
	return Response{Body: string(body) + "\n"}
}

// Unauthorized is the 401 GitLab returns for an invalid token.
func Unauthorized() Response {
	return Response{Status: http.StatusUnauthorized, Body: "{\"message\":\"401 Unauthorized\"}\n"}
}

// Redirect is a 302 with a Location the adapter must not follow.
func Redirect(location string) Response {
	return Response{Status: http.StatusFound, Headers: map[string]string{"Location": location}}
}

// Raw is a 200 whose body is exactly these bytes.
func Raw(body string) Response { return Response{Body: body} }

// Call is one recorded request.
type Call struct {
	Op       Op
	Method   string
	Path     string
	RawQuery string
	Body     string
}

// Provider is the stand-in, standing behind a forge.GitLabHost.
type Provider struct {
	server *httptest.Server
	host   *forge.GitLabHost
	mu     sync.Mutex
	script Script
	calls  []Call
}

// New starts a loopback server speaking script and returns a Provider whose
// Host opens GitLab adapters over it.
func New(t testing.TB, script Script) *Provider {
	t.Helper()
	if script == nil {
		script = Script{}
	}
	p := &Provider{script: script}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	p.host = forge.NewGitLabHost(redactor, forge.WithGitLabEndpoint(p.server.URL+"/api/v4"))
	return p
}

// Host is the forge.Host every adapter over this stand-in is opened from.
func (p *Provider) Host() *forge.GitLabHost { return p.host }

// Rescript replaces the script for every call from now on.
func (p *Provider) Rescript(script Script) {
	if script == nil {
		script = Script{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = script
}

// Calls returns every request the stand-in recorded, in arrival order.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Call, len(p.calls))
	copy(out, p.calls)
	return out
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := r.URL.EscapedPath()
	op := opOf(r.Method, path)
	p.mu.Lock()
	seen := 0
	for _, c := range p.calls {
		if c.Op == op {
			seen++
		}
	}
	p.calls = append(p.calls, Call{Op: op, Method: r.Method, Path: path, RawQuery: r.URL.RawQuery, Body: string(body)})
	answers := p.script[op]
	p.mu.Unlock()
	if op == OpOther || len(answers) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("{\"message\":\"404 Not found\"}\n"))
		return
	}
	answer := answers[len(answers)-1]
	if seen < len(answers) {
		answer = answers[seen]
	}
	for k, v := range answer.Headers {
		w.Header().Set(k, v)
	}
	status := answer.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(answer.Body))
}

func opOf(method, path string) Op {
	const prefix = "/api/v4/projects/"
	if !strings.HasPrefix(path, prefix) {
		return OpOther
	}
	rest := strings.TrimPrefix(path, prefix)
	_, after, found := strings.Cut(rest, "/")
	if !found {
		if method == http.MethodGet {
			return OpProject
		}
		return OpOther
	}
	switch {
	case after == "merge_requests" && method == http.MethodGet:
		return OpList
	case after == "merge_requests" && method == http.MethodPost:
		return OpCreate
	case strings.HasPrefix(after, "merge_requests/") && strings.HasSuffix(after, "/pipelines") && method == http.MethodGet:
		return OpPipelines
	case strings.HasPrefix(after, "merge_requests/") && strings.HasSuffix(after, "/merge") && method == http.MethodPut:
		return OpMerge
	case strings.HasPrefix(after, "merge_requests/") && method == http.MethodGet:
		return OpView
	case strings.HasPrefix(after, "merge_requests/") && method == http.MethodPut:
		return OpUpdate
	case strings.HasPrefix(after, "repository/commits/") && strings.HasSuffix(after, "/statuses") && method == http.MethodGet:
		return OpStatuses
	default:
		return OpOther
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

var redactor = vcs.RedactorFunc(func(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if at := strings.IndexAny(rest, "@/ "); at >= 0 && rest[at] == '@' {
			return s[:i+3] + "REDACTED@" + rest[at+1:]
		}
	}
	return s
})
