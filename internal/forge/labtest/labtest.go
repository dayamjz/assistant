// Package labtest is a stand-in GitLab REST API that answers over the wire. A
// test builds a forge.GitLabHost on it, and the real GitLab adapter sends its
// requests here and reads what comes back, so a shape the adapter could not
// read from GitLab is a shape a test cannot hand it here either. It is
// forgetest for the GitLab adapter, on internal/judge/judgetest's terms, and
// it speaks HTTP rather than a command line because HTTP is what that
// adapter sends.
//
// Its answers are built from the objects GitLab's API documentation gives
// for the requests the adapter makes - a project, a merge request, a list of
// merge requests, a merge request's pipeline list - and from the error object
// those endpoints answer with, {"message": ...}. Each is rendered with fields
// the adapter does not read beside the ones it does, so a test cannot pass
// over a reader that depended on an answer being tidier than GitLab's. Raw is
// the one answer that writes arbitrary bytes, and it exists for the
// unreadable answers a test needs the adapter to refuse.
//
// What it refuses is what GitLab documents refusing and nothing else: a
// request with no PRIVATE-TOKEN header is answered 404, because every project
// it stands in for is private and that is how GitLab documents answering an
// unauthenticated request for one; a create missing a field GitLab requires
// is answered 400 naming it; a request body that is not JSON is answered
// 400; and a path it does not route is answered 404, which is also what a
// project path sent with its slashes unencoded reaches. It does not model
// GitLab's permission rules, its pagination headers, or anything it does not
// answer; a test that needs one of those states it with Error.
//
// Its consumers are internal/forge's own tests, including the conformance
// suite that runs the same assertions against both adapters, and
// internal/service's test that a gitlab.com repository's reads reach the
// GitLab adapter. One copy is why it is a package. Nothing here contacts
// gitlab.com.
package labtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Token is the access token every host New builds carries. A test that needs
// a host with no token builds one with forge.NewGitLabHost itself.
const Token = "glpat-labtest-token"

// Op names the request the adapter made, read off its method and its path
// the way GitLab routes them.
type Op string

// The requests the GitLab adapter makes.
const (
	// OpProject is GET /projects/:id, which the adapter's confirmation reads.
	OpProject Op = "project"
	// OpList is GET /projects/:id/merge_requests, which a Find begins with.
	OpList Op = "list"
	// OpView is GET /projects/:id/merge_requests/:iid.
	OpView Op = "view"
	// OpCreate is POST /projects/:id/merge_requests.
	OpCreate Op = "create"
	// OpUpdate is PUT /projects/:id/merge_requests/:iid.
	OpUpdate Op = "update"
	// OpPipelines is GET /projects/:id/merge_requests/:iid/pipelines.
	OpPipelines Op = "pipelines"
	// OpMerge is PUT /projects/:id/merge_requests/:iid/merge.
	OpMerge Op = "merge"
	// OpOther is anything else, which the adapter never sends and which is
	// answered 404.
	OpOther Op = "other"
)

// Response is one scripted answer: a status and a body. Build one with the
// constructors below.
type Response struct {
	status   int
	body     string
	location string
}

// Script maps a request to the answers the stand-in gives for it, in order;
// the last entry answers every request past the ones listed. A request whose
// operation has no entry is answered 404, as GitLab answers a project or a
// merge request it does not have.
type Script map[Op][]Response

// Project is GitLab's project object for GET /projects/:id. id is the numeric
// identifier a merge request names its projects by, and pathWithNamespace is
// the path GitLab reports the project under, which the adapter's confirmation
// compares with the one it asked about.
func Project(id int, pathWithNamespace string) Response {
	name := pathWithNamespace
	if i := strings.LastIndex(pathWithNamespace, "/"); i >= 0 {
		name = pathWithNamespace[i+1:]
	}
	return jsonResponse(http.StatusOK, struct {
		ID                int    `json:"id"`
		Description       string `json:"description"`
		Name              string `json:"name"`
		NameWithNamespace string `json:"name_with_namespace"`
		Path              string `json:"path"`
		PathWithNamespace string `json:"path_with_namespace"`
		CreatedAt         string `json:"created_at"`
		DefaultBranch     string `json:"default_branch"`
		SSHURLToRepo      string `json:"ssh_url_to_repo"`
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		WebURL            string `json:"web_url"`
		Visibility        string `json:"visibility"`
	}{
		ID: id, Name: name, NameWithNamespace: strings.ReplaceAll(pathWithNamespace, "/", " / "),
		Path: name, PathWithNamespace: pathWithNamespace, CreatedAt: "2026-01-12T09:14:00.000Z",
		DefaultBranch: "main",
		SSHURLToRepo:  "git@gitlab.com:" + pathWithNamespace + ".git",
		HTTPURLToRepo: "https://gitlab.com/" + pathWithNamespace + ".git",
		WebURL:        "https://gitlab.com/" + pathWithNamespace,
		Visibility:    "private",
	})
}

// MergeRequestView is what a test says a merge request looks like, in
// GitLab's own vocabulary. State is opened, merged, closed or locked;
// MergeStatus is can_be_merged, cannot_be_merged, unchecked, checking or
// cannot_be_merged_recheck; HasConflicts is GitLab's has_conflicts. A word
// outside those sets is written as given, so a test can state what the
// adapter does with one it does not recognize.
//
// ProjectID is the project the merge request belongs to, which is its target.
// SourceProjectID is where its source branch lives and is ProjectID when left
// zero, which is a merge request from a branch of the project itself.
type MergeRequestView struct {
	IID             int
	ProjectID       int
	SourceProjectID int
	Title           string
	State           string
	Draft           bool
	SourceBranch    string
	TargetBranch    string
	SHA             string
	MergeStatus     string
	HasConflicts    bool
	WebURL          string
	// HeadPipeline is GitLab's head_pipeline for the merge request, when the
	// test needs one; nil omits the field.
	HeadPipeline *PipelineView
}

// MergeRequest is GitLab's merge request object, the answer to a read, a
// create, a description update and an accepted merge alike.
func MergeRequest(v MergeRequestView) Response {
	return jsonResponse(http.StatusOK, mergeRequestOf(v))
}

// Created is MergeRequest with the 201 GitLab answers a create with.
func Created(v MergeRequestView) Response {
	return jsonResponse(http.StatusCreated, mergeRequestOf(v))
}

// MergeRequests is the merge request list endpoint's answer.
func MergeRequests(views ...MergeRequestView) Response {
	rows := make([]mergeRequestBody, 0, len(views))
	for _, v := range views {
		rows = append(rows, mergeRequestOf(v))
	}
	return jsonResponse(http.StatusOK, rows)
}

// mergeRequestBody is GitLab's merge request object as its API documentation
// shows it, with the fields that would need a user or a milestone left as
// GitLab writes them for a merge request that has neither.
type mergeRequestBody struct {
	ID                        int               `json:"id"`
	IID                       int               `json:"iid"`
	ProjectID                 int               `json:"project_id"`
	Title                     string            `json:"title"`
	Description               string            `json:"description"`
	State                     string            `json:"state"`
	CreatedAt                 string            `json:"created_at"`
	UpdatedAt                 string            `json:"updated_at"`
	MergedBy                  *string           `json:"merged_by"`
	MergedAt                  *string           `json:"merged_at"`
	TargetBranch              string            `json:"target_branch"`
	SourceBranch              string            `json:"source_branch"`
	SourceProjectID           int               `json:"source_project_id"`
	TargetProjectID           int               `json:"target_project_id"`
	Labels                    []string          `json:"labels"`
	Draft                     bool              `json:"draft"`
	WorkInProgress            bool              `json:"work_in_progress"`
	MergeWhenPipelineSucceeds bool              `json:"merge_when_pipeline_succeeds"`
	MergeStatus               string            `json:"merge_status"`
	DetailedMergeStatus       string            `json:"detailed_merge_status"`
	SHA                       string            `json:"sha"`
	MergeCommitSHA            *string           `json:"merge_commit_sha"`
	Reference                 string            `json:"reference"`
	WebURL                    string            `json:"web_url"`
	Squash                    bool              `json:"squash"`
	HasConflicts              bool              `json:"has_conflicts"`
	BlockingDiscussions       bool              `json:"blocking_discussions_resolved"`
	HeadPipeline              *headPipelineBody `json:"head_pipeline"`
}

type headPipelineBody struct {
	ID        int    `json:"id"`
	IID       int    `json:"iid"`
	ProjectID int    `json:"project_id"`
	SHA       string `json:"sha"`
	Ref       string `json:"ref"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	WebURL    string `json:"web_url"`
}

func mergeRequestOf(v MergeRequestView) mergeRequestBody {
	if v.ProjectID == 0 {
		v.ProjectID = 3
	}
	if v.SourceProjectID == 0 {
		v.SourceProjectID = v.ProjectID
	}
	if v.WebURL == "" {
		v.WebURL = "https://gitlab.com/group/name/-/merge_requests/" + strconv.Itoa(v.IID)
	}
	detailed := "mergeable"
	switch {
	case v.HasConflicts:
		detailed = "conflict"
	case v.MergeStatus != "can_be_merged":
		detailed = "checking"
	}
	var mergeCommit *string
	if v.State == "merged" {
		sha := strings.Repeat("e", 40)
		mergeCommit = &sha
	}
	body := mergeRequestBody{
		ID: v.IID + 1000, IID: v.IID, ProjectID: v.ProjectID, Title: v.Title,
		Description: "a description the adapter does not read back",
		State:       v.State, CreatedAt: "2026-04-01T10:00:00.000Z", UpdatedAt: "2026-04-01T10:05:00.000Z",
		TargetBranch: v.TargetBranch, SourceBranch: v.SourceBranch,
		SourceProjectID: v.SourceProjectID, TargetProjectID: v.ProjectID,
		Labels: []string{}, Draft: v.Draft, WorkInProgress: v.Draft,
		MergeStatus: v.MergeStatus, DetailedMergeStatus: detailed, SHA: v.SHA,
		MergeCommitSHA: mergeCommit, Reference: "!" + strconv.Itoa(v.IID), WebURL: v.WebURL,
		HasConflicts: v.HasConflicts, BlockingDiscussions: true,
	}
	if v.HeadPipeline != nil {
		p := *v.HeadPipeline
		body.HeadPipeline = &headPipelineBody{
			ID: p.ID, IID: 1, ProjectID: v.ProjectID, SHA: p.SHA, Ref: p.Ref, Status: p.Status,
			Source: "merge_request_event",
			WebURL: "https://gitlab.com/group/name/-/pipelines/" + strconv.Itoa(p.ID),
		}
	}
	return body
}

// PipelineView is one pipeline in a merge request's pipeline list, in
// GitLab's vocabulary: Status is created, waiting_for_resource, preparing,
// pending, running, success, failed, canceled, skipped, manual or scheduled,
// and a word outside that set is written as given.
type PipelineView struct {
	ID     int
	SHA    string
	Ref    string
	Status string
}

// Pipelines is one page of a merge request's pipeline list, with the fields
// GitLab sends for each entry. A test that needs a second page scripts a
// second answer.
func Pipelines(rows ...PipelineView) Response {
	type pipeline struct {
		ID        int    `json:"id"`
		IID       int    `json:"iid"`
		ProjectID int    `json:"project_id"`
		SHA       string `json:"sha"`
		Ref       string `json:"ref"`
		Status    string `json:"status"`
		Source    string `json:"source"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		WebURL    string `json:"web_url"`
	}
	out := make([]pipeline, 0, len(rows))
	for i, r := range rows {
		out = append(out, pipeline{
			ID: r.ID, IID: i + 1, ProjectID: 3, SHA: r.SHA, Ref: r.Ref, Status: r.Status,
			Source: "merge_request_event", CreatedAt: "2026-04-01T10:01:00.000Z", UpdatedAt: "2026-04-01T10:04:00.000Z",
			WebURL: "https://gitlab.com/group/name/-/pipelines/" + strconv.Itoa(r.ID),
		})
	}
	return jsonResponse(http.StatusOK, out)
}

// Error is GitLab's error object, {"message": message}, under status, which
// must not be a success. It is how GitLab answers a merge it refuses (405,
// 409 or 422 among those its documentation lists), a project or merge
// request it does not have (404), and a caller it does not permit (403).
func Error(status int, message string) Response {
	if status < 400 {
		panic("labtest: Error takes a status of 400 or more; anything else is not an error answer")
	}
	return jsonResponse(status, struct {
		Message string `json:"message"`
	}{message})
}

// Unauthorized is the 401 GitLab answers a request whose token it does not
// accept with.
func Unauthorized() Response { return Error(http.StatusUnauthorized, "401 Unauthorized") }

// Redirect is a 302 pointing at location, which the adapter must neither
// follow nor repeat.
func Redirect(location string) Response {
	return Response{status: http.StatusFound, location: location}
}

// Raw is a 200 whose body is exactly these bytes.
func Raw(body string) Response { return Response{status: http.StatusOK, body: body} }

func jsonResponse(status int, v any) Response {
	body, err := json.Marshal(v)
	if err != nil {
		panic("labtest: encoding an answer: " + err.Error())
	}
	return Response{status: status, body: string(body)}
}

// Call is one request the stand-in received.
type Call struct {
	// Op is the request as GitLab would route it.
	Op Op
	// Method is the HTTP method.
	Method string
	// Project is the project path the request named, decoded, and empty for
	// a request that named none.
	Project string
	// Path is the request's path as it arrived, still encoded.
	Path string
	// Query is the request's query.
	Query url.Values
	// Body is the request body as it arrived.
	Body string
	// Token is the PRIVATE-TOKEN header as it arrived.
	Token string
}

// Provider is the stand-in, standing behind a forge.GitLabHost. It is safe
// for concurrent use.
type Provider struct {
	server *httptest.Server
	host   *forge.GitLabHost
	mu     sync.Mutex
	script Script
	calls  []Call
}

// New starts the stand-in on a loopback address, answering as script says,
// and returns a Provider whose Host opens GitLab adapters on it with Token.
// The server is closed when the test ends.
func New(t testing.TB, script Script) *Provider {
	t.Helper()
	p := &Provider{script: script}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	host, err := forge.NewGitLabHost(redactor, forge.WithGitLabEndpoint(p.Endpoint()), forge.WithGitLabToken(Token))
	if err != nil {
		t.Fatalf("labtest: building the host: %v", err)
	}
	p.host = host
	return p
}

// Endpoint is the API base the stand-in answers on, which a test building
// its own host passes to forge.WithGitLabEndpoint.
func (p *Provider) Endpoint() string { return p.server.URL + "/api/v4" }

// Host is the forge.GitLabHost every adapter over this stand-in is opened
// from.
func (p *Provider) Host() *forge.GitLabHost { return p.host }

// Rescript replaces the script for every request from now on.
func (p *Provider) Rescript(script Script) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = script
}

// Calls returns every request the stand-in received, in arrival order.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// CallsFor returns the requests of one operation, in arrival order.
func (p *Provider) CallsFor(op Op) []Call {
	var out []Call
	for _, c := range p.Calls() {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	op, project := route(r.Method, r.URL.EscapedPath())
	call := Call{
		Op: op, Method: r.Method, Project: project, Path: r.URL.EscapedPath(),
		Query: r.URL.Query(), Body: string(raw), Token: r.Header.Get("PRIVATE-TOKEN"),
	}
	p.mu.Lock()
	seen := 0
	for _, c := range p.calls {
		if c.Op == op {
			seen++
		}
	}
	p.calls = append(p.calls, call)
	answers := p.script[op]
	p.mu.Unlock()

	switch {
	case call.Token == "":
		write(w, Error(http.StatusNotFound, "404 Project Not Found"))
		return
	case op == OpOther:
		write(w, Error(http.StatusNotFound, "404 Not Found"))
		return
	}
	if len(raw) > 0 {
		var fields map[string]any
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || json.Unmarshal(raw, &fields) != nil {
			write(w, jsonResponse(http.StatusBadRequest, struct {
				Error string `json:"error"`
			}{"the request body could not be read as JSON"}))
			return
		}
		if op == OpCreate {
			for _, required := range []string{"source_branch", "target_branch", "title"} {
				if s, _ := fields[required].(string); s == "" {
					write(w, jsonResponse(http.StatusBadRequest, struct {
						Error string `json:"error"`
					}{required + " is missing"}))
					return
				}
			}
		}
	}
	if len(answers) == 0 {
		write(w, Error(http.StatusNotFound, "404 Not Found"))
		return
	}
	answer := answers[len(answers)-1]
	if seen < len(answers) {
		answer = answers[seen]
	}
	write(w, answer)
}

func write(w http.ResponseWriter, answer Response) {
	if answer.location != "" {
		w.Header().Set("Location", answer.location)
	}
	if answer.body != "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(answer.status)
	_, _ = io.WriteString(w, answer.body)
}

// route reads the operation and the project path off a request the way
// GitLab routes it: a project is one path segment, its slashes encoded, so a
// path whose slashes arrived unencoded routes nowhere.
func route(method, path string) (Op, string) {
	rest, found := strings.CutPrefix(path, "/api/v4/projects/")
	if !found {
		return OpOther, ""
	}
	encoded, tail, _ := strings.Cut(rest, "/")
	project, err := url.PathUnescape(encoded)
	if err != nil || project == "" {
		return OpOther, ""
	}
	segments := []string{}
	if tail != "" {
		segments = strings.Split(tail, "/")
	}
	isNumber := func(s string) bool { n, err := strconv.Atoi(s); return err == nil && n > 0 }
	switch {
	case len(segments) == 0 && method == http.MethodGet:
		return OpProject, project
	case len(segments) == 1 && segments[0] == "merge_requests" && method == http.MethodGet:
		return OpList, project
	case len(segments) == 1 && segments[0] == "merge_requests" && method == http.MethodPost:
		return OpCreate, project
	case len(segments) == 2 && segments[0] == "merge_requests" && isNumber(segments[1]) && method == http.MethodGet:
		return OpView, project
	case len(segments) == 2 && segments[0] == "merge_requests" && isNumber(segments[1]) && method == http.MethodPut:
		return OpUpdate, project
	case len(segments) == 3 && segments[0] == "merge_requests" && isNumber(segments[1]) && segments[2] == "pipelines" && method == http.MethodGet:
		return OpPipelines, project
	case len(segments) == 3 && segments[0] == "merge_requests" && isNumber(segments[1]) && segments[2] == "merge" && method == http.MethodPut:
		return OpMerge, project
	default:
		return OpOther, project
	}
}

// redactor is the Redactor every host New builds is given, of the shape
// internal/vcs describes: it replaces the userinfo of a URL carrying a
// scheme. internal/redact is not imported, on forgetest's terms.
var redactor = vcs.RedactorFunc(func(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if at := strings.IndexAny(rest, "@/ "); at >= 0 && rest[at] == '@' {
			return s[:i+3] + "REDACTED@" + rest[at+1:]
		}
	}
	return s
})
