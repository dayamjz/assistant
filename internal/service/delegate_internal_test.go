package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

func TestMayRelaunchLocallyOnlyWhenTheAttemptIsDead(t *testing.T) {
	principles.Cite(t, principles.P6)
	if MayRelaunchLocally(store.RecoveryNone, true) || MayRelaunchLocally(store.RecoveryNone, false) {
		t.Fatal("recovery none relaunched")
	}
	if MayRelaunchLocally(store.RecoveryRelaunchIfDead, false) {
		t.Fatal("an attempt that is not recorded dead was relaunched")
	}
	if !MayRelaunchLocally(store.RecoveryRelaunchIfDead, true) {
		t.Fatal("a recorded dead attempt under relaunch-if-dead was refused")
	}
}

func TestDelegationNoteRedactsSecrets(t *testing.T) {
	note := delegationNote(errors.New("dial https://user:s3cret-token@example.test/x.git Authorization: Bearer s3cret-token"))
	if strings.Contains(note, "s3cret-token") {
		t.Fatalf("the log note kept a secret: %s", note)
	}
	if !strings.Contains(note, redact.Marker) {
		t.Fatalf("the log note = %q, want the redaction marker", note)
	}
}

func TestSubordinateMergeRefusesBeforeAnythingIsAsked(t *testing.T) {
	principles.Cite(t, principles.P14)
	s := &Service{hasGrant: true}
	err := s.mergeUnderAutonomy(t.Context(), nil, store.Run{ID: "delegated"}, passedPullRequest{
		specifier: "owner/name", number: 1, observed: "0123456789abcdef0123456789abcdef01234567",
	})
	if !errors.Is(err, ErrDelegationMerge) {
		t.Fatalf("merge = %v, want %v", err, ErrDelegationMerge)
	}
}

func TestRefuseLocalRelaunchUntilTheAttemptIsRecordedDead(t *testing.T) {
	principles.Cite(t, principles.P14, principles.P6)
	ctx := t.Context()
	records, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = records.Close() })
	s := &Service{store: records}
	child := t.TempDir()

	kept := func(id string, recovery store.DelegationRecovery, end bool) {
		t.Helper()
		if _, err := records.CreateTask(ctx, store.Task{
			ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "delegated",
			WorktreePath: filepath.Join(child, id),
		}, store.Disposition{}); err != nil {
			t.Fatal(err)
		}
		if _, err := records.RecordDelegation(ctx, id, child, recovery); err != nil {
			t.Fatal(err)
		}
		attempt, err := records.BeginTaskAttempt(ctx, id, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.refuseLocalRelaunch(ctx, id); !errors.Is(err, ErrDelegationOwns) {
			t.Fatalf("%s before an end record: %v", id, err)
		}
		if !end {
			return
		}
		if err := records.RecordDelegationAttemptEnd(ctx, id, attempt.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	kept("taskaaaaaaab", store.RecoveryNone, true)
	if err := s.refuseLocalRelaunch(ctx, "taskaaaaaaab"); !errors.Is(err, ErrDelegationOwns) {
		t.Fatalf("recovery none with a dead attempt: %v", err)
	}
	kept("taskaaaaaaac", store.RecoveryRelaunchIfDead, false)
	if err := s.refuseLocalRelaunch(ctx, "taskaaaaaaac"); !errors.Is(err, ErrDelegationOwns) {
		t.Fatalf("relaunch-if-dead without an end record: %v", err)
	}
	if err := records.RecordDelegationAttemptEnd(ctx, "taskaaaaaaac", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.refuseLocalRelaunch(ctx, "taskaaaaaaac"); err != nil {
		t.Fatalf("relaunch-if-dead with an end record: %v", err)
	}
}
