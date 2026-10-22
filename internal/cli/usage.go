package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// readOutUsage prints PRD section 9's usage view, which that section names a
// detailed evidence document in P13's sense: its groups are agent names,
// model identifiers, purposes, run identifiers and stage names, and they are
// printed as the invocation record holds them. It is reached through the
// renderings table's evidence row, so the phrase that points at it, which
// says what it leaves out, is written first.
//
// A count no call in a group reported prints as unreported, and a total over
// some of a group's calls says how many it leaves out, so no line reads a
// missing count as a zero. Every field is the record's text and is escaped,
// since a model identifier can be what an agent reported.
func readOutUsage(w io.Writer, u machine.Usage) {
	writef(w, "Window     %s: calls started since %s, read at %s\n",
		printable(u.Window), u.Since.UTC().Format(time.RFC3339), u.ReadAt.UTC().Format(time.RFC3339))
	writef(w, "Counts     %s; not %s\n", printable(strings.Join(u.Covers, ", ")), printable(strings.Join(u.Excludes, ", ")))
	writeUsageCeiling(w, u.Ceiling)
	writeUsageTotals(w, "Total", u.Total)
	if u.Total.Calls == 0 {
		return
	}
	writeln(w, "\nBy agent, model, purpose, role, provider, type, version, effort and context window")
	for _, g := range u.ByAgent {
		model := g.Model
		if model == "" {
			model = "(no model named)"
		}
		writeUsageTotals(w, fmt.Sprintf("  %s  %s  %s  role %s  %s %s %s  effort %s  context %s",
			printable(g.Agent), printable(model), printable(g.Purpose),
			namedDimension(g.Role), namedDimension(g.Provider), namedDimension(g.Type), namedDimension(g.Version),
			namedDimension(g.Effort), namedDimension(g.ContextWindow)), g.UsageTotals)
	}
	writeln(w, "\nBy run and stage")
	for _, g := range u.ByRun {
		writeUsageTotals(w, fmt.Sprintf("  run %s  stage %s", printable(g.Run.String()), printable(g.Stage.String())), g.UsageTotals)
	}
}

// writeUsageCeiling prints the operator's usage ceiling as the answer
// compared it: internal/machine's row for its standing, which a standing the
// row table does not know reads as unread rather than as room, then the exact
// figures when a count is set, over the ceiling's own window, which need not
// be the window the view was asked for.
func writeUsageCeiling(w io.Writer, c machine.UsageCeiling) {
	writef(w, "Ceiling    %s\n", machine.CeilingPhrase(c.Standing))
	if len(c.Counts) > 0 || c.Unread != "" {
		writef(w, "           %s\n", printable(c.Figures()))
	}
}

// writeUsageTotals prints one group's totals under its heading: the calls,
// failures and time on one line, and the token counts on the next.
func writeUsageTotals(w io.Writer, heading string, t machine.UsageTotals) {
	writef(w, "%s\n", heading)
	writef(w, "      calls %d, failed %s, took %s\n", t.Calls, failures(t.Failures), t.Duration.Round(time.Millisecond))
	if t.Calls == 0 {
		return
	}
	writef(w, "      input %s, output %s, cache read %s, cache written %s\n",
		usageCount(t.InputTokens), usageCount(t.OutputTokens),
		usageCount(t.CacheReadTokens), usageCount(t.CacheCreationTokens))
}

// failures prints the failed calls by category, in category order, or none.
func failures(byCategory map[string]int) string {
	if len(byCategory) == 0 {
		return "none"
	}
	categories := make([]string, 0, len(byCategory))
	total := 0
	for category, n := range byCategory {
		categories = append(categories, category)
		total += n
	}
	sort.Strings(categories)
	parts := make([]string, 0, len(categories))
	for _, category := range categories {
		parts = append(parts, fmt.Sprintf("%s %d", printable(category), byCategory[category]))
	}
	return fmt.Sprintf("%d (%s)", total, strings.Join(parts, ", "))
}

// namedDimension prints one role attribution: unknown when the row predates
// the column, and not named when the launch left it empty.
func namedDimension(v store.Optional[string]) string {
	text, ok := v.Get()
	if !ok {
		return "unknown"
	}
	if strings.TrimSpace(text) == "" {
		return "(not named)"
	}
	return printable(text)
}

// usageCount prints one token count: unreported when no call reported it, the
// total alone when every call did, and the total with how many calls it
// leaves out otherwise.
func usageCount(c machine.UsageCount) string {
	total, ok := c.Total.Get()
	if !ok {
		return "unreported"
	}
	if c.Unreported == 0 {
		return fmt.Sprint(total)
	}
	return fmt.Sprintf("%d (%d of %d calls unreported)", total, c.Unreported, c.Reported+c.Unreported)
}
