// Package judgetest is a stand-in decision-model provider that answers over
// the wire. A test points a judge.Client at it with judge.WithEndpoint, and
// the real client reads what it writes, so a shape the client could not read
// from the real provider is a shape a test cannot hand it here either.
//
// Its replies are built from the provider's published schema rather than
// stated as typed values: a success is the SystemOneResponse body, with
// model, answers keyed by the question's own name, and usage; a refusal over
// the token limit is the 400 body with detail.error_type
// "max_tokens_exceeded"; and a request missing what a noul question needs is
// answered 422 with the HTTPValidationError body, as the provider would. Raw
// is the one reply that writes arbitrary bytes, and it exists for the
// unreadable answers a test needs the client to refuse.
//
// Its consumers are internal/judge's own tests and the review stage's in
// internal/stages, which both need the provider on the wire; one copy is why
// it is a package.
package judgetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/judge"
)

// Request is one request the provider received, decoded.
type Request struct {
	// Question is the name the request gave its one question.
	Question string
	// Model is the model the request named.
	Model string
	// Path, Reason, and Diff are the request's state.
	Path, Reason, Diff string
	// Authorization is the request's Authorization header as sent.
	Authorization string
}

// Reply is how the provider answers one request. Build one with the
// constructors below.
type Reply struct {
	status int
	body   any
	raw    string
	model  string
	noul   float64
	hang   bool
	answer bool
}

// Answer is a success whose one answer is the probability p, from
// judge.ModelVersion.
func Answer(p float64) Reply { return Reply{status: http.StatusOK, noul: p, answer: true} }

// AnswerFrom is Answer from another model, which a client pinned to
// judge.ModelVersion must refuse.
func AnswerFrom(model string, p float64) Reply {
	return Reply{status: http.StatusOK, noul: p, answer: true, model: model}
}

// TooLarge is the provider's refusal of content over its token limit.
func TooLarge() Reply {
	return Reply{status: http.StatusBadRequest, body: map[string]any{
		"detail": map[string]any{"error_type": "max_tokens_exceeded"}}}
}

// Status is a refusal with this status and a body naming nothing the client
// acts on.
func Status(code int) Reply {
	return Reply{status: code, body: map[string]any{"detail": "unavailable"}}
}

// Raw is a reply of exactly this status and body.
func Raw(code int, body string) Reply { return Reply{status: code, raw: body} }

// Hang is a reply that never comes: the provider holds the request until the
// client gives up on it.
func Hang() Reply { return Reply{hang: true} }

// Provider is the stand-in. It is safe for concurrent requests.
type Provider struct {
	server *httptest.Server
	answer func(Request) Reply

	mu       sync.Mutex
	requests []Request
}

// New starts a provider that answers each request with answer's reply and
// stops it when the test ends.
func New(t testing.TB, answer func(Request) Reply) *Provider {
	t.Helper()
	p := &Provider{answer: answer}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

// Always returns an answer function giving every request the same reply.
func Always(r Reply) func(Request) Reply { return func(Request) Reply { return r } }

// URL is the endpoint to hand judge.WithEndpoint.
func (p *Provider) URL() string { return p.server.URL + "/v1/systemone" }

// Requests returns every request received so far, in arrival order.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

// Count is how many requests have arrived.
func (p *Provider) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// wireRequest is the request body as the provider's schema has it.
type wireRequest struct {
	Model     string            `json:"model"`
	State     map[string]string `json:"state"`
	Questions map[string]struct {
		Type         string `json:"type"`
		Instructions string `json:"instructions"`
		Criteria     *struct {
			True  string `json:"true"`
			False string `json:"false"`
		} `json:"criteria"`
	} `json:"questions"`
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	var body wireRequest
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil ||
		body.Model == "" || body.State == nil || len(body.Questions) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, validationError("body"))
		return
	}
	got := Request{
		Model:         body.Model,
		Path:          body.State["path"],
		Reason:        body.State["reason"],
		Diff:          body.State["diff"],
		Authorization: r.Header.Get("Authorization"),
	}
	for name, q := range body.Questions {
		got.Question = name
		if q.Type != "noul" || (q.Instructions == "" && q.Criteria == nil) {
			writeJSON(w, http.StatusUnprocessableEntity, validationError("questions"))
			return
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, got)
	p.mu.Unlock()

	reply := p.answer(got)
	switch {
	case reply.hang:
		select {
		case <-r.Context().Done():
		case <-time.After(time.Minute):
		}
		return
	case reply.raw != "" || (reply.body == nil && !reply.answer):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.raw))
	case reply.answer:
		model := reply.model
		if model == "" {
			model = judge.ModelVersion
		}
		writeJSON(w, reply.status, map[string]any{
			"model":   model,
			"answers": map[string]any{got.Question: map[string]any{"type": "noul", "noul": reply.noul}},
			"usage":   map[string]any{"input_tokens": len(got.Diff)/4 + 1, "output_tokens": 1},
		})
	default:
		writeJSON(w, reply.status, reply.body)
	}
}

// validationError is the provider's 422 body for a request missing a field.
func validationError(field string) map[string]any {
	return map[string]any{"detail": []map[string]any{{
		"loc": []string{"body", field}, "msg": "Field required", "type": "missing"}}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
