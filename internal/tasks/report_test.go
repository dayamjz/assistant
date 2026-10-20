package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A stored report is parsed with findings.ParseReport, so P3 applies: a
// finding whose action is missing or unrecognized is an ask and becomes a
// hold, keyed by the finding's content-derived identifier, with the
// worker's words kept as its subject and detail. A fix finding is recorded
// in the stored report as evidence and becomes no hold: nothing here selects
// it for a fixer, and the type walk in cleanup_test.go holds that nothing
// reachable from the Cleaner is an agent. The report is written once, with
// an evidence copy under the task directory that is not the source.
func TestStoreReportRegistersEveryAskAsAHoldAndKeepsAFixAsEvidence(t *testing.T) {
	principles.Cite(t, principles.P3)
	s := newCleanupSubject(t, nil)
	tk := s.investigationTask(t)
	ctx := context.Background()

	const raw = `Here is what I found.

{"summary":"looked at the retry path","findings":[
 {"severity":"warning","action":"ask","location":"pkg/a.go:3","description":"should the retry be bounded?"},
 {"severity":"warning","action":"autofix","location":"pkg/c.go","description":"an action nobody recognizes"},
 {"severity":"info","location":"pkg/d.go","description":"no action at all"},
 {"severity":"warning","action":"fix","location":"pkg/b.go","description":"a typo in the comment"},
 {"severity":"info","action":"note","description":"nothing to decide"}]}`
	stored, err := s.cleaner.StoreReport(ctx, tk.id, raw)
	if err != nil {
		t.Fatalf("StoreReport: %v", err)
	}
	if len(stored.Holds) != 3 {
		t.Fatalf("registered %d holds, want one for the ask and one for each finding P3 made an ask: %+v", len(stored.Holds), stored.Holds)
	}
	for _, h := range stored.Holds {
		if !h.Open() || h.TaskID.Or("") != tk.id || !strings.HasPrefix(h.Key, "task/"+tk.id+"/finding/f-") {
			t.Errorf("hold %+v is not an open hold of the task keyed by its finding", h)
		}
		if strings.Contains(h.Subject, "typo") || strings.Contains(h.Subject, "nothing to decide") {
			t.Errorf("a fix or a note became a hold: %+v", h)
		}
	}
	if stored.Holds[0].Subject != "should the retry be bounded?" || stored.Holds[0].Detail != "pkg/a.go:3: should the retry be bounded?" {
		t.Errorf("the hold does not keep the worker's words as evidence: %+v", stored.Holds[0])
	}
	// The record holds the body as it arrived and the parsed report, and
	// the parsed report still carries the fix finding: evidence, recorded
	// and never dropped.
	record, err := s.store.TaskReport(ctx, tk.id)
	if err != nil || record.Body != raw || len(record.HoldKeys) != 3 {
		t.Fatalf("TaskReport = %+v, %v", record, err)
	}
	var parsed findings.Report
	if err := json.Unmarshal(record.Report, &parsed); err != nil {
		t.Fatalf("the stored report does not decode: %v", err)
	}
	if fixes := parsed.Fixable(); len(fixes) != 1 || fixes[0].Description != "a typo in the comment" {
		t.Fatalf("the stored report carries the fix findings %+v, want the one as evidence", fixes)
	}
	held := parsed.Held()
	if len(held) != 3 {
		t.Fatalf("the stored report holds %d findings, want 3", len(held))
	}
	for i, h := range held {
		if HoldKey(tk.id, h.ID) != record.HoldKeys[i] {
			t.Errorf("hold key %d is %s, want %s: the key is the finding's identifier", i, record.HoldKeys[i], HoldKey(tk.id, h.ID))
		}
	}
	// The evidence copy is the worker's text, beside the copy, and the row
	// is the source: the copy's removal is no change to the record.
	if text, err := os.ReadFile(stored.Evidence); err != nil || string(text) != raw {
		t.Fatalf("the evidence copy at %s reads %q (%v), want the report as filed", stored.Evidence, text, err)
	}
	if err := os.Remove(stored.Evidence); err != nil {
		t.Fatal(err)
	}
	if again, err := s.store.TaskReport(ctx, tk.id); err != nil || again.Body != raw {
		t.Fatalf("with the evidence copy gone the record reads %+v, %v", again, err)
	}
	// Written once.
	if _, err := s.cleaner.StoreReport(ctx, tk.id, raw); !errors.Is(err, store.ErrTaskReportStored) {
		t.Fatalf("a second report = %v, want ErrTaskReportStored", err)
	}
}

// A report the parser refuses stores nothing and registers nothing, and a
// report for a delivery task is refused before it is read.
func TestStoreReportRefusesWhatItCannotRead(t *testing.T) {
	s := newCleanupSubject(t, nil)
	ctx := context.Background()
	tk := s.investigationTask(t)
	if _, err := s.cleaner.StoreReport(ctx, tk.id, "no report here at all"); !errors.Is(err, ErrReportUnreadable) ||
		!errors.Is(err, findings.ErrNoReport) {
		t.Fatalf("an unreadable report = %v, want ErrReportUnreadable wrapping the parser's refusal", err)
	}
	if _, err := s.store.TaskReport(ctx, tk.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unreadable report left a record: %v", err)
	}
	if holds, err := s.store.OpenHolds(ctx); err != nil || len(holds) != 0 {
		t.Fatalf("an unreadable report registered holds: %+v, %v", holds, err)
	}
	delivery := s.deliveryTask(t, "topic")
	if _, err := s.cleaner.StoreReport(ctx, delivery.id, aReport); !errors.Is(err, ErrNotInvestigation) {
		t.Fatalf("a report for a delivery task = %v, want ErrNotInvestigation", err)
	}
	if _, err := s.cleaner.StoreReport(ctx, "nope", aReport); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a report for no task = %v, want ErrNotFound", err)
	}
}

// A refusal quoting what was filed quotes it redacted, and still says which
// refusal it is. findings.ErrNoReport quotes the filed text itself, so it is
// the refusal whose text the parse's own redaction never reaches; the
// control is the text around the credential, quoted as filed.
func TestAReportRefusalQuotesTheFiledTextRedacted(t *testing.T) {
	s := newCleanupSubject(t, nil)
	tk := s.investigationTask(t)
	const secret = "hunter2-url-password"
	_, err := s.cleaner.StoreReport(context.Background(), tk.id,
		"no report here, only https://deploy:"+secret+"@example.com/o/r.git")
	if !errors.Is(err, ErrReportUnreadable) || !errors.Is(err, findings.ErrNoReport) {
		t.Fatalf("an unreadable report = %v, want ErrReportUnreadable wrapping the parser's refusal", err)
	}
	if text := err.Error(); strings.Contains(text, secret) || !strings.Contains(text, "no report here, only https://redacted@example.com") {
		t.Fatalf("the refusal reads %q, want the filed text quoted with its credential removed", text)
	}
}

// An investigation's uncertainties and follow-ups are stored with the
// report and register no hold: StoreReport's registration reads findings
// alone. The control is the same report with one ask finding added, which
// registers exactly that hold, so the registration was asked of the report
// and found nothing to hold in the remarks. A credential the worker wrote
// into a follow-up is gone from the stored report, the body and the evidence
// copy, and an uncertainty carrying an action is refused with nothing
// stored.
func TestStoreReportKeepsUncertaintiesAndFollowUpsAndHoldsNoneOfThem(t *testing.T) {
	principles.Cite(t, principles.P3)
	const password = "hunter2-follow-up-password"
	remarks := `"uncertainties": [{"text": "whether the cache is ever cold", "location": "pkg/cache.go:12"}, "which callers rely on the order"], ` +
		`"follow_ups": ["rotate the mirror at https://deploy:` + password + `@example.com/o/r.git"]`
	ctx := context.Background()

	for _, c := range []struct {
		name     string
		findings string
		holds    []string
	}{
		{"remarks alone", `[{"severity": "info", "action": "note", "description": "the retry path is bounded"}]`, nil},
		{"with an ask", `[{"severity": "info", "action": "note", "description": "the retry path is bounded"}, ` +
			`{"severity": "warning", "action": "ask", "description": "should the old order be kept?"}]`, []string{"should the old order be kept?"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCleanupSubject(t, nil)
			tk := s.investigationTask(t)
			raw := `{"summary": "looked at the retry path", "findings": ` + c.findings + `, ` + remarks + `}`
			stored, err := s.cleaner.StoreReport(ctx, tk.id, raw)
			if err != nil {
				t.Fatalf("StoreReport: %v", err)
			}
			var subjects []string
			for _, h := range stored.Holds {
				subjects = append(subjects, h.Subject)
			}
			if strings.Join(subjects, "|") != strings.Join(c.holds, "|") {
				t.Fatalf("registered holds %q, want %q", subjects, c.holds)
			}
			open, err := s.store.OpenHolds(ctx)
			if err != nil || len(open) != len(c.holds) {
				t.Fatalf("open holds = %+v, %v, want %d", open, err, len(c.holds))
			}
			record, err := s.store.TaskReport(ctx, tk.id)
			if err != nil || len(record.HoldKeys) != len(c.holds) {
				t.Fatalf("TaskReport = %+v, %v", record, err)
			}
			var parsed findings.Report
			if err := json.Unmarshal(record.Report, &parsed); err != nil {
				t.Fatalf("the stored report does not decode: %v", err)
			}
			if len(parsed.Uncertainties) != 2 || parsed.Uncertainties[0].Text != "whether the cache is ever cold" ||
				parsed.Uncertainties[0].Location.String() != "pkg/cache.go:12" || len(parsed.FollowUps) != 1 ||
				!strings.HasPrefix(parsed.FollowUps[0].Text, "rotate the mirror at https://") {
				t.Fatalf("the stored report carries uncertainties %+v and follow-ups %+v", parsed.Uncertainties, parsed.FollowUps)
			}
			evidence, err := os.ReadFile(stored.Evidence)
			if err != nil {
				t.Fatal(err)
			}
			for what, text := range map[string]string{"stored report": string(record.Report), "body": record.Body, "evidence copy": string(evidence)} {
				if strings.Contains(text, password) {
					t.Errorf("the %s keeps the credential written into a follow-up:\n%s", what, text)
				}
			}
		})
	}

	s := newCleanupSubject(t, nil)
	tk := s.investigationTask(t)
	_, err := s.cleaner.StoreReport(ctx, tk.id,
		`{"summary": "looked", "uncertainties": [{"text": "should the old order be kept?", "action": "ask"}]}`)
	var verr *findings.ValidationError
	if !errors.Is(err, ErrReportUnreadable) || !errors.As(err, &verr) || !verr.HasDefect(findings.DefectActionOutsideFinding) {
		t.Fatalf("an uncertainty with an action = %v, want ErrReportUnreadable wrapping %s", err, findings.DefectActionOutsideFinding)
	}
	if _, err := s.store.TaskReport(ctx, tk.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused report left a record: %v", err)
	}
	if holds, err := s.store.OpenHolds(ctx); err != nil || len(holds) != 0 {
		t.Fatalf("a refused report registered holds: %+v, %v", holds, err)
	}
}

// A remark whose text is JSON null is not read as an empty string: the object
// is kept as written so normalization does not drop it. The control is the
// same shape with a string text, which is read as text. Follow-ups behave
// the same way.
func TestStoreReportKeepsRemarkWhoseTextIsJSONNullAsWritten(t *testing.T) {
	const nullUncertainty = `{"text":null,"question":"Whether retries preserve order"}`
	const readText = "Whether retries preserve order"
	ctx := context.Background()

	for _, c := range []struct {
		name  string
		raw   string
		want  string
		field string
	}{
		{
			name:  "uncertainty with null text",
			raw:   `{"summary":"looked","findings":[{"action":"note","description":"fine"}],"uncertainties":[` + nullUncertainty + `]}`,
			want:  nullUncertainty,
			field: "uncertainties",
		},
		{
			name:  "follow-up with null text",
			raw:   `{"summary":"looked","findings":[{"action":"note","description":"fine"}],"follow_ups":[` + nullUncertainty + `]}`,
			want:  nullUncertainty,
			field: "follow_ups",
		},
		{
			name:  "uncertainty with string text",
			raw:   `{"summary":"looked","findings":[{"action":"note","description":"fine"}],"uncertainties":[{"text":` + jsonString(readText) + `}]}`,
			want:  readText,
			field: "uncertainties",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCleanupSubject(t, nil)
			tk := s.investigationTask(t)
			stored, err := s.cleaner.StoreReport(ctx, tk.id, c.raw)
			if err != nil {
				t.Fatalf("StoreReport: %v", err)
			}
			record, err := s.store.TaskReport(ctx, tk.id)
			if err != nil {
				t.Fatalf("TaskReport: %v", err)
			}
			var parsed findings.Report
			if err := json.Unmarshal(record.Report, &parsed); err != nil {
				t.Fatalf("decode stored report: %v", err)
			}
			var got string
			switch c.field {
			case "uncertainties":
				if len(parsed.Uncertainties) != 1 {
					t.Fatalf("uncertainties = %+v, want one", parsed.Uncertainties)
				}
				got = parsed.Uncertainties[0].Text
			case "follow_ups":
				if len(parsed.FollowUps) != 1 {
					t.Fatalf("follow-ups = %+v, want one", parsed.FollowUps)
				}
				got = parsed.FollowUps[0].Text
			}
			if got != c.want {
				t.Fatalf("%s text = %q, want %q", c.field, got, c.want)
			}
			if !strings.Contains(record.Body, c.want) {
				t.Fatalf("body %q does not carry %q", record.Body, c.want)
			}
			evidence, err := os.ReadFile(stored.Evidence)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(evidence), c.want) {
				t.Fatalf("evidence copy does not carry %q:\n%s", c.want, evidence)
			}
		})
	}
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
