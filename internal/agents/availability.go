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

// WorkerError is a failed worker protocol request or turn. It has no
// invocation purpose or session parameter, preserving the worker side of the
// role split.
type WorkerError struct {
	Agent   string
	Failure Failure
	Message string
	// Err is the typed cause where the adapter read one, such as
	// ErrCredentialRefused or ErrWorkerEnded, and nil otherwise. It stays
	// matchable with errors.Is.
	Err error
}

// Error describes the failed worker request without inventing an invocation purpose.
func (e *WorkerError) Error() string {
	return "agents: " + e.Agent + " worker failed (" + string(e.Failure) + "): " + strings.ReplaceAll(e.Message, "\n", "; ")
}

// Unwrap returns the typed cause, nil when there is none.
func (e *WorkerError) Unwrap() error { return e.Err }

// CurrentWorkerFailure reads an adapter-owned failure without copying captured
// output. None means no recognized failure, not proof of provider availability.
func CurrentWorkerFailure(w Worker) Failure {
	if reporter, ok := w.(interface{ AvailabilityFailure() Failure }); ok {
		return reporter.AvailabilityFailure()
	}
	return FailureNone
}

// CurrentWorkerError is CurrentWorkerFailure's typed account: the
// *WorkerError behind a failure the adapter recognized, carrying its cause,
// or nil when there is none or the adapter keeps only the category. Its
// caller is internal/session, which internal/service's worker handoff reads
// it through, so the refusal a person reads when the backup fails too names
// a missing credential rather than only an outage.
func CurrentWorkerError(w Worker) error {
	if reporter, ok := w.(interface{ AvailabilityError() error }); ok {
		return reporter.AvailabilityError()
	}
	return nil
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
// for "you've hit your limit". The rows are availabilityMarkers.
func availabilityMessage(message string) (Failure, error) {
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
		for _, row := range availabilityMarkers {
			if line == row.marker || strings.HasPrefix(line, row.marker+" ") || strings.HasPrefix(line, row.marker+".") || strings.HasPrefix(line, row.marker+":") {
				return row.failure, row.cause
			}
		}
	}
	return FailureNone, nil
}

// availabilityMarker is one line availabilityMessage recognizes: the
// lower-case marker, the failure it is, and the typed cause it carries, nil
// where the category is the whole of what is known.
type availabilityMarker struct {
	marker  string
	failure Failure
	cause   error
}

// availabilityMarkers is the one table of failure text this package
// recognizes, in the order a line is tried against it, every quota row
// before every outage row. It is a text match and nothing more: a CLI that
// rewords a line drops out of it into the category the adapter already
// had, which never allows a fallback.
//
// Most rows predate this table and carry no recorded output in this
// repository. The Codex credential row does: "unexpected status 401
// unauthorized" is the line Codex CLI 0.163.0 printed, as the turn.failed
// message of `codex exec --json`, both with no credential at all and with a
// key the provider rejected, recorded in
// testdata/codex-exec-unauthenticated-0.163.0.jsonl and
// testdata/codex-exec-rejected-0.163.0.jsonl. That command prints no
// structured status beside the text, so the text is the signal there, pinned
// to that version. The Codex app-server does carry one, and codexworker.go
// reads it instead of this table.
var availabilityMarkers = []availabilityMarker{
	{"rate_limit_error", FailureQuota, nil},
	{"rate_limit_exceeded", FailureQuota, nil},
	{"429", FailureQuota, nil},
	{"quota exceeded", FailureQuota, nil},
	{"insufficient_quota", FailureQuota, nil},
	{"usage limit reached", FailureQuota, nil},
	{"your usage limit has been reached", FailureQuota, nil},
	{"usage limit has been reached", FailureQuota, nil},
	{"you've hit your usage limit", FailureQuota, nil},
	{"you've hit your monthly spend limit", FailureQuota, nil},
	{"you've hit your limit", FailureQuota, nil},
	{"you have hit your limit", FailureQuota, nil},
	{"you have hit your chatgpt usage limit", FailureQuota, nil},
	{"you have hit your codex usage limit", FailureQuota, nil},
	{"exceeded your current quota", FailureQuota, nil},
	{"credit balance is too low", FailureQuota, nil},
	{"authentication required", FailureUnavailable, ErrCredentialRefused},
	{"not logged in", FailureUnavailable, ErrCredentialRefused},
	{"authentication_error", FailureUnavailable, ErrCredentialRefused},
	{"unexpected status 401 unauthorized", FailureUnavailable, ErrCredentialRefused},
	{"service unavailable", FailureUnavailable, nil},
	{"overloaded_error", FailureUnavailable, nil},
}

// availabilityCategory reclassifies an agent-reported failure or a non-zero
// exit by availabilityMessage, and returns the category with the typed cause
// the matched row carries. Any other category is returned as it is, with no
// cause.
func availabilityCategory(category Failure, message string) (Failure, error) {
	if category != FailureAgent && category != FailureExit {
		return category, nil
	}
	if classified, cause := availabilityMessage(message); classified != FailureNone {
		return classified, cause
	}
	return category, nil
}

// withCause joins a typed availability cause onto the cause a failure
// already had, so both stay matchable with errors.Is.
func withCause(err, cause error) error {
	switch {
	case cause == nil:
		return err
	case err == nil:
		return cause
	}
	return errors.Join(err, cause)
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
			} else if classified, _ := availabilityMessage(e.Result + "\n" + strings.Join(e.Errors, "\n")); classified != FailureNone {
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
	err     *WorkerError
}

func (s *workerAvailability) set(f Failure) { s.mu.Lock(); s.failure, s.err = f, nil; s.mu.Unlock() }
func (s *workerAvailability) get() Failure  { s.mu.Lock(); defer s.mu.Unlock(); return s.failure }

// fail records a recognized failure with its typed account, which
// AvailabilityError hands back until the next set clears both.
func (s *workerAvailability) fail(e *WorkerError) {
	s.mu.Lock()
	s.failure, s.err = e.Failure, e
	s.mu.Unlock()
}

func (s *workerAvailability) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		return nil
	}
	return s.err
}
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
