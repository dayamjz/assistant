package safety

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// ErrNotEstablished matches every *IncorporationRefusal. It is deliberately
// not ErrRefused: that sentinel means a branch update may not proceed, and a
// caller handling one is about to not push. An incorporation that could not
// be established is a different stop, one a cleanup decision reads, and a
// caller that matched the two with one check would act on the wrong question.
var ErrNotEstablished = errors.New("safety: whether the work is incorporated could not be established")

// IncorporationResult is the answer to whether a set of commits is already
// present in a tip, together with what was examined to answer it. It is only
// ever produced by Guard.Incorporation. Its fields are unexported and it
// carries no setter, so a caller cannot assemble one, and the zero value
// answers Incorporated false.
//
// The identifiers it names are the ones the check actually read, resolved in
// the local repository at the time of the check. That is PRD principle P6
// applied to a proof rather than to a push: a cleanup that records this
// result records which commits were examined against which tip, so a later
// reader can tell a proof of this work from a proof of a tip that has since
// moved.
type IncorporationResult struct {
	commits      []string
	into         string
	incorporated bool
}

// Incorporated reports whether integrating every examined commit into the
// examined tip would change nothing. The zero IncorporationResult reports
// false, so a caller that ignored the error alongside it holds no proof.
func (r IncorporationResult) Incorporated() bool { return r.incorporated }

// Commits returns the commit identifiers the check examined, in the order
// they were named, each resolved locally. Each call returns a fresh slice, so
// a holder cannot edit the record of what was examined.
func (r IncorporationResult) Commits() []string { return slices.Clone(r.commits) }

// Into returns the identifier of the tip the commits were checked against:
// the commit the caller's revision resolved to when the check ran, which is
// the identifier a caller records rather than the name it passed.
func (r IncorporationResult) Into() string { return r.into }

// String renders the result as "incorporated into <tip>" or "not incorporated
// into <tip>", with the examined commits after it.
func (r IncorporationResult) String() string {
	if r.into == "" {
		return "no incorporation result"
	}
	var b strings.Builder
	if !r.incorporated {
		b.WriteString("not ")
	}
	b.WriteString("incorporated into " + r.into + ": " + strings.Join(r.commits, ", "))
	return b.String()
}

// IncorporationRefusal reports that whether the work is incorporated could not
// be established. It is the fail-closed result: there is no
// IncorporationResult inside it, and a caller holding one has no proof of
// anything. It matches ErrNotEstablished and never ErrRefused.
type IncorporationRefusal struct {
	// Commits is the commit set as the caller named it. Resolution may have
	// failed partway, so these are names rather than identifiers.
	Commits []string
	// Into is the tip as the caller named it.
	Into string
	// Detail states what could not be established, in a sentence a caller
	// can report without adding to it.
	Detail string
	// Cause is the underlying failure, when the refusal came from one.
	Cause error
}

// Error renders the refusal with its detail.
func (r *IncorporationRefusal) Error() string {
	return "safety: incorporation into " + r.Into + " not established: " + r.Detail
}

// Unwrap makes every incorporation refusal match ErrNotEstablished and
// exposes the failure it came from alongside, so errors.Is against a
// mechanism error such as vcs.ErrInconclusive still matches.
func (r *IncorporationRefusal) Unwrap() []error {
	if r.Cause == nil {
		return []error{ErrNotEstablished}
	}
	return []error{ErrNotEstablished, r.Cause}
}

// Incorporation answers whether every commit in commits is already present in
// the tip named by into, by content, on the terms vcs.Incorporated states:
// true when integrating them would change nothing, false when it would, and
// a conflict is not an answer. It is PRD principle P12's content proof, the
// half of it that is a git fact: whether the work is in that tip. Whether the
// tip is an up-to-date default branch is the caller's to establish, by
// fetching it and passing the identifier it fetched, which is why Into
// reports the identifier the check ran against.
//
// It fails closed. An empty commit set, a revision that does not resolve
// locally, and a comparison the mechanism could not answer are each an
// *IncorporationRefusal, never a result reading false: false is a fact about
// the work, and a refusal is the absence of one. A refusal matches
// ErrNotEstablished and not ErrRefused, so no caller reads it as a push
// refusal.
//
// Every revision is resolved before the comparison, and the result names
// what was resolved, so a reference that moves after the check does not
// change what the result says was examined. The mechanism then resolves the
// tip and every commit again, by design: the identifiers this package passes
// are revisions to it like any other, so the local-resolve guarantee holds
// on both sides of the interface, and nothing may optimize that second
// resolve away.
func (g *Guard) Incorporation(ctx context.Context, commits []string, into string) (IncorporationResult, error) {
	refuse := func(detail string, cause error) (IncorporationResult, error) {
		return IncorporationResult{}, &IncorporationRefusal{
			Commits: slices.Clone(commits),
			Into:    into,
			Detail:  detail,
			Cause:   cause,
		}
	}
	if len(commits) == 0 {
		return refuse("no commit was named, so nothing was examined", nil)
	}
	tip, err := g.git.ResolveCommit(ctx, into)
	if err != nil {
		return refuse("the tip "+into+" does not resolve locally, so nothing can be checked against it", err)
	}
	ids := make([]string, 0, len(commits))
	for _, c := range commits {
		id, err := g.git.ResolveCommit(ctx, c)
		if err != nil {
			return refuse("the commit "+c+" does not resolve locally, so whether it is in "+tip+" cannot be checked", err)
		}
		ids = append(ids, id)
	}
	incorporated, err := g.git.Incorporated(ctx, ids, tip)
	if err != nil {
		return refuse("whether "+strings.Join(ids, ", ")+" are present in "+tip+" could not be determined", err)
	}
	return IncorporationResult{commits: ids, into: tip, incorporated: incorporated}, nil
}
