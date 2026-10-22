package machine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/store"
)

// CeilingStanding is where recorded agent use stands against the operator's
// usage ceiling, config.UsageCeiling. It is a closed set, and three of its
// members refuse a start: a ceiling reached, a total the record does not
// establish, and a record that could not be read. A ceiling is never read as
// passed for want of a figure.
type CeilingStanding string

const (
	// CeilingOff is a ceiling with no count set, which compares nothing.
	CeilingOff CeilingStanding = "off"
	// CeilingUnder is every set count's recorded total below its ceiling.
	CeilingUnder CeilingStanding = "under"
	// CeilingReached is a set count whose recorded total has reached its
	// ceiling, equal included: the ceiling is how much may be spent, so a
	// total at it leaves nothing for another call.
	CeilingReached CeilingStanding = "reached"
	// CeilingUnestablished is a set count that some call in the window left
	// unreported, so its total is a lower bound and not the total. A count
	// nobody reported is never read as none.
	CeilingUnestablished CeilingStanding = "unestablished"
	// CeilingUnread is a record that could not be read, so no total is known.
	CeilingUnread CeilingStanding = "unread"
)

// CeilingStandings is the closed set, in the order above.
func CeilingStandings() []CeilingStanding {
	return []CeilingStanding{CeilingOff, CeilingUnder, CeilingReached, CeilingUnestablished, CeilingUnread}
}

// Refuses reports whether this standing refuses another agent call. A
// standing outside the set refuses, so nothing unrecognized reads as room.
func (c CeilingStanding) Refuses() bool { return c != CeilingOff && c != CeilingUnder }

// UsageCeiling is the operator's usage ceiling compared against the agent
// invocation record: the one answer the service's refusal, the hold it
// raises, and the usage view all carry, so every surface that reports spend
// against the ceiling reports the totals UsageOf computed (P14). It is
// recomputed on every read from the rows and kept nowhere (P8).
//
// It is not a loop bound. It is compared against the home's record across
// every run rather than against a run's checkpoint, and it parks nothing:
// internal/service holds the stage that needed the refused call.
type UsageCeiling struct {
	// Standing is where the comparison stands, the strictest of Counts'.
	Standing CeilingStanding `json:"standing"`
	// Window is the ceiling's window in the usage view's syntax.
	Window string `json:"window"`
	// Since is when the window began, measured back from ReadAt.
	Since time.Time `json:"since"`
	// ReadAt is the instant the comparison was measured back from.
	ReadAt time.Time `json:"read_at"`
	// Counts is one entry per token count the operator set a ceiling on, in
	// the order of UsageTotals' counts. It is empty when the ceiling is off.
	Counts []UsageCeilingCount `json:"counts"`
	// Unread is why the record could not be read, set only on CeilingUnread.
	Unread string `json:"unread,omitempty"`
}

// UsageCeilingCount is one set count's comparison.
type UsageCeilingCount struct {
	// Count names the token count, as UsageTotals' JSON names it and as the
	// configuration key's last segment names it.
	Count string `json:"count"`
	// Ceiling is the operator's ceiling on it, which is positive.
	Ceiling int64 `json:"ceiling"`
	// Recorded is UsageOf's total of it over the window.
	Recorded UsageCount `json:"recorded"`
	// Standing is under, reached, or unestablished.
	Standing CeilingStanding `json:"standing"`
}

// ceilingCounts is the one table joining a configured ceiling to the count
// UsageOf totals. Its names are UsageTotals' JSON names, and a ceiling's key
// is "usage_ceiling." followed by the same name, which the test holds to the
// configuration schema.
var ceilingCounts = []struct {
	name     string
	ceiling  func(config.UsageCeiling) int64
	recorded func(UsageTotals) UsageCount
}{
	{"input_tokens", func(c config.UsageCeiling) int64 { return c.InputTokens },
		func(t UsageTotals) UsageCount { return t.InputTokens }},
	{"output_tokens", func(c config.UsageCeiling) int64 { return c.OutputTokens },
		func(t UsageTotals) UsageCount { return t.OutputTokens }},
	{"cache_read_tokens", func(c config.UsageCeiling) int64 { return c.CacheReadTokens },
		func(t UsageTotals) UsageCount { return t.CacheReadTokens }},
	{"cache_creation_tokens", func(c config.UsageCeiling) int64 { return c.CacheCreationTokens },
		func(t UsageTotals) UsageCount { return t.CacheCreationTokens }},
}

// CeilingCountNames is the names of the counts a ceiling may be set on, in
// ceilingCounts' order.
func CeilingCountNames() []string {
	names := make([]string, len(ceilingCounts))
	for i, c := range ceilingCounts {
		names[i] = c.name
	}
	return names
}

// CeilingWindow is the usage window a ceiling sums over: its configured span,
// written in whole days where it is one, the way the usage view's own
// default is written.
func CeilingWindow(c config.UsageCeiling) UsageWindow {
	text := c.Window.String()
	if day := 24 * time.Hour; c.Window > 0 && c.Window%day == 0 {
		text = fmt.Sprintf("%dd", c.Window/day)
	}
	return UsageWindow{text: text, span: c.Window}
}

// CeilingOf compares c against rows, read at readAt. The totals are
// UsageOf's over CeilingWindow(c), so a row whose call started before the
// window is not counted and a caller may hand over more than the window.
//
// A count is reached when its recorded total is at or over its ceiling, and
// unestablished when any call in the window left it unreported, whatever the
// reported calls sum to. A window with no calls is under every positive
// ceiling. The standing is reached when any count is, else unestablished when
// any count is, else under; a ceiling with no count set is off.
//
// A ceiling with a count set and no positive window, which config.Resolve
// never produces, cannot be compared and reads as unread rather than as a
// window holding no calls.
func CeilingOf(c config.UsageCeiling, readAt time.Time, rows []store.AgentInvocation) UsageCeiling {
	if c.Set() && c.Window <= 0 {
		return CeilingUnreadable(config.UsageCeiling{Window: time.Nanosecond, InputTokens: c.InputTokens,
			OutputTokens: c.OutputTokens, CacheReadTokens: c.CacheReadTokens, CacheCreationTokens: c.CacheCreationTokens},
			readAt, errNoCeilingWindow)
	}
	window := CeilingWindow(c)
	usage := UsageOf(window, readAt, rows)
	out := UsageCeiling{Standing: CeilingOff, Window: usage.Window, Since: usage.Since, ReadAt: usage.ReadAt,
		Counts: []UsageCeilingCount{}}
	reached, unestablished := false, false
	for _, row := range ceilingCounts {
		limit := row.ceiling(c)
		if limit <= 0 {
			continue
		}
		recorded := row.recorded(usage.Total)
		count := UsageCeilingCount{Count: row.name, Ceiling: limit, Recorded: recorded, Standing: CeilingUnder}
		switch {
		case recorded.Total.Or(0) >= limit:
			count.Standing, reached = CeilingReached, true
		case recorded.Unreported > 0:
			count.Standing, unestablished = CeilingUnestablished, true
		}
		out.Counts = append(out.Counts, count)
	}
	switch {
	case len(out.Counts) == 0:
		out.Standing = CeilingOff
	case reached:
		out.Standing = CeilingReached
	case unestablished:
		out.Standing = CeilingUnestablished
	default:
		out.Standing = CeilingUnder
	}
	return out
}

// CeilingUnreadable is the comparison when the record could not be read:
// unread, naming why, with no totals. A ceiling with no count set reads
// nothing and is off, so it is never unread.
func CeilingUnreadable(c config.UsageCeiling, readAt time.Time, err error) UsageCeiling {
	out := CeilingOf(c, readAt, nil)
	if out.Standing == CeilingOff {
		return out
	}
	out.Standing = CeilingUnread
	out.Unread = err.Error()
	for i := range out.Counts {
		out.Counts[i].Recorded = UsageCount{}
		out.Counts[i].Standing = CeilingUnread
	}
	return out
}

// Figures is the comparison in exact terms, for the hold's finding and the
// usage view: the window, then each set count's recorded total against its
// ceiling. A total some calls did not report says how many, so it is never
// read as the whole. It names no instant: the finding it goes into is graph
// state, which the convergence bound reads as the run's fingerprint, and a
// time there would differ on every execution; Since and ReadAt travel in the
// structured answer instead.
func (u UsageCeiling) Figures() string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage ceiling %s over the last %s", u.Standing, u.Window)
	for _, c := range u.Counts {
		fmt.Fprintf(&b, "; %s %s of %d (%s)", c.Count, recordedText(c.Recorded), c.Ceiling, c.Standing)
	}
	if u.Unread != "" {
		fmt.Fprintf(&b, "; the record could not be read: %s", u.Unread)
	}
	return b.String()
}

// recordedText is one recorded count as Figures writes it.
func recordedText(c UsageCount) string {
	total, ok := c.Total.Get()
	switch {
	case c.Unreported == 0 && !ok:
		return "0"
	case c.Unreported == 0:
		return fmt.Sprint(total)
	}
	return fmt.Sprintf("at least %d (%d of %d calls unreported)", c.Total.Or(0), c.Unreported, c.Reported+c.Unreported)
}

// errNoCeilingWindow is why a ceiling with no positive window reads as unread.
var errNoCeilingWindow = errors.New("the ceiling names no positive window to sum the record over")

// UsageCeilingError is internal/service's refusal to start an agent call,
// carrying the comparison that refused it. It is returned before anything
// starts, so the call it names never ran and is in no record.
type UsageCeilingError struct {
	// Ceiling is the comparison, whose Standing refuses.
	Ceiling UsageCeiling
}

// Error is the refusal in exact terms.
func (e *UsageCeilingError) Error() string {
	return "the agent call was not started: " + e.Ceiling.Figures()
}

// UsageCeilingFindingID is the identifier of the finding CeilingHeldFinding
// builds, within the report of the stage it holds.
const UsageCeilingFindingID = "usage-ceiling"

// CeilingHeldFinding is the finding that holds a stage whose agent call the
// ceiling refused: an ask, because a person decides, and a remedy outside
// the change, because no edit to the change raises a ceiling, so standing
// consent leaves it for a person rather than spending a fix round on it. It
// is marked as work that never started, findings.Finding.NotStarted, which
// is what offers the stage's hold PRD section 5's retry once there is room. Its
// summary is the hold row's outcome and its description is the whole row
// followed by the comparison's figures, so the hold's detail and the usage
// view carry one set of totals.
func CeilingHeldFinding(u UsageCeiling) findings.Finding {
	phrase := CeilingHoldPhrase(u.Standing)
	return findings.Finding{
		ID:                  UsageCeilingFindingID,
		Severity:            findings.SeverityError,
		Action:              findings.ActionAsk,
		RemedyOutsideChange: true,
		NotStarted:          true,
		Description:         phrase.String() + " " + capitalize(u.Figures()) + ".",
	}
}

// CeilingHeldReport is the report of a stage whose body could not run its
// agent call because the ceiling refused it: CeilingHeldFinding alone, under
// the hold row's outcome as its summary.
func CeilingHeldReport(u UsageCeiling) findings.Report {
	return findings.Report{
		Summary:  CeilingHoldPhrase(u.Standing).Outcome,
		Findings: []findings.Finding{CeilingHeldFinding(u)},
	}
}

// capitalize upper-cases the first letter of s.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
