package machine

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
)

// Usage is PRD section 9's usage view: what the agent calls recorded in a
// window cost, read from section 8's agent invocation record and totalled by
// agent, model, purpose, role, model provider, type, version and effort, and
// by run and stage. The person's rendering and
// the machine interface are this one shape (P14), and a driving agent reaches
// it through the open usage.read method rather than through a second model.
//
// It is a detailed evidence document in P13's sense: its groups carry agent
// names, model identifiers, purposes, run identifiers and stage names as the
// record holds them. The line that points a person at it is a phrase-table
// row, PhraseUsageFollows.
//
// It totals the record and restates none of it: every figure is a sum or a
// count over store.AgentInvocation rows, recomputed on every read, and
// nothing here or in the store keeps a summary beside the rows. What the
// record does not hold this does not report either: Covers and Excludes say
// so, because the calls a stage makes are the only ones the record is fed,
// and a worker's turns and any price are not in it.
type Usage struct {
	// Window is the window the read covers, in the syntax ParseUsageWindow
	// reads, with the default filled in when the request named none.
	Window string `json:"window"`
	// Since is when the window began: a call is in it when it started at or
	// after this instant.
	Since time.Time `json:"since"`
	// ReadAt is the instant the window was measured back from. It bounds
	// nothing on its own: a call recorded with a later start is still in the
	// window, since a window is over when a call started and a clock can be
	// stepped.
	ReadAt time.Time `json:"read_at"`
	// Covers names what the totals count, which is UsageCovers.
	Covers []string `json:"covers"`
	// Excludes names what the totals do not count, which is UsageExcludes.
	Excludes []string `json:"excludes"`
	// Total is every call in the window.
	Total UsageTotals `json:"total"`
	// ByAgent is the calls grouped by agent, model, purpose, role, provider,
	// type, version and effort, in the order each group's first call was
	// recorded. A provider, type, version, role, or effort that is unknown
	// is not the same group as one recorded as not named, and neither is a
	// group that named a value.
	ByAgent []UsageByAgent `json:"by_agent"`
	// ByRun is the calls grouped by the run and stage they served, in the
	// order each group's first call was recorded. A call whose row names
	// neither, which is every row written before the record gained them and
	// any call made outside a run, is grouped under an unknown run and stage
	// rather than guessed into one.
	ByRun []UsageByRun `json:"by_run"`
	// Ceiling is the operator's usage ceiling compared against the same rows
	// over its own window, which UsageViewOf fills and UsageOf leaves zero.
	// Its totals are UsageOf's, so the view and the refusal the ceiling
	// raises report one total.
	Ceiling UsageCeiling `json:"ceiling"`
}

// UsageCovers and UsageExcludes are what Usage.Covers and Usage.Excludes say,
// in tokens a driving agent can compare: the agent calls validation stages
// made, which is everything the invocation record is fed, and not a task
// worker's turns or any price, which nothing in this build records there.
var (
	UsageCovers   = []string{"stage-agent-calls"}
	UsageExcludes = []string{"worker-turns", "price"}
)

// UsageTotals is what a set of calls cost. Calls and the failure counts are
// counts of rows, and Duration is the sum of every call's duration, both
// known for every row. Each token count is a UsageCount, because an agent may
// not report one and an unreported count is never summed as a zero.
type UsageTotals struct {
	// Calls is how many calls there were.
	Calls int `json:"calls"`
	// Failures counts the calls that failed, by failure category as
	// internal/agents names it. A call that produced a result is not in it,
	// and a set with no failed call has an empty map rather than none.
	Failures map[string]int `json:"failures"`
	// Duration is how long the calls took in total.
	Duration time.Duration `json:"duration_ns"`
	// InputTokens is the prompt tokens the agent reported.
	InputTokens UsageCount `json:"input_tokens"`
	// OutputTokens is the tokens the agent generated.
	OutputTokens UsageCount `json:"output_tokens"`
	// CacheReadTokens is prompt tokens served from a cache.
	CacheReadTokens UsageCount `json:"cache_read_tokens"`
	// CacheCreationTokens is prompt tokens written to a cache.
	CacheCreationTokens UsageCount `json:"cache_creation_tokens"`
}

// UsageCount is one token count over a set of calls, kept apart by whether
// each call reported it. Total sums only the calls that did, and is unknown
// when none did, so a count no agent reported reads back as unknown rather
// than as a zero; Unreported says how many calls the total leaves out, so a
// total over some calls is never read as one over all of them.
type UsageCount struct {
	// Total is the sum over the calls that reported the count, unknown when
	// none of them did.
	Total store.Optional[int64] `json:"total"`
	// Reported is how many calls reported it.
	Reported int `json:"reported"`
	// Unreported is how many calls did not.
	Unreported int `json:"unreported"`
}

// add counts one call's value for this count.
func (c *UsageCount) add(v store.Optional[int64]) {
	n, ok := v.Get()
	if !ok {
		c.Unreported++
		return
	}
	c.Reported++
	c.Total = store.Known(c.Total.Or(0) + n)
}

// add counts one call.
func (t *UsageTotals) add(inv store.AgentInvocation) {
	if t.Failures == nil {
		t.Failures = map[string]int{}
	}
	t.Calls++
	if inv.Failure != "" {
		t.Failures[inv.Failure]++
	}
	t.Duration += inv.Duration
	t.InputTokens.add(inv.InputTokens)
	t.OutputTokens.add(inv.OutputTokens)
	t.CacheReadTokens.add(inv.CacheReadTokens)
	t.CacheCreationTokens.add(inv.CacheCreationTokens)
}

// UsageByAgent is the calls one agent made with one model, for one purpose,
// under one role and one model triple and effort. Model is empty where
// neither the invocation nor the agent named one. Role, Provider, Type,
// Version, and Effort are unknown where the row predates the column, and
// known empty where the launch did not name that one.
type UsageByAgent struct {
	// Agent is the agent's name.
	Agent string `json:"agent"`
	// Model is the model, empty when none was named.
	Model string `json:"model"`
	// Purpose is the role the calls played.
	Purpose string `json:"purpose"`
	// Role is the configured role the launch ran under.
	Role store.Optional[string] `json:"role"`
	// Provider is the model provider the launch was asked for.
	Provider store.Optional[string] `json:"provider"`
	// Type is the model type the launch was asked for.
	Type store.Optional[string] `json:"type"`
	// Version is the model version the launch was asked for.
	Version store.Optional[string] `json:"version"`
	// Effort is the effort the launch was asked for.
	Effort store.Optional[string] `json:"effort"`
	UsageTotals
}

// UsageByRun is the calls that served one run's one stage. Run and Stage are
// unknown together for the calls whose rows name neither; a row naming one
// and not the other is grouped by what it names, with the other unknown.
type UsageByRun struct {
	// Run is the run the calls served, unknown where the row names none.
	Run store.Optional[string] `json:"run"`
	// Stage is the stage the calls served, unknown where the row names none.
	Stage store.Optional[string] `json:"stage"`
	UsageTotals
}

// UsageOf totals rows into the usage view over window, measured back from
// readAt. A row whose call started before the window began is not counted,
// so a caller may hand over more than the window and get the window back.
//
// It reads every row it is handed and keeps nothing between calls, so the
// view is the record's as it stands at the read and never a summary that
// could fall behind it (P8).
func UsageOf(window UsageWindow, readAt time.Time, rows []store.AgentInvocation) Usage {
	since := window.Since(readAt)
	usage := Usage{
		Window:   window.String(),
		Since:    since,
		ReadAt:   readAt,
		Covers:   append([]string(nil), UsageCovers...),
		Excludes: append([]string(nil), UsageExcludes...),
		Total:    UsageTotals{Failures: map[string]int{}},
		ByAgent:  []UsageByAgent{},
		ByRun:    []UsageByRun{},
	}
	type agentKey struct {
		agent, model, purpose                string
		role, provider, typ, version, effort store.Optional[string]
	}
	type runKey struct{ run, stage store.Optional[string] }
	agents := map[agentKey]int{}
	runs := map[runKey]int{}
	for _, inv := range rows {
		if inv.Started.Before(since) {
			continue
		}
		usage.Total.add(inv)

		ak := agentKey{inv.Agent, inv.Model, inv.Purpose, inv.Role, inv.Provider, inv.Type, inv.Version, inv.Effort}
		i, ok := agents[ak]
		if !ok {
			i = len(usage.ByAgent)
			agents[ak] = i
			usage.ByAgent = append(usage.ByAgent, UsageByAgent{
				Agent: inv.Agent, Model: inv.Model, Purpose: inv.Purpose,
				Role: inv.Role, Provider: inv.Provider, Type: inv.Type, Version: inv.Version, Effort: inv.Effort,
			})
		}
		usage.ByAgent[i].add(inv)

		rk := runKey{inv.Run, inv.Stage}
		j, ok := runs[rk]
		if !ok {
			j = len(usage.ByRun)
			runs[rk] = j
			usage.ByRun = append(usage.ByRun, UsageByRun{Run: inv.Run, Stage: inv.Stage})
		}
		usage.ByRun[j].add(inv)
	}
	return usage
}

// UsageViewOf is the usage view as usage.read answers it: UsageOf over
// window, with the operator's ceiling compared against the same rows by
// CeilingOf. rows must reach back to the earlier of the two windows' starts,
// which UsageViewSince says.
func UsageViewOf(window UsageWindow, ceiling config.UsageCeiling, readAt time.Time, rows []store.AgentInvocation) Usage {
	usage := UsageOf(window, readAt, rows)
	usage.Ceiling = CeilingOf(ceiling, readAt, rows)
	return usage
}

// UsageViewSince is how far back the rows UsageViewOf is handed must reach:
// the earlier of window's start and the ceiling's.
func UsageViewSince(window UsageWindow, ceiling config.UsageCeiling, readAt time.Time) time.Time {
	since := window.Since(readAt)
	if c := CeilingWindow(ceiling).Since(readAt); c.Before(since) {
		since = c
	}
	return since
}

// DefaultUsageWindow is the window a usage read covers when the request names
// none: the last seven days.
const DefaultUsageWindow = "7d"

// ErrUsageWindow is the refusal for a window ParseUsageWindow cannot read.
var ErrUsageWindow = errors.New("a usage window is a positive span such as 24h, 90m or 7d")

// UsageWindow is how far back a usage read reaches. The zero value is not a
// window; ParseUsageWindow is the way to get one from text, and CeilingWindow
// the way to get the configured usage ceiling's.
type UsageWindow struct {
	text string
	span time.Duration
}

// days is a whole number of days, the one unit time.ParseDuration does not
// read.
var days = regexp.MustCompile(`^([0-9]+)d$`)

// ParseUsageWindow reads a window: a whole number of days written as 7d, or
// anything time.ParseDuration reads, such as 24h, 90m or 1h30m. Empty is
// DefaultUsageWindow. A span that is not positive, or too long to measure
// back from now, is refused with ErrUsageWindow.
func ParseUsageWindow(text string) (UsageWindow, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = DefaultUsageWindow
	}
	var span time.Duration
	if m := days.FindStringSubmatch(text); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > int64(math.MaxInt64/(24*time.Hour)) {
			return UsageWindow{}, fmt.Errorf("%w: %q is too long", ErrUsageWindow, text)
		}
		span = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return UsageWindow{}, fmt.Errorf("%w: %q is not one", ErrUsageWindow, text)
		}
		span = parsed
	}
	if span <= 0 {
		return UsageWindow{}, fmt.Errorf("%w: %q is not positive", ErrUsageWindow, text)
	}
	return UsageWindow{text: text, span: span}, nil
}

// String is the window as it was written, or the default it stood for.
func (w UsageWindow) String() string { return w.text }

// Span is how far back the window reaches.
func (w UsageWindow) Span() time.Duration { return w.span }

// Since is when a window measured back from now began.
func (w UsageWindow) Since(now time.Time) time.Time { return now.Add(-w.span) }
