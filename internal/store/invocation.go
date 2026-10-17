package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AgentInvocation is one row of the agent invocation history: what one agent
// call cost, as PRD section 8 lists the record. It is history, not an
// authoritative record: nothing reads it back to decide what a run may do,
// and nothing here revises a row once it is written.
//
// The vocabulary fields are text whose meaning belongs to internal/agents,
// the way run.resolved_agent holds a name the agent catalog owns: this
// package stores what it was handed and does not decode Purpose, SessionUse,
// or Failure into anything a caller could branch on here. Failure is empty
// for an invocation that produced a result, which is the recording package's
// own spelling of that fact.
//
// Every count is an Optional because an agent that did not report one and an
// agent that reported zero are different facts, and the distinction is born
// at the adapter; a row must keep it or the history fabricates zeros. Run and
// Stage are Optionals for the reason a later column always is: a row written
// before they existed reads back unknown for both, and so does a call that
// served no run or no stage, so unknown is the one spelling of "names none".
type AgentInvocation struct {
	// Seq is the row's position in the history, assigned by this package on
	// append and counted from one. A caller never supplies it.
	Seq int64
	// Purpose is the role the invocation played.
	Purpose string
	// Agent is the name of the agent that ran.
	Agent string
	// Model is the model the invocation asked for or the agent reported,
	// empty when neither named one.
	Model string
	// Role is the role the launch ran under. Unknown means the row predates
	// the column. A known empty value means the launch named no role.
	Role Optional[string]
	// Provider, Type, and Version are the model triple the launch was asked
	// for. Each is unknown when the row predates the column, and known empty
	// when the launch did not name that one.
	Provider Optional[string]
	Type     Optional[string]
	Version  Optional[string]
	// Effort is the effort the launch was asked for, on the same terms as
	// Provider.
	Effort Optional[string]
	// SessionUse is what the invocation did with the run's durable session.
	SessionUse string
	// Started is when the agent process was started.
	Started time.Time
	// Duration is how long the invocation took to reach a result or a
	// failure.
	Duration time.Duration
	// Failure is the failure category, empty for an invocation that produced
	// a result.
	Failure string
	// InputTokens is the prompt tokens the agent reported.
	InputTokens Optional[int64]
	// OutputTokens is the tokens the agent generated.
	OutputTokens Optional[int64]
	// CacheReadTokens is prompt tokens served from a cache.
	CacheReadTokens Optional[int64]
	// CacheCreationTokens is prompt tokens written to a cache.
	CacheCreationTokens Optional[int64]
	// Turns is how many turns the agent took.
	Turns Optional[int64]
	// Run is the identifier of the run the invocation served, unknown when
	// it served none or the row predates the column.
	Run Optional[string]
	// Stage is the name of the pipeline stage the invocation served, unknown
	// when it served none or the row predates the column.
	Stage Optional[string]
}

// AppendAgentInvocation appends one invocation to the history and returns the
// row with the sequence it was assigned. It refuses a row naming no purpose
// or no agent, because a cost nothing is attributed to answers no question
// the history exists for, and a Run or Stage that is known but blank,
// because unknown is how a row says it names none and a blank known value
// would be a second spelling of that read back as a name. Every other field
// is stored as handed over.
//
// Nothing revises or deletes a row: there is no accessor that does, so the
// history holds what happened in the order it was accepted.
func (s *Store) AppendAgentInvocation(ctx context.Context, inv AgentInvocation) (AgentInvocation, error) {
	if strings.TrimSpace(inv.Purpose) == "" {
		return AgentInvocation{}, fmt.Errorf("store: agent invocation has no purpose")
	}
	if strings.TrimSpace(inv.Agent) == "" {
		return AgentInvocation{}, fmt.Errorf("store: agent invocation has no agent")
	}
	for _, named := range []struct {
		column string
		value  Optional[string]
	}{{"run", inv.Run}, {"stage", inv.Stage}} {
		if v, known := named.value.Get(); known && strings.TrimSpace(v) == "" {
			return AgentInvocation{}, fmt.Errorf("store: agent invocation names a blank %s; a call that served none leaves it unknown", named.column)
		}
	}
	var seq int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO agent_invocation (
				purpose, agent, model, session_use, started, duration_ns, failure,
				input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, turns,
				run, stage, role, provider, model_type, version, effort)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			inv.Purpose, inv.Agent, inv.Model, inv.SessionUse,
			encodeTime(inv.Started), int64(inv.Duration), inv.Failure,
			inv.InputTokens, inv.OutputTokens, inv.CacheReadTokens,
			inv.CacheCreationTokens, inv.Turns, inv.Run, inv.Stage,
			inv.Role, inv.Provider, inv.Type, inv.Version, inv.Effort)
		if err != nil {
			return err
		}
		seq, err = result.LastInsertId()
		return err
	})
	if err != nil {
		return AgentInvocation{}, fmt.Errorf("store: appending agent invocation: %w", err)
	}
	inv.Seq = seq
	return inv, nil
}

// AgentInvocations returns the whole history in the order it was appended.
// It is a history read, so a caller summarizing cost reads every row rather
// than trusting a summary kept beside them; none is kept.
func (s *Store) AgentInvocations(ctx context.Context) ([]AgentInvocation, error) {
	return s.agentInvocations(ctx, "", nil)
}

// AgentInvocationsSince returns every row of the history whose call started
// at or after since, in the order it was appended. It is AgentInvocations
// bounded by a window, for a caller totalling what calls in that window cost:
// the window is over when a call started, so a call that started before since
// and ran into the window is not in it.
func (s *Store) AgentInvocationsSince(ctx context.Context, since time.Time) ([]AgentInvocation, error) {
	return s.agentInvocations(ctx, "WHERE started >= ?", []any{encodeTime(since)})
}

// agentInvocations is the one read both accessors make, with where the
// filter its caller adds. Stored instants are fixed-width UTC text, which
// sorts as the instants do, so the comparison is over the column as stored.
func (s *Store) agentInvocations(ctx context.Context, where string, args []any) ([]AgentInvocation, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT seq, purpose, agent, model, session_use, started, duration_ns, failure,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, turns,
			run, stage, role, provider, model_type, version, effort
		FROM agent_invocation `+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading agent invocations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AgentInvocation
	for rows.Next() {
		var inv AgentInvocation
		var started string
		var durationNS int64
		if err := rows.Scan(&inv.Seq, &inv.Purpose, &inv.Agent, &inv.Model,
			&inv.SessionUse, &started, &durationNS, &inv.Failure,
			&inv.InputTokens, &inv.OutputTokens, &inv.CacheReadTokens,
			&inv.CacheCreationTokens, &inv.Turns, &inv.Run, &inv.Stage,
			&inv.Role, &inv.Provider, &inv.Type, &inv.Version, &inv.Effort); err != nil {
			return nil, fmt.Errorf("store: reading agent invocations: %w", err)
		}
		if inv.Started, err = decodeTime(started); err != nil {
			return nil, fmt.Errorf("store: agent invocation %d: %w", inv.Seq, err)
		}
		inv.Duration = time.Duration(durationNS)
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading agent invocations: %w", err)
	}
	return out, nil
}
