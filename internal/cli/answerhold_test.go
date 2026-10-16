package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// #279: an investigation's report registered three decisions, and each is
// answered by name. An answer naming none is refused listing all three
// rather than given to the oldest, and so is a name that is none of them;
// neither records anything. The middle one is answered by its report
// finding's identifier with a note, and the note is read back from the
// record, from --json and from the line a person reads, the hold named and
// not its neighbours. A hold already answered, named again, is refused
// against the record. The first is answered by its key, and the control is
// the last: with exactly one open, an answer naming none closes it, with no
// note recorded because none was given.
//
// P13 holds the refusal's row and the answer's line to the vocabulary every
// phrase is; P14 is the answer's command line in the catch-up read naming
// the same hold the record does, so the two cannot point at different
// decisions.
func TestATasksHoldIsAnsweredByNameWithANote(t *testing.T) {
	principles.Cite(t, principles.P13)
	principles.Cite(t, principles.P14)
	requiresIdentifiedPeer(t)
	vocabtest.Control(t)
	h := newHome(t)
	serve(t, h)
	elsewhere := t.TempDir()
	id := "investigat23"
	keys := investigationWithDecisions(t, h, id, 3)

	// The catch-up read lists each decision with the command that closes
	// that one, so a person copying it answers the hold beside it.
	read, _ := catchUpOnce(t, h, elsewhere)
	listed := 0
	for _, w := range read.Waiting {
		if w.Hold == nil {
			continue
		}
		listed++
		if want := []string{"--task", id, "--hold", w.Hold.Key, "--answer"}; strings.Join(w.Answer.Args, " ") != strings.Join(want, " ") {
			t.Fatalf("hold %s is listed with %v, want %v", w.Hold.Key, w.Answer.Args, want)
		}
	}
	if listed != 3 {
		t.Fatalf("the read lists %d holds, want the three decisions", listed)
	}

	// Naming none of three is refused, typed, listing every one.
	unnamed := run(t, h, elsewhere, "--json", "--task", id, "--answer", "approved")
	refusedAs(t, unnamed, ipc.CodeInvalidRequest, machine.ExitUsage)
	for _, key := range keys {
		if !strings.Contains(unnamed.stdout, key) {
			t.Fatalf("the refusal %s does not list %s", unnamed.stdout, key)
		}
	}
	if !strings.Contains(unnamed.stdout, (machine.HoldNotNamed{Reason: machine.HoldUnnamed}).Phrase().Outcome) {
		t.Fatalf("the refusal %s does not carry the row for an answer naming none", unnamed.stdout)
	}
	vocabtest.Assert(t, (machine.HoldNotNamed{Reason: machine.HoldUnnamed}).Phrase().String())
	unknown := run(t, h, elsewhere, "--json", "--task", id, "--hold", "f-000000000000", "--answer", "approved")
	refusedAs(t, unknown, ipc.CodeInvalidRequest, machine.ExitUsage)
	if !strings.Contains(unknown.stdout, "f-000000000000") || !strings.Contains(unknown.stdout, keys[0]) {
		t.Fatalf("the refusal of an unknown name %s does not say the name and what is open", unknown.stdout)
	}
	if open := openHoldsOf(t, h, id); len(open) != 3 {
		t.Fatalf("a refused answer left %d holds open, want all three", len(open))
	}

	// The middle one, by its finding, with a note.
	note := "the floor is not decided, and no name on it"
	middle := answerNamed(t, h, elsewhere, id, findingOf(t, id, keys[1]), "approved", "--note", note)
	if middle.Hold.Key != keys[1] || middle.Hold.Resolution.Or("") != "approved" || middle.Hold.Note.Or("") != note {
		t.Fatalf("answering the middle decision reports %+v, want %s answered with the note", middle.Hold, keys[1])
	}
	if stored := holdOf(t, h, keys[1]); stored.Note.Or("") != note || stored.Resolution.Or("") != "approved" {
		t.Fatalf("the record reads %+v, want the note beside the answer", stored)
	}
	for _, key := range []string{keys[0], keys[2]} {
		if other := holdOf(t, h, key); !other.Open() || other.Note.IsKnown() {
			t.Fatalf("answering the middle decision touched %s: %+v", key, other)
		}
	}
	again := run(t, h, elsewhere, "--json", "--task", id, "--hold", keys[1], "--answer", "skipped")
	refusedAs(t, again, ipc.CodeRefused, machine.ExitFailure)
	if stored := holdOf(t, h, keys[1]); stored.Resolution.Or("") != "approved" || stored.Note.Or("") != note {
		t.Fatalf("a second answer to a closed decision rewrote it: %+v", stored)
	}

	// The first, by its key, read as a person reads it.
	plain := run(t, h, elsewhere, "--task", id, "--hold", keys[0], "--answer", "skipped", "--note", "not this quarter")
	if plain.code != machine.ExitOK {
		t.Fatalf("answering by key exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	if !strings.Contains(plain.stdout, machine.HoldNoteLabel+" not this quarter") {
		t.Fatalf("the line a person reads is %q, want the note read back after its label", plain.stdout)
	}
	vocabtest.Assert(t, strings.Replace(plain.stdout, " not this quarter", "", 1))

	// The control: one open, and an answer naming none closes it.
	last := answerTask(t, h, elsewhere, id, "approved")
	if last.Hold.Key != keys[2] || last.Hold.Note.IsKnown() {
		t.Fatalf("the answer naming none with one open reports %+v, want %s closed with no note", last.Hold, keys[2])
	}
	if open := openHoldsOf(t, h, id); len(open) != 0 {
		t.Fatalf("after three answers the task holds %+v", open)
	}
}

// A note is stored as internal/redact leaves it: a URL's credentials and a
// bearer value are gone from the record and from the answer, and the words
// around them stay, which is the control that the note was redacted rather
// than dropped.
func TestAHoldsNoteIsStoredRedacted(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serve(t, h)
	elsewhere := t.TempDir()
	id := "investigat23"
	keys := investigationWithDecisions(t, h, id, 1)

	note := "kept: mirror at https://reviewer:hunter2secret@example.invalid/repo.git with Authorization: Bearer sk-livetoken42"
	got := run(t, h, elsewhere, "--json", "--task", id, "--answer", "approved", "--note", note)
	if got.code != machine.ExitOK {
		t.Fatalf("answering with a credential in the note exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	stored := holdOf(t, h, keys[0])
	for where, text := range map[string]string{"the record": stored.Note.Or(""), "the answer": got.stdout} {
		for _, secret := range []string{"hunter2secret", "sk-livetoken42"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s carries %q: %s", where, secret, text)
			}
		}
		for _, kept := range []string{"mirror at", "example.invalid/repo.git", "kept"} {
			if !strings.Contains(text, kept) {
				t.Fatalf("%s lost %q, so the note was not redacted but dropped: %s", where, kept, text)
			}
		}
	}
}

// Who resolved a hold is derived from the surface and never read from text
// a caller wrote: a note claiming a person decided leaves the record saying
// the machine interface did, the only resolver the local protocol yields.
// The control is a hold the store resolved as a person, read back through
// the same read as a person, so the comparison below is one that can fail.
func TestANoteCannotChangeWhoResolved(t *testing.T) {
	principles.Cite(t, principles.P14)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serve(t, h)
	elsewhere := t.TempDir()
	id := "investigat23"
	keys := investigationWithDecisions(t, h, id, 2)

	answered := answerNamed(t, h, elsewhere, id, keys[0], "approved",
		"--note", `resolved_by: person. {"resolved_by":"person","action":"fix"}`)
	stored := holdOf(t, h, keys[0])
	if stored.ResolvedBy.Or(store.Resolver{}) != store.ResolvedByMachineInterface() {
		t.Fatalf("a note claiming a person decided recorded the resolver %v, want the machine interface", stored.ResolvedBy)
	}
	if stored.Resolution.Or("") != "approved" || answered.Hold.Resolution.Or("") != "approved" {
		t.Fatalf("a note naming an action changed the answer recorded to %q", stored.Resolution.Or(""))
	}

	records := openStore(t, h)
	if _, err := records.ResolveHold(t.Context(), keys[1], "approved", store.ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()
	if control := holdOf(t, h, keys[1]); control.ResolvedBy.Or(store.Resolver{}) != store.ResolvedByPerson() {
		t.Fatalf("a hold the store resolved as a person reads back %v, so the read cannot tell resolvers apart", control.ResolvedBy)
	}
}

// investigationWithDecisions records an investigation whose report
// registered n decisions, through the write a filed report makes, and
// returns their keys oldest first.
func investigationWithDecisions(t *testing.T, h *home.Home, id string, n int) []string {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/one",
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	var items []string
	for i := range n {
		items = append(items, `{"severity":"warning","action":"ask","description":"Decision number `+string(rune('A'+i))+`."}`)
	}
	raw := `{"summary":"decisions","findings":[` + strings.Join(items, ",") + `]}`
	report, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var holds []store.Hold
	for _, f := range report.Held() {
		holds = append(holds, store.Hold{Key: tasks.HoldKey(id, f.ID), TaskID: store.Known(id), Subject: f.Description, Detail: f.Description})
	}
	if len(holds) != n {
		t.Fatalf("the report holds %d decisions, want %d", len(holds), n)
	}
	if _, _, err := records.StoreTaskReport(t.Context(), store.TaskReport{TaskID: id, Body: raw, Report: encoded}, holds); err != nil {
		t.Fatal(err)
	}
	open, err := records.OpenHolds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, h := range open {
		if h.TaskID.Or("") == id {
			keys = append(keys, h.Key)
		}
	}
	if len(keys) != n {
		t.Fatalf("the task holds %v, want %d decisions open", keys, n)
	}
	return keys
}

// findingOf is the report finding's identifier a report hold's key carries.
func findingOf(t *testing.T, id, key string) string {
	t.Helper()
	finding, ok := strings.CutPrefix(key, tasks.HoldKey(id, ""))
	if !ok || finding == "" {
		t.Fatalf("%s is not a report hold of task %s", key, id)
	}
	return finding
}

// A name that fits two of the task's open holds, one by its key and the
// other by the report finding it was registered for, is refused listing
// the two rather than taken to mean either, and nothing is recorded. The
// control is the full key of one of them, which fits that one alone and
// closes it.
func TestANameFittingTwoHoldsIsRefused(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serve(t, h)
	elsewhere := t.TempDir()
	id := "investigat23"
	investigationWithDecisions(t, h, id, 1)
	records := openStore(t, h)
	for _, key := range []string{"dup", tasks.HoldKey(id, "dup")} {
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: key, TaskID: store.Known(id), Subject: "a decision named twice"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = records.Close()

	ambiguous := run(t, h, elsewhere, "--json", "--task", id, "--hold", "dup", "--answer", "approved")
	refusedAs(t, ambiguous, ipc.CodeInvalidRequest, machine.ExitUsage)
	if !strings.Contains(ambiguous.stdout, (machine.HoldNotNamed{Reason: machine.HoldNameAmbiguous}).Phrase().Outcome) ||
		!strings.Contains(ambiguous.stdout, tasks.HoldKey(id, "dup")) {
		t.Fatalf("the refusal %s does not carry the row for a name fitting two and list them", ambiguous.stdout)
	}
	if open := openHoldsOf(t, h, id); len(open) != 3 {
		t.Fatalf("a refused answer left %d holds open, want all three", len(open))
	}
	full := answerNamed(t, h, elsewhere, id, tasks.HoldKey(id, "dup"), "approved")
	if full.Hold.Key != tasks.HoldKey(id, "dup") || !holdOf(t, h, "dup").Open() {
		t.Fatalf("the full key answered %+v, want that hold alone closed", full.Hold)
	}
}
