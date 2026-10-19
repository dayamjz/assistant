package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegationGrantIsWrittenOnceAndCannotWiden(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	parent := t.TempDir()
	branch := t.TempDir()
	id := "taskaaaaaaab"
	seedTask(t, s, id)
	other := "taskaaaaaaac"

	first, err := s.RecordDelegationGrant(ctx, DelegationGrant{
		ParentHome: parent, Tasks: []string{id}, ParentBranches: []string{branch},
		ParentPID: 10, ParentUID: 20, ParentGID: 30,
	})
	if err != nil {
		t.Fatalf("RecordDelegationGrant: %v", err)
	}
	if _, err := s.RecordDelegationGrant(ctx, DelegationGrant{
		ParentHome: parent, Tasks: []string{id, other}, ParentBranches: nil,
		ParentPID: 10, ParentUID: 20, ParentGID: 30,
	}); !errors.Is(err, ErrGrantRecorded) {
		t.Fatalf("widening the grant = %v, want %v", err, ErrGrantRecorded)
	}
	got, err := s.DelegationGrant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0] != id {
		t.Fatalf("grant tasks = %v, want [%s]", got.Tasks, id)
	}
	if len(got.ParentBranches) != 1 || got.ParentBranches[0] != filepath.Clean(branch) {
		t.Fatalf("parent branches = %v", got.ParentBranches)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE delegation_grant SET tasks = ? WHERE singleton = 1`, other); err == nil {
		t.Fatal("updating the grant succeeded")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM delegation_grant`); err == nil {
		t.Fatal("deleting the grant succeeded")
	}
	if err := first.RefuseGate(branch); !errors.Is(err, ErrParentGate) {
		t.Fatalf("RefuseGate(parent branch) = %v, want %v", err, ErrParentGate)
	}
	if err := first.RefuseGate(parent); !errors.Is(err, ErrParentGate) {
		t.Fatalf("RefuseGate(parent home) = %v, want %v", err, ErrParentGate)
	}
	own := t.TempDir()
	if err := first.RefuseGate(own); err != nil {
		t.Fatalf("RefuseGate(child path) = %v", err)
	}
	if _, err := s.RecordDelegationGrant(ctx, DelegationGrant{
		ParentHome: parent, Tasks: []string{id}, ParentPID: 0, ParentUID: 1, ParentGID: 1,
	}); err == nil {
		t.Fatal("a grant with no parent pid was recorded")
	}
}

func TestDelegationDeliveryAndAttemptEndAreOnce(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	id := "taskaaaaaaab"
	seedTask(t, s, id)
	child := t.TempDir()
	if _, err := s.RecordDelegation(ctx, id, child, RecoveryNone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDelegation(ctx, id, t.TempDir(), RecoveryRelaunchIfDead); !errors.Is(err, ErrDelegationRecorded) {
		t.Fatalf("second delegation = %v, want %v", err, ErrDelegationRecorded)
	}
	if _, err := s.RecordDelegationDelivery(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDelegationDelivery(ctx, id); !errors.Is(err, ErrDelegationDelivered) {
		t.Fatalf("second delivery = %v, want %v", err, ErrDelegationDelivered)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM delegation_delivery WHERE task_id = ?`, id); err == nil {
		t.Fatal("deleting a delivery succeeded")
	}
	attempt, err := s.BeginTaskAttempt(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := s.DelegationAttemptDead(ctx, id, attempt.Sequence)
	if err != nil || dead {
		t.Fatalf("before an end record, dead = %v, %v", dead, err)
	}
	if err := s.RecordDelegationAttemptEnd(ctx, id, attempt.Sequence); err != nil {
		t.Fatal(err)
	}
	dead, err = s.DelegationAttemptDead(ctx, id, attempt.Sequence)
	if err != nil || !dead {
		t.Fatalf("after an end record, dead = %v, %v", dead, err)
	}
	if err := s.RecordDelegationAttemptEnd(ctx, id, attempt.Sequence); !errors.Is(err, ErrDelegationAttemptEnded) {
		t.Fatalf("second end = %v, want %v", err, ErrDelegationAttemptEnded)
	}
	if _, err := s.RecordDelegation(ctx, id, child, DelegationRecovery("widen")); err == nil {
		t.Fatal("an unrecognized recovery was recorded")
	}
}

func TestDelegationGrantRejectsARelativeParent(t *testing.T) {
	s := openStore(t)
	_, err := s.RecordDelegationGrant(t.Context(), DelegationGrant{
		ParentHome: "relative", Tasks: []string{"taskaaaaaaab"},
		ParentPID: 1, ParentUID: 1, ParentGID: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative parent = %v", err)
	}
}
