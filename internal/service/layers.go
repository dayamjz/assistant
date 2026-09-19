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
// any of them is the guess PRD section 10 forbids. A commit whose tree holds
// no document is the one case that is not a refusal: that is a repository
// with no configuration, which is valid and resolves to the layers beneath.
//
// A pushed document that cannot be parsed is refused too, wrapped in
// ErrPushedConfig, and deliberately so even though its keys are mostly
// dropped: section 10 has invalid configuration fail at parse time even on a
// branch whose fields are otherwise ignored, because a broken rule should
// surface before it merges.

// ErrTrustedConfig reports that the trusted configuration copy could not be
// established: the default branch could not be fetched, or the document there
// could not be read or parsed. PRD section 10 stops the run before launching
// anything rather than guessing at defaults.
var ErrTrustedConfig = errors.New("service: the trusted configuration could not be read; the run is stopped rather than run on guessed defaults")

// ErrPushedConfig reports that the pushed copy of the repository
// configuration document is invalid. It is refused rather than ignored so the
// broken rule surfaces before it merges, per PRD section 10.
var ErrPushedConfig = errors.New("service: the pushed configuration document is invalid")

// ErrRepositoryAgent reports a repository configuration asking for an agent
// list this build cannot honor. This build resolves one agent per service,
// from the operator's layer, and a run whose resolved agent list differs is
// refused rather than quietly run under an agent its repository did not ask
// for. Unsetting "agent" in the repository document, or setting the same list
// in the operator's configuration, resolves it.
var ErrRepositoryAgent = errors.New("service: this build resolves one agent per service, from the operator's configuration, and this repository's configuration asks for a different agent list")

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
}

// resolveRunConfig reads the run's two repository configuration copies and
// resolves them with the operator's layer. The file's opening comment owns
// the refusals; what this adds is the order, which is trusted before pushed
// so a repository whose trusted copy is broken refuses on that rather than on
// whatever the branch carries.
func (s *Service) resolveRunConfig(ctx context.Context, record store.Run) (runResolution, error) {
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
		return runResolution{}, fmt.Errorf("service: resolving the configuration of run %s: %w", record.ID, err)
	}
	for _, rejection := range resolution.Rejected {
		s.log.Printf("run %s: configuration key dropped: %s", record.ID, rejection)
	}
	if !slices.Equal(resolution.Config.Agent, s.cfg.Agent) {
		return runResolution{}, fmt.Errorf("%w: the run resolves %v and this service resolved %v",
			ErrRepositoryAgent, resolution.Config.Agent, s.cfg.Agent)
	}
	return runResolution{
		cfg:    resolution.Config,
		digest: runConfigDigest(s.digest, trustedAt, trustedBytes, pushedBytes),
	}, nil
}

// trustedLayer fetches the default branch's tip from the upstream and reads
// the repository document at it. A commit with no document is the valid
// no-configuration case; everything else that stops the read is a refusal.
func (s *Service) trustedLayer(ctx context.Context, bare *vcs.Repository, repository store.Repository,
	runID string) (config.Layer, string, []byte, error) {
	if repository.DefaultBranch == "" {
		return config.Layer{}, "", nil, fmt.Errorf(
			"%w: the repository record of run %s names no default branch to read it from", ErrTrustedConfig, runID)
	}
	ref := trustedRef(runID)
	if err := bare.Fetch(ctx, vcs.FetchSpec{
		Remote:   repository.UpstreamURL,
		Refspecs: []string{"+refs/heads/" + repository.DefaultBranch + ":" + ref},
	}); err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: fetching %s from the upstream for run %s: %w",
			ErrTrustedConfig, repository.DefaultBranch, runID, err)
	}
	at, err := bare.ResolveCommit(ctx, ref)
	if err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: resolving the fetched %s for run %s: %w",
			ErrTrustedConfig, repository.DefaultBranch, runID, err)
	}
	document, err := bare.FileAt(ctx, at, config.RepositoryDocument)
	if errors.Is(err, vcs.ErrPathNotFound) {
		return config.Absent(config.OriginTrusted), at, nil, nil
	}
	if err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: reading %s at %s for run %s: %w",
			ErrTrustedConfig, config.RepositoryDocument, at, runID, err)
	}
	layer, err := config.Parse(config.OriginTrusted, document)
	if err != nil {
		return config.Layer{}, "", nil, fmt.Errorf("%w: parsing %s at %s for run %s: %w",
			ErrTrustedConfig, config.RepositoryDocument, at, runID, err)
	}
	return layer, at, document, nil
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
