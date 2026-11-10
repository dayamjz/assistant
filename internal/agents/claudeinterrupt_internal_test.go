package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The interrupt fixture is the stream-json control_request, control_response,
// and interrupted result this package claims for a pipe cancel. Playing the
// result through observe settles an armed turn, so the fixture cannot drop
// those shapes with this test still green. The stand-in's interrupt frames
// are a separate model in standin/stream.go; this test does not hold them.
func TestSchemaClaudeStreamInterruptFramesSettleTheWorker(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-stream-interrupt-control-request.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := &claudePipeWorker{}
	w.armTurn()
	settled := w.settled
	close(w.begun) // interrupt waits on init; fixture starts after it
	var sawRequest, sawResponse, sawResult bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
			} `json:"response"`
			Subtype string `json:"subtype"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			t.Fatalf("fixture line does not decode: %s", line)
		}
		switch msg.Type {
		case "control_request":
			if msg.Request.Subtype != "interrupt" || msg.RequestID == "" {
				t.Fatalf("control_request is %s, want subtype interrupt with request_id", line)
			}
			sawRequest = true
		case "control_response":
			if msg.Response.Subtype != "success" || msg.Response.RequestID == "" {
				t.Fatalf("control_response is %s, want subtype success with request_id", line)
			}
			sawResponse = true
		case "result":
			if msg.Subtype == "" || !msg.IsError || msg.Result == "" {
				t.Fatalf("result is %s, want interrupt subtype, is_error true, and result text", line)
			}
			sawResult = true
			w.observe([]byte(line))
		}
	}
	if !sawRequest || !sawResponse || !sawResult {
		t.Fatalf("fixture missing control_request (%t), control_response (%t), or interrupted result (%t)", sawRequest, sawResponse, sawResult)
	}
	select {
	case <-settled:
	default:
		t.Fatal("interrupted result did not finish the turn")
	}
}

// Interrupt returns only after settled closes. Answering control_request
// first and holding the result until the test releases it would leave a
// mutation that returns true right after write green against every other
// interrupt test, because those settle in the same receive burst as the
// cancel. Not parallel: it swaps waitAfterClaudeInterruptCancel.
func TestClaudeInterruptWaitsForSettledAfterCancelReturns(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	settled := make(chan struct{})
	begun := make(chan struct{})
	close(begun)
	w := &claudePipeWorker{
		procWorker: &procWorker{
			stdin:        stdinW,
			out:          &tailWriter{limit: 1 << 20},
			availability: &workerAvailability{},
			done:         make(chan struct{}),
			slot:         make(chan struct{}, 1),
			frame:        plainLine,
			format:       OutputStreamJSON,
			stdinOpen:    true,
		},
		settled: settled,
		begun:   begun,
	}
	releaseSettle := make(chan struct{})
	reachedWait := make(chan struct{})
	oldWait := waitAfterClaudeInterruptCancel
	t.Cleanup(func() { waitAfterClaudeInterruptCancel = oldWait })
	waitAfterClaudeInterruptCancel = func(ctx context.Context, settled <-chan struct{}, ended <-chan struct{}) error {
		close(reachedWait)
		select {
		case <-settled:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ended:
			return ErrWorkerEnded
		}
	}
	go func() {
		defer stdinR.Close()
		sc := bufio.NewScanner(stdinR)
		for sc.Scan() {
			var req struct {
				Type    string `json:"type"`
				Request struct {
					Subtype string `json:"subtype"`
				} `json:"request"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || req.Type != "control_request" || req.Request.Subtype != "interrupt" {
				continue
			}
			<-releaseSettle
			close(settled)
			return
		}
	}()

	done := make(chan error, 1)
	go func() {
		got, err := w.Interrupt(t.Context())
		if err != nil || !got {
			done <- fmt.Errorf("Interrupt: %t, %w", got, err)
			return
		}
		done <- nil
	}()

	select {
	case <-reachedWait:
	case err := <-done:
		if err == nil {
			err = errors.New("Interrupt returned on control_request alone")
		}
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("Interrupt never reached the post-cancel settle wait")
	}
	select {
	case err := <-done:
		if err == nil {
			err = errors.New("Interrupt returned before settle after reaching the wait")
		}
		t.Fatal(err)
	default:
	}
	close(releaseSettle)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

