package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The repository configuration layer, read per run.
//
// PRD section 10 gives a run three configuration documents: the operator's
// global file, the repository document as the default branch holds it at a
// freshly fetched commit, and the same document as the branch under
// validation holds it. The global file is read when this service opens; the
// two repository copies are read here, once per run and again on a resume,
// because the trusted copy has to be fresh and the pushed copy has to be the
// run's own submitted commit.
//
// Both copies are read out of the gate's bare repository, which is where the
// run's isolated copy is cut from: the submitted commit is fetched into it
// before the run starts, and the default branch's tip is fetched from the
// upstream the repository record names, under a reference named for the run
// so two runs cannot overwrite each other's. Reading from the gate repository
// rather than from the person's working copy is what makes the trusted
// origin's claim honest: the bytes come from the upstream's default branch at
// a commit this fetch resolved, not from whatever a checkout happens to hold.
//
// # The refusals, and which case is not one
//
// A default branch that cannot be fetched, a trusted document that cannot be
// read, and a trusted document that cannot be parsed all stop the run before
// anything launches, wrapped in ErrTrustedConfig; falling back to defaults on
// any of them is the guess PRD section 10 forbids. So does a trusted document
// that parses and sets a key this build does not act on yet, placement.arch
// (unhonored.go owns which keys, why, and what that leaves uncovered). A
// commit whose tree holds no document is the one case that is not a refusal:
// that is a repository with no configuration, which is valid and resolves to
// the layers beneath.
//
// A pushed document that cannot be parsed is refused too, wrapped in
// ErrPushedConfig, and deliberately so even though its keys are mostly
// dropped: section 10 has invalid configuration fail at parse time even on a
// branch whose fields are otherwise ignored, because a broken rule should
// surface before it merges.
//
// A copy that loosens a key only the operator's own file may loosen, the
// delivery posture (config.TrustGlobalLoosens), refuses the resolution with a
// *config.LoosenError, which is reported under ErrTrustedConfig or
// ErrPushedConfig by the copy it names: the run stops before anything
// launches, whatever push started it, because no posture is assumed in the
// refused one's place.
//
// # Which of them cross the wire as refused
//
// A document that was read and decided against is a refusal the caller must
// handle, and refusedConfig is the one place that says so: a trusted or a
// pushed copy internal/config refused (config.ErrMalformed, ErrInvalid or
// ErrLoosened beneath ErrTrustedConfig or ErrPushedConfig), a trusted copy
// setting a key this build does not act on (errUnhonored beneath
// ErrTrustedConfig), a knowledge
// document internal/knowledge refused at the trusted copy (ErrTooLarge or
// ErrNotText beneath ErrTrustedConfig, knowledge.go), and an agent
// list or a roles table this build cannot honour, cross as ipc.ErrRefused
// with their own text. A copy that could not be read at all - a default
// branch the record does not name, a fetch or a read of the gate repository
// that failed, a store that could not answer - is not a decision about any
// document and crosses as it did before, which for a failed read is
// internal. The classification is applied where a run's resolution and
// standing consent's read answer a request, and not inside trustedLayerAt,
// because intake and promotion read the same copy and answer for it through
// internal/tasks' ErrPostureUnreadable, which crosses as unavailable. A
// refused run whose record then could not be ended is not a clean refusal
// either: begin answers it as a *RunNotEndedError, which endRefused strips of
// this classification, so it crosses on its ending's failure instead.

// ErrTrustedConfig reports that the trusted configuration copy could not be
// established: the default branch could not be fetched, the document there
// could not be read or parsed, it sets a key this build does not act on yet
// (refusalUnhonoredRepository), or it loosened a key only the operator's own
// file may loosen (*config.LoosenError). PRD section 10 stops the run before
// launching anything rather than guessing at defaults.
var ErrTrustedConfig = errors.New("service: the trusted configuration could not be established; the run is stopped rather than run on guessed defaults")

// ErrPushedConfig reports that the pushed copy of the repository
// configuration document is invalid, or loosened a key only the operator's
// own file may loosen (*config.LoosenError). It is refused rather than
// ignored so the broken rule surfaces before it merges, per PRD section 10.
var ErrPushedConfig = errors.New("service: the pushed configuration document is invalid")

// ErrRepositoryAgent reports a repository configuration asking for an agent
// list this build cannot honor. This build resolves one agent per service,
// from the operator's layer, and a run whose resolved agent list differs is
// refused rather than quietly run under an agent its repository did not ask
// for. Unsetting "agent" in the repository document, or setting the same list
// in the operator's configuration, resolves it.
var ErrRepositoryAgent = errors.New("service: this build resolves one agent per service, from the operator's configuration, and this repository's configuration asks for a different agent list")

// ErrRepositoryRoles reports a repository configuration asking for a roles
// table this build cannot honor. This build reads roles from the operator's
// layer, the same document it reads the agent list from, and a run whose
// resolved roles differ is refused rather than launched on a table its
// repository did not ask for.
var ErrRepositoryRoles = errors.New("service: this build resolves one roles table per service, from the operator's configuration, and this repository's configuration asks for a different one")

// ErrForkUnresolved reports a repository record naming a fork that a run
// cannot push to and propose from: the fork or its upstream is not a
// repository on a code host this build addresses, the two are on different
// hosts, or the fork names the upstream itself. The run is refused before
// anything is recorded as running, because pushing anywhere else would be a
// guess about where its work goes. Running assistant init again with a fork
// that resolves, or without one, resolves it.
var ErrForkUnresolved = errors.New("service: the repository record names a fork this run cannot push to and propose from")

// ErrUnsupportedHost reports a repository whose upstream names a network host
// this build does not address (a self-managed GitLab, GitHub Enterprise, an
// scp-style host that is neither github.com nor gitlab.com, or a scheme-less
// host/path form). The run is refused rather than walked to the pull request
// stage and stalled; begin ends the pending record for that refusal before
// returning, or answers *RunNotEndedError wrapping this error when it cannot,
// so an unfinished run is never reported as this refusal alone. A local path
// or file:// remote is not this error: those have no code host and the run
// skips the pull request and checks stages after push.
var ErrUnsupportedHost = errors.New("service: the repository's upstream names a host this build does not address")

// trustedRef is the name the gate repository holds a run's freshly fetched
// default-branch tip under. It is named for the run so concurrent runs cannot
// overwrite each other's, on the same terms as submittedRef, and it is
// removed with the run's other references when the run is reclaimed.
func trustedRef(runID string) string { return "refs/assistant/trusted/" + runID }

// runResolution is one run's resolved configuration together with the digest
// that identifies the documents it was resolved from.
type runResolution struct {
	cfg config.Config
	// digest identifies the three documents this resolution was made from, so
	// the run's record traces to the settings that reached it. It covers the
	// operator document, the trusted copy and the commit it was read at, and
	// the pushed copy; two runs resolving identical values from different
	// documents digest differently, which is the point: the question a reader
	// asks is which settings the run was given.
	digest string
	// rejected is each key the resolution dropped because the layer that set
	// it was not allowed to, rendered as config.Rejection does, in the order
	// the resolution reported them. It goes to the run's record with the
	// digest, so an author whose key had no effect can read that from the
	// run rather than from this service's log.
	rejected []string
	// knowledge is the repository's curated knowledge document read at the
	// trusted commit the digest names, so two resolutions with one digest
	// carry one reading, and the zero reading for a run that suppresses
	// project instructions, which the digest also covers through the
	// documents that set the key.
	knowledge knowledge.Knowledge
}

// resolveRunConfig reads the run's two repository configuration copies and
// resolves them with the operator's layer. The file's opening comment owns
// the refusals; what this adds is the order, which is trusted before pushed
// so a repository whose trusted copy is broken refuses on that rather than on
// whatever the branch carries.
func (s *Service) resolveRunConfig(ctx context.Context, record store.Run) (runResolution, error) {
	resolved, err := s.resolveRunDocuments(ctx, record)
	if err != nil {
		return runResolution{}, refusedConfig(err)
	}
	return resolved, nil
}

// resolveRunDocuments is resolveRunConfig before its refusals are classified
// for the wire.
func (s *Service) resolveRunDocuments(ctx context.Context, record store.Run) (runResolution, error) {
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		return runResolution{}, fmt.Errorf("service: reading the repository run %s resolves configuration for: %w",
			record.ID, err)
	}
	bare, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err != nil {
		return runResolution{}, err
	}

	trusted, trustedAt, trustedBytes, err := s.trustedLayer(ctx, bare, repository, record.ID)
	if err != nil {
		return runResolution{}, err
	}
	pushed, pushedBytes, err := pushedLayer(ctx, bare, record)
	if err != nil {
		return runResolution{}, err
	}

	resolution, err := config.ResolveRun(s.global, trusted, pushed)
	if err != nil {
		return runResolution{}, fmt.Errorf("%w: resolving the configuration of run %s: %w",
			resolutionRefusal(err), record.ID, err)
	}
	rejected := make([]string, 0, len(resolution.Rejected))
	for _, rejection := range resolution.Rejected {
		s.log.Printf("run %s: configuration key dropped: %s", record.ID, rejection)
		rejected = append(rejected, rejection.String())
	}
	if !slices.Equal(resolution.Config.Agent, s.cfg.Agent) {
		return runResolution{}, fmt.Errorf("%w: the run resolves %v and this service resolved %v",
			ErrRepositoryAgent, resolution.Config.Agent, s.cfg.Agent)
	}
	if !resolution.Config.Roles.Equal(s.cfg.Roles) {
		return runResolution{}, ErrRepositoryRoles
	}
	// The curated knowledge a review is told is read at the commit the
	// trusted copy was, and never at the submitted one (knowledge.go). A run
	// under suppress_project_instructions is told none: the document is the
	// repository's own instructions, which is what that key withholds.
	var project knowledge.Knowledge
	if !resolution.Config.SuppressProjectInstructions {
		project, err = knowledgeAt(ctx, bare, trustedAt, "run "+record.ID, ErrTrustedConfig)
		if err != nil {
			return runResolution{}, err
		}
	}
	return runResolution{
		cfg:       resolution.Config,
		digest:    runConfigDigest(s.digest, trustedAt, trustedBytes, pushedBytes),
		rejected:  rejected,
		knowledge: project,
	}, nil
}

// trustedLayer fetches the default branch's tip from the upstream and reads
// the repository document at it, for run runID. A commit with no document is
// the valid no-configuration case; everything else that stops the read is a
// refusal.
func (s *Service) trustedLayer(ctx context.Context, bare *vcs.Repository, repository store.Repository,
	runID string) (config.Layer, string, []byte, error) {
	return s.trustedLayerAt(ctx, bare, repository, trustedRef(runID), "run "+runID)
}

// trustedLayerAt is trustedLayer for whoever reads a trusted copy: the fetch
// lands under ref, which the caller names after what it reads for so two
// reads cannot overwrite each other's, and every refusal names for. A copy
// that parses and sets a key this build does not act on is refused too,
// through refusalUnhonoredRepository, so every reader refuses it alike.
func (s *Service) trustedLayerAt(ctx context.Context, bare *vcs.Repository, repository store.Repository,
	ref, forWhat string) (config.Layer, string, []byte, error) {
	at, err := fetchTrusted(ctx, bare, repository, ref, forWhat, ErrTrustedConfig)
	if err != nil {
		return config.Layer{}, "", nil, err
	}
	document, err := bare.FileAt(ctx, at, config.RepositoryDocument)
	if errors.Is(err, vcs.ErrPathNotFound) {
		return config.Absent(config.OriginTrusted), at, nil, nil
	}
	if err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: reading %s at %s for %s: %w",
			ErrTrustedConfig, config.RepositoryDocument, at, forWhat, err)
	}
	layer, err := config.Parse(config.OriginTrusted, document)
	if err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: parsing %s at %s for %s: %w",
			ErrTrustedConfig, config.RepositoryDocument, at, forWhat, err)
	}
	if err := refusalUnhonoredRepository(layer); err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: %s at %s for %s: %w",
			ErrTrustedConfig, config.RepositoryDocument, at, forWhat, err)
	}
	return layer, at, document, nil
}

// fetchTrusted fetches the default branch's tip from the upstream the
// repository record names into ref in the gate repository and returns the
// commit it resolved, which is the one trusted copy every reader of one
// reads at. A record naming no default branch, a fetch that fails and a tip
// that does not resolve are each reported under class, the reader's own
// refusal, naming forWhat.
func fetchTrusted(ctx context.Context, bare *vcs.Repository, repository store.Repository,
	ref, forWhat string, class error) (string, error) {
	if repository.DefaultBranch == "" {
		return "", fmt.Errorf(
			"%w: the repository record of %s names no default branch to read it from", class, forWhat)
	}
	if err := fetchIntoGate(ctx, bare, vcs.FetchSpec{
		Remote:   repository.UpstreamURL,
		Refspecs: []string{"+refs/heads/" + repository.DefaultBranch + ":" + ref},
	}); err != nil {
		return "", fmt.Errorf("%w: fetching %s from the upstream for %s: %w",
			class, repository.DefaultBranch, forWhat, err)
	}
	at, err := bare.ResolveCommit(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("%w: resolving the fetched %s for %s: %w",
			class, repository.DefaultBranch, forWhat, err)
	}
	return at, nil
}

// taskTrustedRef is the name the gate repository holds the default branch's
// tip under while an intake or a promotion reads the posture of the task it
// is creating or promoting. It is named for the task, on trustedRef's terms,
// and deleted once read.
func taskTrustedRef(taskID string) string { return "refs/assistant/trusted/task/" + taskID }

// projectPosture is internal/tasks' ProjectPosture: the delivery posture a
// repository's tasks are held to, PRD section 10's delivery key resolved
// from the operator's layer and the repository's trusted copy at a fresh
// fetch of the default branch. The operator's layer is the loosest posture
// allowed and the trusted copy may only tighten it (config.TrustGlobalLoosens).
// It is read on the terms resolveRunConfig reads a run's trusted copy, with
// no pushed copy, because a task's posture is recorded at intake or
// promotion and no branch is under validation then: a default branch that
// cannot be fetched, a document that cannot be read or parsed, and a trusted
// copy naming a lighter posture than the operator's are ErrTrustedConfig, and
// internal/tasks refuses the intake or the promotion on it rather than
// assuming a posture. The reference the fetch landed under is
// deleted afterwards, best effort; one left behind is overwritten by the
// next read for the same task and read by nothing.
func (s *Service) projectPosture(ctx context.Context, repository store.Repository, taskID string) (config.Delivery, error) {
	bare, err := s.gateRepositoryOf(ctx, repository, "task "+taskID)
	if err != nil {
		return "", err
	}
	ref := taskTrustedRef(taskID)
	defer func() { _ = bare.DeleteRef(context.WithoutCancel(ctx), ref) }()
	trusted, _, _, err := s.trustedLayerAt(ctx, bare, repository, ref, "task "+taskID)
	if err != nil {
		return "", err
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return "", fmt.Errorf("%w: resolving the configuration of task %s: %w", resolutionRefusal(err), taskID, err)
	}
	return resolution.Config.Delivery, nil
}

// errResolution is the class of a resolution refused for a reason that names
// neither repository copy.
var errResolution = errors.New("service: the configuration could not be resolved")

// resolutionRefusal is the class a refused resolution is reported under: a
// *config.LoosenError is the copy that loosened a key only the operator's
// own file may loosen, so it is ErrTrustedConfig or ErrPushedConfig by the
// layer it names, and anything else is errResolution. The run or the intake
// is stopped on any of them, before anything launches; this only says which
// document to fix.
func resolutionRefusal(err error) error {
	var loosened *config.LoosenError
	switch {
	case errors.As(err, &loosened) && loosened.Origin == config.OriginTrusted:
		return ErrTrustedConfig
	case errors.As(err, &loosened) && loosened.Origin == config.OriginPushed:
		return ErrPushedConfig
	default:
		return errResolution
	}
}

// configRefused is a refusal of a configuration document as this service
// answers with it: on the wire it is ipc.ErrRefused, because the request was
// readable and a document it was resolved from decided against it, and its
// message is the refusal's own text with no prefix. errors.Is still reaches
// ErrTrustedConfig or ErrPushedConfig and the config refusal beneath.
type configRefused struct{ err error }

// Error is the refusal's own text.
func (c configRefused) Error() string { return c.err.Error() }

// Unwrap resolves to the wire category and to the refusal.
func (c configRefused) Unwrap() []error { return []error{ipc.ErrRefused, c.err} }

// unclassifiedRefusal is err without the wire classification refusedConfig
// gave it: a configRefused answers with the refusal beneath it, which still
// matches ErrTrustedConfig or ErrPushedConfig and the document refusal, and
// anything else is returned as it is. endRefused is its one caller, for a
// refused run whose record could not be ended.
func unclassifiedRefusal(err error) error {
	var refused configRefused
	if errors.As(err, &refused) {
		return refused.err
	}
	return err
}

// refusedConfig classifies a failure to establish a run's configuration for
// the wire, on the terms the file's opening comment states: a document that
// was read and refused becomes configRefused, and every other failure is
// returned as it is. A refusal is recognized by internal/config's and
// internal/knowledge's own refusal classes, and errUnhonored, beneath this
// file's two copy classes, so a failed fetch or read, which wraps
// ErrTrustedConfig with no document refusal beneath it, is not one.
func refusedConfig(err error) error {
	documentRefused := errors.Is(err, config.ErrMalformed) ||
		errors.Is(err, config.ErrInvalid) ||
		errors.Is(err, errUnhonored) ||
		errors.Is(err, config.ErrLoosened) ||
		errors.Is(err, knowledge.ErrTooLarge) ||
		errors.Is(err, knowledge.ErrNotText)
	switch {
	case errors.Is(err, ErrRepositoryAgent), errors.Is(err, ErrRepositoryRoles):
		return configRefused{err}
	case (errors.Is(err, ErrTrustedConfig) || errors.Is(err, ErrPushedConfig)) && documentRefused:
		return configRefused{err}
	default:
		return err
	}
}

// pushedLayer reads the repository document as the run's submitted commit
// holds it. The commit is already in the gate repository: ensureCopy fetched
// it before the run's record moved to running, and the run's submitted
// reference keeps it reachable.
func pushedLayer(ctx context.Context, bare *vcs.Repository, record store.Run) (config.Layer, []byte, error) {
	document, err := bare.FileAt(ctx, record.SubmittedHead, config.RepositoryDocument)
	if errors.Is(err, vcs.ErrPathNotFound) {
		return config.Absent(config.OriginPushed), nil, nil
	}
	if err != nil {
		return config.Layer{}, nil, fmt.Errorf("service: reading %s at the submitted %s of run %s: %w",
			config.RepositoryDocument, record.SubmittedHead, record.ID, err)
	}
	layer, err := config.Parse(config.OriginPushed, document)
	if err != nil {
		return config.Layer{}, nil, fmt.Errorf("%w: %s at the submitted %s of run %s: %w",
			ErrPushedConfig, config.RepositoryDocument, record.SubmittedHead, record.ID, err)
	}
	return layer, document, nil
}

// runConfigDigest identifies the documents one run's configuration was
// resolved from. Each part is delimited by its length, so two document sets
// cannot collide by moving bytes across a boundary, and the trusted commit is
// included so a re-read of a moved default branch digests differently even
// when the document's bytes did not change.
func runConfigDigest(operatorDigest, trustedAt string, trusted, pushed []byte) string {
	sum := sha256.New()
	for _, part := range [][]byte{[]byte(operatorDigest), []byte(trustedAt), trusted, pushed} {
		sum.Write([]byte(strconv.Itoa(len(part)) + ":"))
		sum.Write(part)
	}
	return hex.EncodeToString(sum.Sum(nil))
}
