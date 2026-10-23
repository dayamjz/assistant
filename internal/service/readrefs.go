package service

import (
	"context"
	"crypto/rand"
	"strings"
)

// A read of a repository's default branch - its trusted copy at a dispatch,
// an intake, a promotion or a check of standing consent, and its curated
// knowledge at a launch - fetches the branch's tip into a reference in the
// gate repository, resolves it, reads at the commit it resolved, and removes
// the reference once read, best effort. Such a reference is the scratch of
// one read in flight and nothing a record names, and these are the
// namespaces it lives under; readNamespaces is the one list of them. A run's
// own trusted reference, trustedRef, is not one: it stays with the run and
// goes when the run is reclaimed (copy.go).
const (
	// dispatchReadNamespace holds a dispatch's read of the trusted copy
	// before a task's first claim (unhonored.go's placeable).
	dispatchReadNamespace = "refs/assistant/trusted/dispatch/"
	// postureReadNamespace holds an intake's or a promotion's read of a
	// task's delivery posture (layers.go's projectPosture).
	postureReadNamespace = "refs/assistant/trusted/task/"
	// consentReadNamespace holds one check of whether a repository allows
	// standing consent (consent.go's consentTrustedRef).
	consentReadNamespace = "refs/assistant/trusted/consent/"
	// knowledgeReadNamespace holds a launch's read of the curated
	// knowledge (knowledge.go's knowledgeRef).
	knowledgeReadNamespace = "refs/assistant/knowledge/"
)

// readNamespaces is every namespace a read's reference lives under, the
// list sweepReadRefs removes from at recovery.
var readNamespaces = []string{
	dispatchReadNamespace, postureReadNamespace, consentReadNamespace, knowledgeReadNamespace,
}

// readRef names the reference one read fetches into: under namespace, named
// for owner, what the read is for, and then for this read alone, so two reads
// for one owner never share a name and neither can update or remove the
// other's fetch between its fetch and its resolve (#552). The read's part
// follows a dash and not a slash, so no name is the directory of another, and
// a reference an earlier build left under the bare owner name blocks nothing.
func readRef(namespace, owner string) string {
	return namespace + owner + "-" + strings.ToLower(rand.Text())
}

// sweepReadRefs removes every reference under readNamespaces from the gate
// repository of every repository this home records, and writes each one it
// removes to the service's log. Recovery runs it first, before anything this
// service starts can read a default branch and before the caller serves the
// socket (Serve, after Open returns), so no request's read is in flight, and
// the home's lock keeps any other service from reading in the same
// gates, so nothing it removes is a read in flight.
//
// It is P15's restart rule for what a read's removal does not cover: a
// service that stopped between a fetch and the removal after it, and a
// removal that failed. Each such name is its read's own, so nothing later
// overwrites it, and without this sweep they would pile up a reference per
// interrupted read. Until the next open a reference left behind keeps the
// commit it names reachable in the gate, and nothing reads it. A repository
// whose gate cannot be opened, listed or written at an open keeps what it
// holds until an open that can, and each such failure is logged.
func (s *Service) sweepReadRefs(ctx context.Context) {
	repositories, err := s.store.Repositories(ctx)
	if err != nil {
		s.log.Printf("recovery could not list the repositories to remove what reads of their default branches left: %v", err)
		return
	}
	for _, repository := range repositories {
		bare, err := s.gateRepositoryOf(ctx, repository, "recovery's sweep of "+repository.ID)
		if err != nil {
			s.log.Printf("recovery could not open the gate of %s to remove what reads of its default branch left: %v",
				repository.ID, err)
			continue
		}
		left, err := bare.ListRefs(ctx, readNamespaces...)
		if err != nil {
			s.log.Printf("recovery could not list what reads of the default branch of %s left: %v", repository.ID, err)
			continue
		}
		for _, ref := range left {
			if err := bare.DeleteRef(ctx, ref.Name); err != nil {
				s.log.Printf("recovery could not remove %s, which a read of the default branch of %s left: %v",
					ref.Name, repository.ID, err)
				continue
			}
			s.log.Printf("recovery removed %s, which a read of the default branch of %s left", ref.Name, repository.ID)
		}
	}
}
