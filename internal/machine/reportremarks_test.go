package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// A stored report's uncertainties and follow-ups reach a person as a count
// row for each list that has entries, through the phrase table, and never as
// the worker's text (P13). A task with no report and a report with neither
// list has no row, and each row the table lists is the one printed.
func TestAReportsRemarksAreCountedAndNeverRelayed(t *testing.T) {
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	const uncertain, further = "whether the cache is ever cold", "measure the retry budget"
	task := machine.Task{Report: &machine.InvestigationReport{
		Summary:       "looked",
		Uncertainties: findings.RemarkList{{Text: uncertain}, {Text: "which callers rely on the order"}},
		FollowUps:     findings.RemarkList{{Text: further}},
	}}
	rows := machine.ReportRemarkPhrases(task)
	if len(rows) != 2 || rows[0] != machine.UncertaintiesPhrase(2) || rows[1] != machine.FollowUpsPhrase(1) {
		t.Fatalf("rows = %+v, want the uncertainties row counting 2 and the follow-ups row counting 1", rows)
	}
	for _, row := range rows {
		text := row.String()
		vocabtest.Assert(t, text)
		if strings.Contains(text, uncertain) || strings.Contains(text, further) {
			t.Fatalf("the row %q relays a remark", text)
		}
	}
	if !strings.Contains(rows[0].String(), "2 ") || !strings.Contains(rows[1].String(), "1 ") {
		t.Fatalf("the rows do not carry the counts: %q", rows)
	}
	listed := strings.Join(machine.Phrases(), "\n")
	for _, row := range []machine.Phrase{machine.UncertaintiesPhrase(2), machine.FollowUpsPhrase(1)} {
		if !strings.Contains(listed, row.String()) {
			t.Fatalf("the table does not list %q", row)
		}
	}

	followOnly := machine.Task{Report: &machine.InvestigationReport{Summary: "looked", FollowUps: task.Report.FollowUps}}
	if rows := machine.ReportRemarkPhrases(followOnly); len(rows) != 1 || rows[0] != machine.FollowUpsPhrase(1) {
		t.Fatalf("a report with follow-ups alone reads %+v", rows)
	}
	for name, none := range map[string]machine.Task{
		"no report":       {},
		"neither list":    {Report: &machine.InvestigationReport{Summary: "looked"}},
		"two empty lists": {Report: &machine.InvestigationReport{Summary: "looked", Uncertainties: findings.RemarkList{}, FollowUps: findings.RemarkList{}}},
	} {
		if rows := machine.ReportRemarkPhrases(none); len(rows) != 0 {
			t.Errorf("%s reads %+v, want no rows", name, rows)
		}
	}
}
