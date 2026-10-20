package forge_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/forge/labtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// gitlabRedactor replaces the userinfo of a URL, the shape internal/redact
// recognizes, so a test can see outbound redaction happen.
var gitlabRedactor = vcs.RedactorFunc(func(s string) string {
	return strings.ReplaceAll(s, "user:hunter2@", "REDACTED@")
})

// openGitLab opens the adapter for gitlab.com/group/name over a stand-in
// answering as script says.
func openGitLab(t *testing.T, script labtest.Script) (*labtest.Provider, forge.Provider) {
	t.Helper()
	p := labtest.New(t, script)
	adapter, err := p.Host().Open("gitlab.com/group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return p, adapter
}

func openMR(iid int) labtest.MergeRequestView {
	return labtest.MergeRequestView{
		IID: iid, State: "opened", MergeStatus: "can_be_merged", SourceBranch: "fm/work",
		TargetBranch: "main", SHA: conformanceHead, Title: "feat: a change",
	}
}

func refusalOf(t *testing.T, err error) *forge.Refusal {
	t.Helper()
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want a *forge.Refusal", err)
	}
	return refusal
}

// GitLabRepository reads a specifier out of a gitlab.com remote and no other,
// names the host in it, accepts a group under a group, and refuses a web
// address pointing inside a project rather than reading the project above it.
func TestGitLabRepositoryReadsOnlyAGitLabComProject(t *testing.T) {
	cases := []struct {
		remote, want string
	}{
		{"https://gitlab.com/group/name.git", "gitlab.com/group/name"},
		{"git@gitlab.com:group/sub/name.git", "gitlab.com/group/sub/name"},
		{"ssh://git@gitlab.com:22/group/name.git", "gitlab.com/group/name"},
		{"https://oauth2:secret@gitlab.com/group/name/", "gitlab.com/group/name"},
		{"https://gitlab.com/group/name/-/tree/main", ""},
		{"https://gitlab.com/group", ""},
		{"https://gitlab.com/group/../name", ""},
		{"https://gitlab.example.com/group/name.git", ""},
		{"https://github.com/group/name.git", ""},
		{"gitlab.com/group/name", ""},
	}
	for _, c := range cases {
		got, ok := forge.GitLabRepository(c.remote)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("GitLabRepository(%q) = %q, %v; want %q", c.remote, got, ok, c.want)
		}
	}
}

// RepositoryOf is the one reading of a remote: a GitHub remote gives
// owner/name, a GitLab remote gives the host-qualified path, and anything
// else gives no specifier and the kind ClassifyRemote reports.
func TestRepositoryOfNamesTheHostAndTheSpecifierTogether(t *testing.T) {
	cases := []struct {
		remote string
		spec   string
		kind   forge.RemoteKind
	}{
		{"https://github.com/owner/name.git", "owner/name", forge.RemoteGitHub},
		{"https://gitlab.com/owner/name.git", "gitlab.com/owner/name", forge.RemoteGitLab},
		{"/srv/git/name.git", "", forge.RemoteLocal},
		{"https://gitlab.example.com/owner/name.git", "", forge.RemoteUnsupported},
	}
	for _, c := range cases {
		spec, kind, ok := forge.RepositoryOf(c.remote)
		if spec != c.spec || kind != c.kind || ok != (c.spec != "") {
			t.Errorf("RepositoryOf(%q) = %q, %v, %v; want %q, %v", c.remote, spec, kind, ok, c.spec, c.kind)
		}
	}
}

// recordingHost is a forge.Host that records what it was asked to open. It
// states no forge value a real adapter produces: Hosts' contract is which
// member a string reaches, and this records the string.
type recordingHost struct {
	opened *[]string
}

func (h recordingHost) Open(repository string) (forge.Provider, error) {
	*h.opened = append(*h.opened, repository)
	return nil, errors.New("recorded")
}

func (h recordingHost) OpenFork(repository, fork string) (forge.Provider, error) {
	*h.opened = append(*h.opened, repository+" <- "+fork)
	return nil, errors.New("recorded")
}

// Hosts hands a GitLab specifier to the GitLab member and every other one to
// the GitHub member, refuses a specifier whose member it does not hold rather
// than handing it to the other, and refuses a fork on a different host than
// its upstream.
func TestHostsRoutesASpecifierToTheHostItNames(t *testing.T) {
	var github, gitlab []string
	hosts := forge.Hosts{GitHub: recordingHost{&github}, GitLab: recordingHost{&gitlab}}
	_, _ = hosts.Open("owner/name")
	_, _ = hosts.Open("gitlab.com/group/name")
	_, _ = hosts.OpenFork("gitlab.com/group/name", "gitlab.com/me/name")
	if want := []string{"owner/name"}; strings.Join(github, ",") != strings.Join(want, ",") {
		t.Errorf("the GitHub member was asked %q, want %q", github, want)
	}
	if want := []string{"gitlab.com/group/name", "gitlab.com/group/name <- gitlab.com/me/name"}; strings.Join(gitlab, ",") != strings.Join(want, ",") {
		t.Errorf("the GitLab member was asked %q, want %q", gitlab, want)
	}

	if _, err := hosts.OpenFork("gitlab.com/group/name", "me/name"); !errors.Is(err, forge.ErrInvalidArgument) {
		t.Errorf("a fork on another host than its upstream answered %v, want an argument refusal", err)
	}
	if _, err := hosts.Open(""); !errors.Is(err, forge.ErrInvalidArgument) {
		t.Errorf("an empty specifier answered %v, want an argument refusal", err)
	}

	github, gitlab = nil, nil
	githubOnly := forge.Hosts{GitHub: recordingHost{&github}}
	_, err := githubOnly.Open("gitlab.com/group/name")
	if refusal := refusalOf(t, err); refusal.Reason != forge.ReasonUnavailable {
		t.Errorf("a GitLab specifier with no GitLab member answered %v, want unavailable", err)
	}
	if len(github) != 0 {
		t.Errorf("a GitLab specifier reached the GitHub member: %q", github)
	}
}

// A GitLab host opens only a specifier GitLabRepository writes, so an
// owner/name meant for GitHub never becomes a gitlab.com request.
func TestGitLabHostOpensOnlyAGitLabSpecifier(t *testing.T) {
	host := labtest.New(t, nil).Host()
	for _, spec := range []string{"", "group/name", "github.com/group/name", "gitlab.com/group", "GITLAB.COM/group/name"} {
		if _, err := host.Open(spec); !errors.Is(err, forge.ErrInvalidArgument) {
			t.Errorf("Open(%q) answered %v, want an argument refusal", spec, err)
		}
	}
	if _, err := host.OpenFork("gitlab.com/group/name", "gitlab.com/Group/Name"); !errors.Is(err, forge.ErrInvalidArgument) {
		t.Errorf("a fork naming the upstream answered %v, want an argument refusal", err)
	}
}

// No option can send a request to a host other than gitlab.com or a loopback
// stand-in, which is what keeps the token from going anywhere else.
func TestGitLabEndpointCannotLeaveGitLab(t *testing.T) {
	for _, endpoint := range []string{
		"https://gitlab.example.com/api/v4",
		"http://gitlab.com/api/v4",
		"https://user:token@gitlab.com/api/v4",
		"http://10.0.0.1/api/v4",
		"http://user:pw@127.0.0.1:9/api/v4",
	} {
		if _, err := forge.NewGitLabHost(gitlabRedactor, forge.WithGitLabEndpoint(endpoint)); !errors.Is(err, forge.ErrInvalidArgument) {
			t.Errorf("endpoint %q answered %v, want an argument refusal", endpoint, err)
		}
	}
	for _, endpoint := range []string{"https://gitlab.com/api/v4", "http://127.0.0.1:9/api/v4", "http://[::1]:9/api/v4"} {
		if _, err := forge.NewGitLabHost(gitlabRedactor, forge.WithGitLabEndpoint(endpoint)); err != nil {
			t.Errorf("endpoint %q was refused: %v", endpoint, err)
		}
	}
}

// A host with no token refuses every request as unauthenticated before
// anything is sent, and its probe says so; the stand-in's own record shows
// nothing arrived.
func TestGitLabWithoutATokenSendsNothing(t *testing.T) {
	p := labtest.New(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(openMR(12))}})
	host, err := forge.NewGitLabHost(gitlabRedactor, forge.WithGitLabEndpoint(p.Endpoint()))
	if err != nil {
		t.Fatalf("NewGitLabHost: %v", err)
	}
	if err := host.Probe(); refusalOf(t, err).Reason != forge.ReasonUnauthenticated {
		t.Errorf("Probe answered %v, want unauthenticated", err)
	}
	adapter, err := host.Open("gitlab.com/group/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = adapter.Get(context.Background(), 12)
	if refusalOf(t, err).Reason != forge.ReasonUnauthenticated {
		t.Errorf("Get answered %v, want unauthenticated", err)
	}
	if calls := p.Calls(); len(calls) != 0 {
		t.Fatalf("a host with no token sent %d requests", len(calls))
	}
	if err := p.Host().Probe(); err != nil {
		t.Errorf("a host with a token probed %v", err)
	}
}

// The token travels in the PRIVATE-TOKEN header and nowhere else, and a
// project path reaches GitLab as one encoded segment.
func TestGitLabSendsTheTokenAsAHeaderAndThePathEncoded(t *testing.T) {
	p := labtest.New(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(openMR(12))}})
	adapter, err := p.Host().Open("gitlab.com/group/sub/name")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := adapter.Get(context.Background(), 12); err != nil {
		t.Fatalf("Get: %v", err)
	}
	calls := p.Calls()
	if len(calls) != 1 {
		t.Fatalf("Get made %d requests, want 1", len(calls))
	}
	c := calls[0]
	if c.Token != labtest.Token || c.Project != "group/sub/name" || c.Path != "/api/v4/projects/group%2Fsub%2Fname/merge_requests/12" {
		t.Fatalf("the request arrived as %+v", c)
	}
	if strings.Contains(c.Path+c.Query.Encode()+c.Body, labtest.Token) {
		t.Fatalf("the token reached the URL or the body: %+v", c)
	}
}

// Get maps GitLab's states and mergeability onto the package's model, with
// every word it does not recognize, and GitLab's locked read as unknown.
func TestGitLabGetMapsStateAndMergeability(t *testing.T) {
	cases := []struct {
		state, mergeStatus string
		conflicts          bool
		wantState          forge.PullRequestState
		wantMerge          forge.Mergeability
	}{
		{"opened", "can_be_merged", false, forge.PullRequestStateOpen, forge.MergeabilityMergeable},
		{"opened", "cannot_be_merged", true, forge.PullRequestStateOpen, forge.MergeabilityConflicted},
		{"opened", "cannot_be_merged", false, forge.PullRequestStateOpen, forge.MergeabilityUnknown},
		{"opened", "unchecked", false, forge.PullRequestStateOpen, forge.MergeabilityUnknown},
		{"opened", "checking", false, forge.PullRequestStateOpen, forge.MergeabilityUnknown},
		{"opened", "cannot_be_merged_recheck", false, forge.PullRequestStateOpen, forge.MergeabilityUnknown},
		{"opened", "", false, forge.PullRequestStateOpen, forge.MergeabilityUnknown},
		{"merged", "can_be_merged", false, forge.PullRequestStateMerged, forge.MergeabilityMergeable},
		{"closed", "unchecked", false, forge.PullRequestStateClosed, forge.MergeabilityUnknown},
		{"locked", "unchecked", false, forge.PullRequestStateUnknown, forge.MergeabilityUnknown},
		{"reopened", "can_be_merged", false, forge.PullRequestStateUnknown, forge.MergeabilityMergeable},
	}
	for _, c := range cases {
		v := openMR(12)
		v.State, v.MergeStatus, v.HasConflicts, v.Draft = c.state, c.mergeStatus, c.conflicts, true
		_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(v)}})
		pr, err := adapter.Get(context.Background(), 12)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if pr.State != c.wantState || pr.Mergeability != c.wantMerge {
			t.Errorf("%s/%s/conflicts=%v read as %s/%s, want %s/%s", c.state, c.mergeStatus, c.conflicts,
				pr.State, pr.Mergeability, c.wantState, c.wantMerge)
		}
		if pr.Number != 12 || pr.Head != "fm/work" || pr.Base != "main" || pr.HeadCommit != conformanceHead || !pr.Draft ||
			pr.URL != "https://gitlab.com/group/name/-/merge_requests/12" {
			t.Errorf("Get answered %+v", pr)
		}
	}
}

// GitLab documents has_conflicts as false unless merge_status is
// cannot_be_merged, so the pair can_be_merged with has_conflicts true is
// unknown rather than conflicted.
func TestGitLabGetReadsImpossibleConflictPairAsUnknown(t *testing.T) {
	v := openMR(12)
	v.MergeStatus, v.HasConflicts = "can_be_merged", true
	_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(v)}})
	pr, err := adapter.Get(context.Background(), 12)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.Mergeability != forge.MergeabilityUnknown {
		t.Fatalf("Get read mergeability as %s, want unknown", pr.Mergeability)
	}
}

// An answer about a different merge request than the one asked about is not
// an answer.
func TestGitLabGetRefusesAnAnswerAboutAnotherMergeRequest(t *testing.T) {
	_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(openMR(13))}})
	_, err := adapter.Get(context.Background(), 12)
	if refusalOf(t, err).Reason != forge.ReasonMalformed {
		t.Fatalf("Get answered %v, want malformed", err)
	}
}

// Find keeps only a merge request whose source project is its target, so a
// fork proposing a branch of the same name is not this run's, and two of its
// own are ambiguous rather than picked between.
func TestGitLabFindKeepsOnlyTheProjectsOwnMergeRequest(t *testing.T) {
	own := openMR(12)
	fromFork := openMR(13)
	fromFork.SourceProjectID = 99
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpList: {labtest.MergeRequests(fromFork, own)},
		labtest.OpView: {labtest.MergeRequest(own)},
	})
	pr, found, err := adapter.Find(context.Background(), "fm/work")
	if err != nil || !found || pr.Number != 12 {
		t.Fatalf("Find answered %+v, %v, %v; want merge request 12", pr, found, err)
	}
	list := p.CallsFor(labtest.OpList)
	if len(list) != 1 || list[0].Query.Get("state") != "opened" || list[0].Query.Get("source_branch") != "fm/work" {
		t.Fatalf("the list was asked as %+v", list)
	}

	_, adapter = openGitLab(t, labtest.Script{labtest.OpList: {labtest.MergeRequests(fromFork)}})
	if _, found, err := adapter.Find(context.Background(), "fm/work"); err != nil || found {
		t.Fatalf("a branch only a fork proposes answered %v, %v; want not found", found, err)
	}

	second := openMR(14)
	_, adapter = openGitLab(t, labtest.Script{labtest.OpList: {labtest.MergeRequests(own, second)}})
	_, _, err = adapter.Find(context.Background(), "fm/work")
	if refusalOf(t, err).Reason != forge.ReasonAmbiguous {
		t.Fatalf("two of the project's own merge requests answered %v, want ambiguous", err)
	}

	full := make([]labtest.MergeRequestView, 100)
	for i := range full {
		full[i] = fromFork
		full[i].IID = 100 + i
	}
	_, adapter = openGitLab(t, labtest.Script{labtest.OpList: {labtest.MergeRequests(full...)}})
	_, _, err = adapter.Find(context.Background(), "fm/work")
	if refusalOf(t, err).Reason != forge.ReasonAmbiguous {
		t.Fatalf("a full page answered %v, want ambiguous", err)
	}
}

// Find from a fork confirms the fork, which is what gives it the fork's
// identifier, and keeps only a merge request whose source project is that.
func TestGitLabFindFromAForkKeepsOnlyTheForksMergeRequest(t *testing.T) {
	fromFork := openMR(13)
	fromFork.SourceProjectID = 99
	own := openMR(12)
	p := labtest.New(t, labtest.Script{
		labtest.OpProject: {labtest.Project(99, "me/name")},
		labtest.OpList:    {labtest.MergeRequests(own, fromFork)},
		labtest.OpView:    {labtest.MergeRequest(fromFork)},
	})
	adapter, err := p.Host().OpenFork("gitlab.com/group/name", "gitlab.com/me/name")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	pr, found, err := adapter.Find(context.Background(), "fm/work")
	if err != nil || !found || pr.Number != 13 {
		t.Fatalf("Find answered %+v, %v, %v; want merge request 13", pr, found, err)
	}
	if got := p.CallsFor(labtest.OpProject); len(got) != 1 || got[0].Project != "me/name" {
		t.Fatalf("the fork was confirmed as %+v", got)
	}
	if got := p.CallsFor(labtest.OpList); len(got) != 1 || got[0].Project != "group/name" {
		t.Fatalf("the list was read from %+v, want the upstream", got)
	}
}

// A create goes to the project the branch lives in, carries exactly the
// fields asked for with outbound text redacted, and is answered with the merge
// request GitLab created; a draft is marked the way GitLab documents.
func TestGitLabOpenSendsTheCreateGitLabDocuments(t *testing.T) {
	created := openMR(12)
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name")},
		labtest.OpCreate:  {labtest.Created(created)},
	})
	pr, err := adapter.Open(context.Background(), forge.OpenSpec{
		Head: "fm/work", Base: "main", Title: "feat: a change",
		Body: "see https://user:hunter2@example.com/log", Draft: true,
	})
	if err != nil || pr.Number != 12 {
		t.Fatalf("Open answered %+v, %v", pr, err)
	}
	creates := p.CallsFor(labtest.OpCreate)
	if len(creates) != 1 || creates[0].Project != "group/name" {
		t.Fatalf("the create was sent as %+v", creates)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(creates[0].Body), &body); err != nil {
		t.Fatalf("the create body: %v", err)
	}
	want := map[string]any{
		"source_branch": "fm/work", "target_branch": "main", "title": "Draft: feat: a change",
		"description": "see https://REDACTED@example.com/log",
	}
	if len(body) != len(want) {
		t.Fatalf("the create carried %v, want exactly %v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("the create carried %s=%v, want %v", k, body[k], v)
		}
	}
	if order := p.Calls(); order[0].Op != labtest.OpProject {
		t.Fatalf("the create was not preceded by the confirmation: %+v", order)
	}
}

// A create from a fork is sent to the fork with the upstream's identifier as
// its target, after both projects are confirmed, and a fork the host reports
// as another project stops it.
func TestGitLabOpenFromAForkNamesTheUpstreamAsTarget(t *testing.T) {
	created := openMR(12)
	created.SourceProjectID = 99
	p := labtest.New(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name"), labtest.Project(99, "me/name")},
		labtest.OpCreate:  {labtest.Created(created)},
	})
	adapter, err := p.Host().OpenFork("gitlab.com/group/name", "gitlab.com/me/name")
	if err != nil {
		t.Fatalf("OpenFork: %v", err)
	}
	if _, err := adapter.Open(context.Background(), forge.OpenSpec{Head: "fm/work", Base: "main", Title: "t"}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	creates := p.CallsFor(labtest.OpCreate)
	if len(creates) != 1 || creates[0].Project != "me/name" || !strings.Contains(creates[0].Body, `"target_project_id":3`) {
		t.Fatalf("the create was sent as %+v", creates)
	}

	p = labtest.New(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name"), labtest.Project(98, "someone/else")},
		labtest.OpCreate:  {labtest.Created(created)},
	})
	adapter, _ = p.Host().OpenFork("gitlab.com/group/name", "gitlab.com/me/name")
	_, err = adapter.Open(context.Background(), forge.OpenSpec{Head: "fm/work", Base: "main", Title: "t"})
	if refusalOf(t, err).Reason != forge.ReasonWrongRepository || len(p.CallsFor(labtest.OpCreate)) != 0 {
		t.Fatalf("a fork reported as another project answered %v and created %d", err, len(p.CallsFor(labtest.OpCreate)))
	}
}

// An answer to a create that is not on the branch and base asked for is not
// relied on.
func TestGitLabOpenRefusesACreateAnsweredForAnotherBranch(t *testing.T) {
	created := openMR(12)
	created.SourceBranch = "someone-else"
	_, adapter := openGitLab(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name")},
		labtest.OpCreate:  {labtest.Created(created)},
	})
	_, err := adapter.Open(context.Background(), forge.OpenSpec{Head: "fm/work", Base: "main", Title: "t"})
	if refusalOf(t, err).Reason != forge.ReasonMalformed {
		t.Fatalf("Open answered %v, want malformed", err)
	}
}

// UpdateBody confirms the project, sends the description and nothing else,
// and answers with what GitLab reported.
func TestGitLabUpdateBodySendsOnlyTheDescription(t *testing.T) {
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name")},
		labtest.OpUpdate:  {labtest.MergeRequest(openMR(12))},
	})
	if _, err := adapter.UpdateBody(context.Background(), 12, "new body"); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}
	updates := p.CallsFor(labtest.OpUpdate)
	if len(updates) != 1 || updates[0].Body != `{"description":"new body"}` || len(p.CallsFor(labtest.OpProject)) != 1 {
		t.Fatalf("the update was sent as %+v", p.Calls())
	}
}

// The merge body binds the merge to the head and asks for nothing else: no
// method, no squash, and no merge once a pipeline succeeds.
func TestGitLabMergeAsksOnlyForABoundMergeNow(t *testing.T) {
	principles.Cite(t, principles.P6)
	merged := openMR(12)
	merged.State = "merged"
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name")},
		labtest.OpMerge:   {labtest.MergeRequest(merged)},
	})
	if _, err := adapter.Merge(context.Background(), 12, conformanceHead); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	merges := p.CallsFor(labtest.OpMerge)
	if len(merges) != 1 || merges[0].Body != `{"sha":"`+conformanceHead+`"}` || len(merges[0].Query) != 0 {
		t.Fatalf("the merge was sent as %+v", merges)
	}
}

// A merge GitLab refuses arrives from Merge as ReasonRejected carrying
// GitLab's status and its own message, for each refusal its merge endpoint
// documents for a merge request it will not merge, with the message GitLab's
// source writes for it: 405 for one it will not merge now, 409 for a bound
// sha that is not the source branch's head, and 422 for a merge it attempted
// and did not make. Each is asked once; the adapter does not ask again.
func TestGitLabMergeRefusalsAreRejected(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, refusal := range []struct {
		status  int
		message string
	}{
		{http.StatusMethodNotAllowed, "405 Method Not Allowed"},
		{http.StatusConflict, "SHA does not match HEAD of source branch: " + strings.Repeat("b", 40)},
		{http.StatusUnprocessableEntity, "Branch cannot be merged"},
	} {
		t.Run(http.StatusText(refusal.status), func(t *testing.T) {
			p, adapter := openGitLab(t, labtest.Script{
				labtest.OpProject: {labtest.Project(3, "group/name")},
				labtest.OpMerge:   {labtest.Error(refusal.status, refusal.message)},
			})
			pr, err := adapter.Merge(context.Background(), 12, conformanceHead)
			if pr != (forge.PullRequest{}) {
				t.Fatalf("a refused merge answered the pull request %+v", pr)
			}
			got := refusalOf(t, err)
			if got.Reason != forge.ReasonRejected || got.Op != "merge" ||
				!strings.Contains(got.Detail, "HTTP "+strconv.Itoa(refusal.status)) || !strings.Contains(got.Detail, refusal.message) {
				t.Fatalf("the refusal is %+v, want rejected carrying HTTP %d and %q", got, refusal.status, refusal.message)
			}
			if merges := p.CallsFor(labtest.OpMerge); len(merges) != 1 {
				t.Fatalf("the refused merge was asked %d times, want once", len(merges))
			}
		})
	}
}

// Checks reads the pipelines on the head and only those: a pipeline on an
// earlier head is not a check on this one, and a skipped pipeline ran
// nothing, so a head whose only pipeline was skipped has no checks and is
// not green.
func TestGitLabChecksReadsOnlyThePipelinesThatRanOnTheHead(t *testing.T) {
	older := strings.Repeat("a", 40)
	_, adapter := openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(openMR(12))},
		labtest.OpPipelines: {labtest.Pipelines(
			labtest.PipelineView{ID: 9, SHA: conformanceHead, Ref: "fm/work", Status: "running"},
			labtest.PipelineView{ID: 8, SHA: conformanceHead, Ref: "fm/work", Status: "skipped"},
			labtest.PipelineView{ID: 7, SHA: older, Ref: "fm/work", Status: "failed"},
		)},
	})
	report, err := adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(report.Runs) != 1 || report.Runs[0].Name != "pipeline 9" || report.Runs[0].State != forge.CheckStatePending ||
		report.Runs[0].URL != "https://gitlab.com/group/name/-/pipelines/9" {
		t.Fatalf("Checks answered %+v, want the one pipeline on the head that ran", report.Runs)
	}

	_, adapter = openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(openMR(12))},
		labtest.OpPipelines: {labtest.Pipelines(
			labtest.PipelineView{ID: 8, SHA: conformanceHead, Ref: "fm/work", Status: "skipped"},
		)},
	})
	report, err = adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictNoChecks {
		t.Fatalf("a head whose only pipeline was skipped evaluated as %s, want no checks", result)
	}
}

const gitLabMergedResultRef = "refs/merge-requests/12/merge"

const gitLabMergeRequestHeadRef = "refs/merge-requests/12/head"

// Checks counts a merged-result pipeline only when head_pipeline names it,
// because GitLab documents that field as the pipeline for the current head.
func TestGitLabChecksCountsMergedResultPipelines(t *testing.T) {
	mergedSHA := strings.Repeat("c", 40)
	headPipe := labtest.PipelineView{ID: 10, SHA: mergedSHA, Ref: gitLabMergedResultRef, Status: "success"}
	mr := openMR(12)
	mr.HeadPipeline = &headPipe
	_, adapter := openGitLab(t, labtest.Script{
		labtest.OpView:      {labtest.MergeRequest(mr)},
		labtest.OpPipelines: {labtest.Pipelines(headPipe)},
	})
	report, err := adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictPassed {
		t.Fatalf("head_pipeline naming a successful merged-result pipeline evaluated as %s, want passed", result)
	}

	headPipe = labtest.PipelineView{ID: 10, SHA: mergedSHA, Ref: gitLabMergedResultRef, Status: "failed"}
	mr = openMR(12)
	mr.HeadPipeline = &headPipe
	_, adapter = openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(mr)},
		labtest.OpPipelines: {labtest.Pipelines(
			labtest.PipelineView{ID: 9, SHA: conformanceHead, Ref: "fm/work", Status: "success"},
			headPipe,
		)},
	})
	report, err = adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictFailed {
		t.Fatalf("head_pipeline naming a failed merged-result pipeline evaluated as %s, want failed", result)
	}
}

// Checks does not treat a historical merged-result pipeline as a check on a
// new head when head_pipeline names nothing and no branch pipeline matches.
func TestGitLabChecksIgnoresHistoricalPipelineWhenHeadAdvanced(t *testing.T) {
	olderHead := strings.Repeat("a", 40)
	_, adapter := openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(openMR(12))},
		labtest.OpPipelines: {labtest.Pipelines(
			labtest.PipelineView{ID: 10, SHA: olderHead, Ref: gitLabMergeRequestHeadRef, Status: "success"},
		)},
	})
	report, err := adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictNoChecks {
		t.Fatalf("only a previous head's pipeline evaluated as %s, want no checks", result)
	}

	_, adapter = openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(openMR(12))},
		labtest.OpPipelines: {labtest.Pipelines(
			labtest.PipelineView{ID: 10, SHA: olderHead, Ref: gitLabMergeRequestHeadRef, Status: "success"},
			labtest.PipelineView{ID: 9, SHA: conformanceHead, Ref: "fm/work", Status: "running"},
		)},
	})
	report, err = adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(report.Runs) != 1 || report.Runs[0].Name != "pipeline 9" {
		t.Fatalf("Checks answered %+v, want the branch pipeline on the current head", report.Runs)
	}
}

// Checks follows head_pipeline on the current head, not a failed merged-result
// pipeline GitLab still lists from an earlier head.
func TestGitLabChecksFollowsHeadPipelinePastHistoricalFailure(t *testing.T) {
	olderHead := strings.Repeat("a", 40)
	mergedSHA := strings.Repeat("c", 40)
	headPipe := labtest.PipelineView{ID: 11, SHA: mergedSHA, Ref: gitLabMergedResultRef, Status: "success"}
	mr := openMR(12)
	mr.HeadPipeline = &headPipe
	_, adapter := openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(mr)},
		labtest.OpPipelines: {labtest.Pipelines(
			labtest.PipelineView{ID: 10, SHA: olderHead, Ref: gitLabMergedResultRef, Status: "failed"},
			headPipe,
		)},
	})
	report, err := adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictPassed {
		t.Fatalf("head_pipeline naming a passing pipeline evaluated as %s, want passed despite an older failure in the list", result)
	}

	headPipe = labtest.PipelineView{ID: 10, SHA: olderHead, Ref: gitLabMergedResultRef, Status: "failed"}
	mr = openMR(12)
	mr.HeadPipeline = &headPipe
	_, adapter = openGitLab(t, labtest.Script{
		labtest.OpView:      {labtest.MergeRequest(mr)},
		labtest.OpPipelines: {labtest.Pipelines(headPipe)},
	})
	report, err = adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictFailed {
		t.Fatalf("head_pipeline naming a failed pipeline evaluated as %s, want failed", result)
	}
}

// Every pipeline status GitLab documents maps to a state, the two-L spelling
// of cancelled is a word GitLab does not use and waits, and manual waits for
// the person who has to start a job.
func TestGitLabPipelineStatusesMapStrictly(t *testing.T) {
	cases := map[string]forge.CheckState{
		"created": forge.CheckStatePending, "waiting_for_resource": forge.CheckStatePending,
		"preparing": forge.CheckStatePending, "pending": forge.CheckStatePending,
		"running": forge.CheckStatePending, "scheduled": forge.CheckStatePending,
		"manual": forge.CheckStatePending, "success": forge.CheckStateSucceeded,
		"failed": forge.CheckStateFailed, "canceled": forge.CheckStateCancelled,
		"cancelled": forge.CheckStateUnrecognized, "": forge.CheckStateUnrecognized,
	}
	for status, want := range cases {
		_, adapter := openGitLab(t, labtest.Script{
			labtest.OpView:      {labtest.MergeRequest(openMR(12))},
			labtest.OpPipelines: {labtest.Pipelines(labtest.PipelineView{ID: 9, SHA: conformanceHead, Status: status})},
		})
		report, err := adapter.Checks(context.Background(), 12)
		if err != nil {
			t.Fatalf("Checks(%q): %v", status, err)
		}
		if len(report.Runs) != 1 || report.Runs[0].State != want {
			t.Errorf("status %q read as %+v, want %s", status, report.Runs, want)
		}
		if want == forge.CheckStateUnrecognized && report.Runs[0].Reported != status {
			t.Errorf("status %q was not named on the run: %+v", status, report.Runs[0])
		}
	}
}

// The pipeline list is followed page by page until a short page, and one
// still full at the bound is refused rather than judged on what was read.
func TestGitLabChecksFollowsPagesUpToTheBound(t *testing.T) {
	fullPage := func(status string) labtest.Response {
		rows := make([]labtest.PipelineView, 100)
		for i := range rows {
			rows[i] = labtest.PipelineView{ID: 1000 + i, SHA: strings.Repeat("b", 40), Ref: "fm/work", Status: status}
		}
		return labtest.Pipelines(rows...)
	}
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpView: {labtest.MergeRequest(openMR(12))},
		labtest.OpPipelines: {fullPage("success"), labtest.Pipelines(
			labtest.PipelineView{ID: 5, SHA: conformanceHead, Status: "failed"},
		)},
	})
	report, err := adapter.Checks(context.Background(), 12)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if result := report.Evaluate(forge.NoCIDeclaration{}); result.Verdict != forge.VerdictFailed {
		t.Fatalf("a failed pipeline on the second page evaluated as %s, want failed", result)
	}
	pages := p.CallsFor(labtest.OpPipelines)
	if len(pages) != 2 || pages[0].Query.Get("page") != "1" || pages[1].Query.Get("page") != "2" || pages[1].Query.Get("per_page") != "100" {
		t.Fatalf("the pages were asked as %+v", pages)
	}

	p, adapter = openGitLab(t, labtest.Script{
		labtest.OpView:      {labtest.MergeRequest(openMR(12))},
		labtest.OpPipelines: {fullPage("success")},
	})
	_, err = adapter.Checks(context.Background(), 12)
	if refusalOf(t, err).Reason != forge.ReasonAmbiguous {
		t.Fatalf("pipelines still full at the bound answered %v, want ambiguous", err)
	}
	if n := len(p.CallsFor(labtest.OpPipelines)); n != 10 {
		t.Fatalf("a read still full at the bound asked for %d pages, want the bound of 10", n)
	}
}

// A merge request GitLab reports with no head commit has no checks this
// package can place.
func TestGitLabChecksRefusesAMergeRequestWithNoHead(t *testing.T) {
	v := openMR(12)
	v.SHA = ""
	_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {labtest.MergeRequest(v)}})
	_, err := adapter.Checks(context.Background(), 12)
	if refusalOf(t, err).Reason != forge.ReasonMalformed {
		t.Fatalf("Checks answered %v, want malformed", err)
	}
}

// GitLab's answers map onto the refusal reasons: 401 is unauthenticated, any
// other error status is a rejection carrying GitLab's message, and an
// unreadable answer is malformed.
func TestGitLabRefusalsCarryTheReasonTheStatusGives(t *testing.T) {
	cases := []struct {
		answer labtest.Response
		want   forge.Reason
		detail string
	}{
		{labtest.Unauthorized(), forge.ReasonUnauthenticated, "401 Unauthorized"},
		{labtest.Error(http.StatusNotFound, "404 Not found"), forge.ReasonRejected, "HTTP 404"},
		{labtest.Error(http.StatusForbidden, "403 Forbidden"), forge.ReasonRejected, "403 Forbidden"},
		{labtest.Raw("{not json"), forge.ReasonMalformed, "could not be read"},
	}
	for _, c := range cases {
		_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {c.answer}})
		_, err := adapter.Get(context.Background(), 12)
		refusal := refusalOf(t, err)
		if refusal.Reason != c.want || !strings.Contains(refusal.Detail, c.detail) {
			t.Errorf("answer %+v refused as %v, want %s naming %q", c.answer, refusal, c.want, c.detail)
		}
	}
}

// A redirect is refused, its target is not repeated, and nothing - the token
// least of all - is sent where it pointed.
func TestGitLabDoesNotFollowARedirect(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer elsewhere.Close()
	_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {labtest.Redirect(elsewhere.URL + "/steal")}})
	_, err := adapter.Get(context.Background(), 12)
	refusal := refusalOf(t, err)
	if refusal.Reason != forge.ReasonRejected || strings.Contains(refusal.Error(), elsewhere.URL) {
		t.Fatalf("a redirect refused as %v", refusal)
	}
	if reached {
		t.Fatal("the redirect was followed")
	}
}

// A request the host never answers ends at the request's own bound, as
// unavailable, so a caller's checks_timeout bounds a read that hangs.
func TestGitLabARequestThatHangsEndsAtItsTimeout(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer hang.Close()
	defer close(release)
	host, err := forge.NewGitLabHost(gitlabRedactor, forge.WithGitLabEndpoint(hang.URL+"/api/v4"),
		forge.WithGitLabToken("t"), forge.WithGitLabTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGitLabHost: %v", err)
	}
	adapter, _ := host.Open("gitlab.com/group/name")
	start := time.Now()
	_, err = adapter.Get(context.Background(), 12)
	if refusalOf(t, err).Reason != forge.ReasonUnavailable {
		t.Fatalf("a hung request answered %v, want unavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a hung request took %s to end", elapsed)
	}
}

// An answer over the output bound is discarded whole.
func TestGitLabDiscardsAnOversizeAnswer(t *testing.T) {
	_, adapter := openGitLab(t, labtest.Script{labtest.OpView: {labtest.Raw(`"` + strings.Repeat("x", forge.DefaultMaxOutput) + `"`)}})
	_, err := adapter.Get(context.Background(), 12)
	if refusalOf(t, err).Reason != forge.ReasonOversizeAnswer {
		t.Fatalf("an oversize answer refused as %v", err)
	}
}

// A refusal whose body is over the output bound is discarded whole as well:
// it is ReasonOversizeAnswer and not ReasonRejected, and its detail carries
// neither GitLab's status nor its message, because the body is bounded
// before the status is read. This is the exception doc.go names to a merge
// refusal arriving rejected with both.
func TestGitLabDiscardsAnOversizeRefusal(t *testing.T) {
	message := "Branch cannot be merged " + strings.Repeat("x", forge.DefaultMaxOutput)
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpProject: {labtest.Project(3, "group/name")},
		labtest.OpMerge:   {labtest.Error(http.StatusUnprocessableEntity, message)},
	})
	_, err := adapter.Merge(context.Background(), 12, conformanceHead)
	got := refusalOf(t, err)
	if got.Reason != forge.ReasonOversizeAnswer || got.Op != "merge" {
		t.Fatalf("an oversize 422 refused as %+v, want oversize", got)
	}
	if strings.Contains(got.Detail, "422") || strings.Contains(got.Detail, "Branch cannot be merged") {
		t.Fatalf("an oversize refusal's detail %q carries the status or message it discarded", got.Detail)
	}
	if merges := p.CallsFor(labtest.OpMerge); len(merges) != 1 {
		t.Fatalf("the merge was asked %d times, want once", len(merges))
	}
}

// The confirmation is remembered only once it held, so a write after a failed
// confirmation asks again rather than refusing for ever or writing blind.
func TestGitLabConfirmationIsRememberedOnlyOnceItHeld(t *testing.T) {
	p, adapter := openGitLab(t, labtest.Script{
		labtest.OpProject: {labtest.Error(http.StatusBadGateway, "502 Bad Gateway"), labtest.Project(3, "group/name")},
		labtest.OpUpdate:  {labtest.MergeRequest(openMR(12))},
	})
	if _, err := adapter.UpdateBody(context.Background(), 12, "b"); err == nil {
		t.Fatal("an update whose confirmation failed went ahead")
	}
	for range 2 {
		if _, err := adapter.UpdateBody(context.Background(), 12, "b"); err != nil {
			t.Fatalf("UpdateBody: %v", err)
		}
	}
	if n := len(p.CallsFor(labtest.OpProject)); n != 2 {
		t.Fatalf("the project was confirmed %d times, want 2", n)
	}
	if n := len(p.CallsFor(labtest.OpUpdate)); n != 2 {
		t.Fatalf("%d updates were sent, want the 2 after a confirmation held", n)
	}
}
