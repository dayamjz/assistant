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
// at the adapter; a row must keep it or the history fabricates zeros.
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
}

// AppendAgentInvocation appends one invocation to the history and returns the
// row with the sequence it was assigned. It refuses a row naming no purpose
// or no agent, because a cost nothing is attributed to answers no question
// the history exists for; every other field is stored as handed over.
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
	var seq int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO agent_invocation (
				purpose, agent, model, session_use, started, duration_ns, failure,
				input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, turns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			inv.Purpose, inv.Agent, inv.Model, inv.SessionUse,
			encodeTime(inv.Started), int64(inv.Duration), inv.Failure,
			inv.InputTokens, inv.OutputTokens, inv.CacheReadTokens,
			inv.CacheCreationTokens, inv.Turns)
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
	rows, err := s.read.QueryContext(ctx, `
		SELECT seq, purpose, agent, model, session_use, started, duration_ns, failure,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, turns
		FROM agent_invocation ORDER BY seq`)
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
			&inv.CacheCreationTokens, &inv.Turns); err != nil {
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
