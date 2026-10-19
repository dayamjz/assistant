package machine_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// spent is one call that reported input and output tokens, started ago
// before usageReadAt.
func spent(input, output int64, ago time.Duration) store.AgentInvocation {
	row := call("claude", "", "review", ago)
	row.InputTokens = store.Known(input)
	row.OutputTokens = store.Known(output)
	return row
}

func ceiling(input, output int64) config.UsageCeiling {
	return config.UsageCeiling{Window: 24 * time.Hour, InputTokens: input, OutputTokens: output}
}

// The comparison is over UsageOf's totals for the ceiling's window: under
// below the ceiling, reached at it and over it, so a total equal to the
// ceiling leaves no room. A call before the window is in no total, a count
// with no ceiling is not compared however large it is, and a window with no
// calls is under.
func TestCeilingOfComparesTheRecordOverItsWindow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		rows []store.AgentInvocation
		want machine.CeilingStanding
	}{
		{"no calls", nil, machine.CeilingUnder},
		{"under", []store.AgentInvocation{spent(40, 1, time.Hour), spent(59, 1, time.Hour)}, machine.CeilingUnder},
		{"at the ceiling", []store.AgentInvocation{spent(40, 1, time.Hour), spent(60, 1, time.Hour)}, machine.CeilingReached},
		{"over it", []store.AgentInvocation{spent(101, 1, time.Hour)}, machine.CeilingReached},
		{"over it before the window", []store.AgentInvocation{spent(1000, 1, 25*time.Hour)}, machine.CeilingUnder},
	} {
		got := machine.CeilingOf(ceiling(100, 0), usageReadAt, c.rows)
		if got.Standing != c.want {
			t.Errorf("%s: standing %s, want %s (%s)", c.name, got.Standing, c.want, got.Figures())
		}
		if len(got.Counts) != 1 || got.Counts[0].Count != "input_tokens" || got.Counts[0].Ceiling != 100 {
			t.Errorf("%s: counts %+v, want the one input ceiling", c.name, got.Counts)
		}
		usage := machine.UsageOf(machine.CeilingWindow(ceiling(100, 0)), usageReadAt, c.rows)
		if got.Counts[0].Recorded != usage.Total.InputTokens {
			t.Errorf("%s: compared %+v, but UsageOf totals %+v", c.name, got.Counts[0].Recorded, usage.Total.InputTokens)
		}
	}
	if got := machine.CeilingOf(ceiling(0, 0), usageReadAt, []store.AgentInvocation{spent(1e9, 1e9, time.Hour)}); got.Standing != machine.CeilingOff || len(got.Counts) != 0 {
		t.Errorf("a ceiling with no count set compared %+v", got)
	}
	both := machine.CeilingOf(ceiling(1000, 10), usageReadAt, []store.AgentInvocation{spent(5, 10, time.Hour)})
	if both.Standing != machine.CeilingReached || both.Counts[0].Standing != machine.CeilingUnder || both.Counts[1].Standing != machine.CeilingReached {
		t.Errorf("one count reached of two compared %+v", both)
	}
	if window := machine.CeilingWindow(config.Defaults().UsageCeiling).String(); window != machine.DefaultUsageWindow {
		t.Errorf("the default ceiling window reads %q, want the usage view's %q", window, machine.DefaultUsageWindow)
	}
}

// A count some call in the window left unreported is unestablished, whatever
// the calls that reported it sum to, because a count nobody reported is not
// read as none; and an unreadable record is unread, with no totals. Both
// refuse, and so does a standing outside the set, while off and under do not.
func TestCeilingOfRefusesWhatTheRecordDoesNotEstablish(t *testing.T) {
	t.Parallel()
	silent := call("claude", "", "review", time.Hour)
	got := machine.CeilingOf(ceiling(100, 0), usageReadAt, []store.AgentInvocation{spent(10, 1, time.Hour), silent})
	if got.Standing != machine.CeilingUnestablished || got.Counts[0].Recorded.Unreported != 1 {
		t.Errorf("an unreported count compared %+v", got)
	}
	if !strings.Contains(got.Figures(), "at least 10 (1 of 2 calls unreported)") {
		t.Errorf("the figures %q do not say the total is a lower bound", got.Figures())
	}
	over := machine.CeilingOf(ceiling(100, 0), usageReadAt, []store.AgentInvocation{spent(150, 1, time.Hour), silent})
	if over.Standing != machine.CeilingReached {
		t.Errorf("a reported total already past the ceiling, beside an unreported call, compared %s", over.Standing)
	}
	unread := machine.CeilingUnreadable(ceiling(100, 0), usageReadAt, errors.New("disk on fire"))
	if unread.Standing != machine.CeilingUnread || unread.Unread != "disk on fire" || unread.Counts[0].Recorded.Total.IsKnown() {
		t.Errorf("an unreadable record compared %+v", unread)
	}
	if off := machine.CeilingUnreadable(ceiling(0, 0), usageReadAt, errors.New("x")); off.Standing != machine.CeilingOff {
		t.Errorf("an unset ceiling over an unreadable record compared %s, want off", off.Standing)
	}
	noWindow := config.UsageCeiling{InputTokens: 100}
	if got := machine.CeilingOf(noWindow, usageReadAt, []store.AgentInvocation{spent(1000, 1, time.Second)}); got.Standing != machine.CeilingUnread {
		t.Errorf("a ceiling with no window compared %s, want unread", got.Standing)
	}
	refusing := map[machine.CeilingStanding]bool{
		machine.CeilingOff: false, machine.CeilingUnder: false, machine.CeilingReached: true,
		machine.CeilingUnestablished: true, machine.CeilingUnread: true, "unrecognized": true,
	}
	for standing, want := range refusing {
		if standing.Refuses() != want {
			t.Errorf("%q refuses %v, want %v", standing, standing.Refuses(), want)
		}
	}
	if !slices.Equal(machine.CeilingStandings(), []machine.CeilingStanding{machine.CeilingOff, machine.CeilingUnder,
		machine.CeilingReached, machine.CeilingUnestablished, machine.CeilingUnread}) {
		t.Errorf("CeilingStandings() = %v", machine.CeilingStandings())
	}
}

// A ceiling's count is named as the configuration key's last segment names
// it, so the comparison and the schema cannot name different counts, and
// every count the schema can set a ceiling on is one the comparison reads.
func TestEveryCeilingCountIsAKeyOfTheSchema(t *testing.T) {
	t.Parallel()
	var keyed []string
	for _, k := range config.Keys() {
		if name, ok := strings.CutPrefix(string(k), "usage_ceiling."); ok && name != "window" {
			keyed = append(keyed, name)
		}
	}
	if !slices.Equal(keyed, machine.CeilingCountNames()) {
		t.Errorf("the schema sets ceilings on %v, the comparison reads %v", keyed, machine.CeilingCountNames())
	}
}

// The hold the ceiling raises is an ask whose remedy lies outside the change,
// so P3 holds it for a person and standing consent does not answer it with a
// fix round; it carries the hold row and the exact figures, and a report of it
// validates the way the stage node holds every report to.
func TestTheCeilingHoldIsAnAskForAPersonCarryingTheFigures(t *testing.T) {
	t.Parallel()
	reached := machine.CeilingOf(ceiling(100, 0), usageReadAt, []store.AgentInvocation{spent(150, 1, time.Hour)})
	f := machine.CeilingHeldFinding(reached)
	if f.Action != findings.ActionAsk || !f.RemedyOutsideChange || !f.Holds() || f.FixEligible() {
		t.Errorf("the hold's finding is %+v, want an ask outside the change", f)
	}
	for _, part := range []string{machine.CeilingHoldPhrase(machine.CeilingReached).String(), "input_tokens 150 of 100 (reached)", "over the last 1d"} {
		if !strings.Contains(f.Description, part) {
			t.Errorf("the description %q does not carry %q", f.Description, part)
		}
	}
	report := machine.CeilingHeldReport(reached)
	if err := report.Normalize().Validate(); err != nil {
		t.Errorf("the hold's report does not validate: %v", err)
	}
	if report.Summary != machine.CeilingHoldPhrase(machine.CeilingReached).Outcome {
		t.Errorf("the summary is %q", report.Summary)
	}
	var refusal error = &machine.UsageCeilingError{Ceiling: reached}
	if !strings.Contains(refusal.Error(), reached.Figures()) {
		t.Errorf("the refusal %q does not carry the figures", refusal)
	}
	if machine.CeilingHoldPhrase("unrecognized") != machine.CeilingHoldPhrase(machine.CeilingUnread) ||
		machine.CeilingPhrase("unrecognized") != machine.CeilingPhrase(machine.CeilingUnread) {
		t.Error("a standing outside the set does not read as unread")
	}
}
