package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// FailureQuota is a failed invocation whose agent reported exhausted quota.
const FailureQuota Failure = "quota"

// FailureUnavailable is an agent-reported service or authentication outage.
const FailureUnavailable Failure = "unavailable"

// WorkerError is a failed worker protocol request. It has no invocation
// purpose or session parameter, preserving the worker side of the role split.
type WorkerError struct {
	Agent   string
	Failure Failure
	Message string
}

// Error describes the failed worker request without inventing an invocation purpose.
func (e *WorkerError) Error() string {
	return "agents: " + e.Agent + " worker failed (" + string(e.Failure) + "): " + strings.ReplaceAll(e.Message, "\n", "; ")
}

// CurrentWorkerFailure reads an adapter-owned failure without copying captured
// output. None means no recognized failure, not proof of provider availability.
func CurrentWorkerFailure(w Worker) Failure {
	if reporter, ok := w.(interface{ AvailabilityFailure() Failure }); ok {
		return reporter.AvailabilityFailure()
	}
	return FailureNone
}

// FallbackAllowed reports only recognized quota/availability failures. A bad
// report, a cancellation, an unknown adapter, or a missing capability is never
// a reason to try a different agent.
func FallbackAllowed(err error) bool {
	var worker *WorkerError
	if errors.As(err, &worker) {
		return worker.Failure == FailureQuota || worker.Failure == FailureUnavailable
	}
	var failed *InvocationError
	if errors.As(err, &failed) {
		return failed.Failure == FailureQuota || failed.Failure == FailureUnavailable || (failed.Failure == FailureProcess && (errors.Is(failed.Err, exec.ErrNotFound) || errors.Is(failed.Err, os.ErrNotExist)))
	}
	if errors.Is(err, ErrInvalidInvocation) || errors.Is(err, ErrAdapterDeclaration) || errors.Is(err, ErrUndeclaredCapability) {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	var resolution *ResolutionError
	if errors.As(err, &resolution) {
		if len(resolution.Tried) == 0 {
			return false
		}
		for _, t := range resolution.Tried {
			if !FallbackAllowed(t.Err) {
				return false
			}
		}
		return true
	}
	return false
}

// availabilityMessage recognizes the failure strings the supported CLIs
// report. It is called only on an error envelope or a failed process's stderr,
// never on successful output, file contents, or an ordinary transcript.
//
// A line matches a marker when the line is that marker, or starts with it
// followed by a space, a period, or a colon. A shorter marker does not cover
// a sentence that puts other words in front of the rest of it, so Claude's
// monthly spend-limit sentence is its own quota marker rather than a match
// for "you've hit your limit".
func availabilityMessage(message string) Failure {
	s := strings.ToLower(strings.TrimSpace(message))
	for _, prefix := range []string{"error: ", "api error: "} {
		s = strings.TrimPrefix(s, prefix)
	}
	var wire struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(s), &wire) == nil {
		s = wire.Type + " " + wire.Error.Type + " " + wire.Error.Code + " " + wire.Error.Message + " " + wire.Message
		if wire.Error.Type != "" {
			s = wire.Error.Type + " " + wire.Error.Message
		}
		if wire.Error.Code != "" {
			s = wire.Error.Code + " " + wire.Error.Message
		}
		if wire.Error.Type == "" && wire.Error.Code == "" {
			s = strings.TrimSpace(wire.Error.Message + " " + wire.Message)
		}
		s = strings.TrimSpace(s)
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		for _, marker := range []string{"rate_limit_error", "rate_limit_exceeded", "429", "quota exceeded", "insufficient_quota", "usage limit reached", "your usage limit has been reached", "usage limit has been reached", "you've hit your usage limit", "you've hit your monthly spend limit", "you've hit your limit", "you have hit your limit", "you have hit your chatgpt usage limit", "you have hit your codex usage limit", "exceeded your current quota", "credit balance is too low"} {
			if line == marker || strings.HasPrefix(line, marker+" ") || strings.HasPrefix(line, marker+".") || strings.HasPrefix(line, marker+":") {
				return FailureQuota
			}
		}
		for _, marker := range []string{"authentication required", "not logged in", "authentication_error", "service unavailable", "overloaded_error"} {
			if line == marker || strings.HasPrefix(line, marker+" ") || strings.HasPrefix(line, marker+".") || strings.HasPrefix(line, marker+":") {
				return FailureUnavailable
			}
		}
	}
	return FailureNone
}

func availabilityCategory(category Failure, message string) Failure {
	if category != FailureAgent && category != FailureExit {
		return category
	}
	if classified := availabilityMessage(message); classified != FailureNone {
		return classified
	}
	return category
}

// WorkerAvailability reads only explicit error results and rejected rate-limit
// events from a stream capture. Unknown, partial and successful output does
// not establish exhaustion. A text terminal is deliberately not guessed at.
func WorkerAvailability(c Capture) Failure {
	if c.Format != OutputStreamJSON {
		return FailureNone
	}
	var failure Failure
	lines := strings.Split(c.Text, "\n")
	for i, line := range lines {
		if (i == 0 && c.Dropped > 0) || (i == len(lines)-1 && !strings.HasSuffix(c.Text, "\n")) {
			continue
		}
		var e struct {
			Type    string   `json:"type"`
			IsError bool     `json:"is_error"`
			Result  string   `json:"result"`
			Errors  []string `json:"errors"`
			Rate    struct {
				Status string `json:"status"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Type == "result" {
			if !e.IsError {
				failure = FailureNone
			} else if classified := availabilityMessage(e.Result + "\n" + strings.Join(e.Errors, "\n")); classified != FailureNone {
				failure = classified
			}
		}
		if e.Type == "rate_limit_event" && e.Rate.Status == "rejected" {
			failure = FailureQuota
		}
	}
	return failure
}

// workerAvailability owns a live worker's last protocol failure. Captured
// output is evidence, never the source of this state. A queued message and a
// successful response clear it even when old error output is still retained.
type workerAvailability struct {
	mu      sync.Mutex
	failure Failure
}

func (s *workerAvailability) set(f Failure) { s.mu.Lock(); s.failure = f; s.mu.Unlock() }
func (s *workerAvailability) get() Failure  { s.mu.Lock(); defer s.mu.Unlock(); return s.failure }
func (s *workerAvailability) observe(line []byte) {
	var header struct {
		Type    string `json:"type"`
		IsError bool   `json:"is_error"`
		Rate    struct {
			Status string `json:"status"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(line, &header) != nil {
		return
	}
	if header.Type == "result" && !header.IsError {
		s.set(FailureNone)
		return
	}
	if header.Type == "rate_limit_event" && (header.Rate.Status == "allowed" || header.Rate.Status == "allowed_warning") {
		s.set(FailureNone)
		return
	}
	if f := WorkerAvailability(Capture{Text: string(line) + "\n", Format: OutputStreamJSON}); f != FailureNone {
		s.set(f)
	}
}

type availabilityStream struct {
	mu      sync.Mutex
	out     *tailWriter
	state   *workerAvailability
	pending []byte
	limit   int64
	discard bool
}

// Write retains bytes while updating live availability from complete bounded frames.
func (w *availabilityStream) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.out.Write(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		}
		if !w.discard && int64(len(w.pending)+end) <= w.limit {
			w.pending = append(w.pending, p[:end]...)
		} else {
			w.pending = nil
			w.discard = true
		}
		if end == len(p) {
			break
		}
		if !w.discard {
			w.state.observe(w.pending)
		}
		w.pending = nil
		w.discard = false
		p = p[end+1:]
	}
	return n, err
}
