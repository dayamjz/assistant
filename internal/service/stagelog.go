package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
)

// PRD section 8's per-stage log, logs/<run>/<stage>.log, which home.StageLog
// names and this file is the one writer and the one reader of.
//
// What is written, and where from:
//
//   - observe.go's wrappers write when each stage body and each fix round
//     begins and ends, with the report a body returned or the summary a round
//     returned, and the error when either failed. A fix round is written to
//     the log of the stage whose findings it fixes, which is the stage
//     agents.Served names for it too.
//   - contain.go's wrappers write every agent invocation a body or a round
//     makes through the resolved agent: its account, which is the
//     agents.Record the invocation record keeps, and its full reply, which
//     that record by design never holds. A failed invocation writes its
//     error instead, which carries what the agent wrote about the failure.
//
// Every write passes through internal/redact first, which is PRD section 8's
// rule for anything persisted, so the file holds the reply with credentials
// in the shapes that package recognizes removed rather than the bytes the
// agent printed; each section's header says so. A read passes the tail
// through it again, which costs nothing on text this file wrote and covers a
// file it did not.
//
// The log is evidence and never state (P8). It is appended and never
// revised, nothing in this service reads it to decide anything, and a run's
// answer is built from its record and its checkpoint without it. The one
// reader is runLog, which hands a bounded tail to a person or an agent to
// read.
//
// The residual gaps:
//
//   - Writing is best-effort. A log that cannot be opened or written is
//     reported in the service log and the stage goes on, because a gate that
//     stopped validating when a disk filled would be worse than a log with a
//     hole in it. A lost write leaves no mark in the file and nothing on the
//     run's answer says one was lost, so the file is PRD section 8's full
//     output only as far as its writes landed, and a reader cannot tell a
//     hole from a stage that wrote nothing there.
//   - What reaches the log is what passes through these two wrappers. A body
//     that resolved an adapter of its own stands outside contain.go's wrapper
//     on the terms that file states, so its calls are not here; and the test,
//     lint and document commands' own output is their evidence file under
//     home.Evidence, which the stage's report names and this log does not
//     copy.
//   - A file grows for as long as its run does. Nothing here rotates or
//     bounds it: PRD section 8 makes it the full output, and the bound is on
//     what a read returns.

// stageLogEntry appends one section to a run's stage log. run and stage come
// from this service's own marks on the context or from the run's state, never
// from a caller, and an entry naming neither is dropped, since there is no
// file it could belong to.
func (s *Service) stageLogEntry(run, stage, text string) {
	if s.home == nil || run == "" || stage == "" {
		return
	}
	path := s.home.StageLog(run, stage)
	s.stageLogMu.Lock()
	defer s.stageLogMu.Unlock()
	if err := appendStageLog(path, redact.New().Redact(text)); err != nil && s.log != nil {
		s.log.Printf("dropping part of run %s's %s log: %v", run, stage, err)
	}
}

// appendStageLog appends text to the file at path, creating it and the run's
// directory when they are not there.
func appendStageLog(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, text)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// stamp is the time a section header carries.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// recordedNote is the line every section header carries, because what the
// file holds is not byte-identical to what was printed.
const recordedNote = "=== record:  redacted, not verbatim: credentials internal/redact recognizes are removed\n"

// logBodyBegan writes that a stage's body began.
func (s *Service) logBodyBegan(run string, stage pipeline.Stage, at time.Time) {
	s.stageLogEntry(run, stage.String(), fmt.Sprintf("\n=== %s %s: the stage's body began\n%s", stamp(at), stage, recordedNote))
}

// logBodyEnded writes how a stage's body ended: the report it returned, or
// the error it failed with.
func (s *Service) logBodyEnded(run string, stage pipeline.Stage, started, ended time.Time, report findings.Report, failed error) {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s %s: the stage's body ended after %s\n", stamp(ended), stage, ended.Sub(started).Round(time.Millisecond))
	if failed != nil {
		fmt.Fprintf(&b, "=== failed: %v\n", failed)
	} else {
		fmt.Fprintf(&b, "=== report (%d finding(s)):\n%s\n", len(report.Findings), encodeForLog(report))
	}
	s.stageLogEntry(run, stage.String(), b.String())
}

// logRoundBegan writes that a fix round began for a stage, and how many
// findings it was given and whether a person asked for it.
func (s *Service) logRoundBegan(run string, in pipeline.FixInput, at time.Time) {
	kind := "an automatic fix round"
	if in.Requested != nil {
		kind = "a fix round a person asked for"
	}
	s.stageLogEntry(run, in.Stage.String(), fmt.Sprintf("\n=== %s %s: %s began over %d finding(s)\n%s",
		stamp(at), in.Stage, kind, len(in.Findings), recordedNote))
}

// logRoundEnded writes how a fix round ended: the summary it reported and the
// head it wrote, or the error it failed with.
func (s *Service) logRoundEnded(run string, stage pipeline.Stage, started, ended time.Time, out pipeline.FixOutput, failed error) {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s %s: the fix round ended after %s\n", stamp(ended), stage, ended.Sub(started).Round(time.Millisecond))
	switch {
	case failed != nil:
		fmt.Fprintf(&b, "=== failed: %v\n", failed)
	default:
		if head, _ := out.Writes[pipeline.KeyHead].Text(); head != "" {
			fmt.Fprintf(&b, "=== committed: %s\n", head)
		} else {
			b.WriteString("=== committed: nothing\n")
		}
		fmt.Fprintf(&b, "=== summary:\n%s\n", out.Summary)
		for _, hold := range out.Holds {
			fmt.Fprintf(&b, "=== held: %s\n", hold.Description)
		}
	}
	s.stageLogEntry(run, stage.String(), b.String())
}

// logInvocation writes one agent invocation's account and its full reply,
// or the error it ended with. served is what contain.go stamped on it, which
// names the log it belongs to.
func (s *Service) logInvocation(served agents.Served, purpose agents.Purpose, result agents.Result, failed error) {
	if served.Run == "" || served.Stage == "" {
		return
	}
	var b strings.Builder
	if failed != nil {
		fmt.Fprintf(&b, "--- agent invocation for %s failed: %v\n", purpose, failed)
		s.stageLogEntry(served.Run, served.Stage, b.String())
		return
	}
	r := result.Record
	fmt.Fprintf(&b, "--- agent invocation: purpose %s, agent %s, model %s, session %s\n",
		r.Purpose, r.Agent, orUnnamed(r.Model), r.Session)
	failure := string(r.Failure)
	if r.Failure == agents.FailureNone {
		failure = "none"
	}
	fmt.Fprintf(&b, "--- started %s, took %s, failure %s\n", stamp(r.Started), r.Duration.Round(time.Millisecond), failure)
	fmt.Fprintf(&b, "--- tokens: input %s, output %s, cache read %s, cache written %s, turns %s\n",
		r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheCreationTokens, r.Usage.Turns)
	fmt.Fprintf(&b, "--- reply:\n%s\n--- end of reply\n", result.Text)
	s.stageLogEntry(served.Run, served.Stage, b.String())
}

// orUnnamed is a model name for the log, saying so when none was named.
func orUnnamed(model string) string {
	if model == "" {
		return "(none named)"
	}
	return model
}

// encodeForLog writes a value as indented JSON for a person reading the log,
// or says why it could not.
func encodeForLog(v any) string {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("(could not be written: %v)", err)
	}
	return string(body)
}

// runLog answers run.log: the end of one stage's log in one run, at most
// machine.StageLogTailBytes of it, starting at a line boundary, with how
// much it left out and where the whole of it is.
//
// The run is read from the record first, so a log is only ever read for a
// run this home holds and its path is composed from an identifier the store
// assigned rather than one a caller wrote; a stage that is not one of the
// nine is an invalid request. A log that does not exist is an answer, not an
// error: a stage that never ran in a run has none. What this reads decides
// nothing about the run, on the terms this file's header states.
func (s *Service) runLog(ctx context.Context, req machine.StageLogRequest) (machine.StageLog, error) {
	stage, ok := pipeline.ParseStage(req.Stage)
	if !ok {
		return machine.StageLog{}, fmt.Errorf("%w: %q is not one of the stages: %s",
			ipc.ErrInvalidRequest, req.Stage, strings.Join(stageNames(), ", "))
	}
	record, err := s.store.Run(ctx, req.Run)
	if err != nil {
		return machine.StageLog{}, err
	}
	answer := machine.StageLog{Run: record.ID, Stage: stage.String(), Path: s.home.StageLog(record.ID, stage.String())}
	s.stageLogMu.Lock()
	tail, size, omitted, err := readTail(answer.Path, machine.StageLogTailBytes)
	s.stageLogMu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return answer, nil
	}
	if err != nil {
		return machine.StageLog{}, fmt.Errorf("service: reading run %s's %s log: %w", record.ID, stage, err)
	}
	answer.Written, answer.Size, answer.Omitted = true, size, omitted
	answer.Text = redact.New().Redact(tail)
	return answer, nil
}

// readTail reads at most limit bytes from the end of the file at path. When
// that leaves something out, the tail starts after its first newline, so it
// never opens on half a line, and omitted counts the half line too.
func readTail(path string, limit int64) (tail string, size, omitted int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	size = info.Size()
	start := size - limit
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", 0, 0, err
	}
	if start > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
			start += int64(i + 1)
		}
	}
	return string(buf), size, start, nil
}
