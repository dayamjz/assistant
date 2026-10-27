package standin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestTheModelledUsageKeysAreTheRecordedOnes holds each one-shot wire this
// package writes to what the real CLI printed, recorded in
// internal/agents/testdata: the numeric keys of the modelled usage object are
// exactly the recording's, less the ones a recording carries that the
// invocation record keeps no count for, which are named here. A key the model
// misspells or drops fails here, rather than every test that runs against
// the model agreeing with an adapter that reads a key no CLI writes, which is
// how #510's Cursor counts were lost.
func TestTheModelledUsageKeysAreTheRecordedOnes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		recording string
		wire      func(Envelope, Call) ([]byte, error)
		// unkept is what the recording reports that the invocation record
		// has no count for.
		unkept []string
	}{
		{"claude", "claude-print-2.1.306.json", Envelope.wire, nil},
		{"cursor", "cursor-print-2026.10.19.json", Envelope.cursorWire, nil},
		{"codex", "codex-exec-0.163.0.jsonl", Envelope.codexWire, []string{"reasoning_output_tokens"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			recorded, err := os.ReadFile(filepath.Join("..", "testdata", c.recording))
			if err != nil {
				t.Fatalf("reading the recording: %v", err)
			}
			want := numericUsageKeys(t, recorded)
			for _, key := range c.unkept {
				if !slices.Contains(want, key) {
					t.Fatalf("%s is named as unkept and the recording does not carry it", key)
				}
				want = slices.DeleteFunc(want, func(k string) bool { return k == key })
			}
			if len(want) != 4 {
				t.Fatalf("the recording carries usage keys %v, want the four token counts", want)
			}
			modelled, err := c.wire(Envelope{Result: "ok", Usage: DefaultUsage()}, Call{})
			if err != nil {
				t.Fatalf("encoding the modelled envelope: %v", err)
			}
			if got := numericUsageKeys(t, modelled); !slices.Equal(got, want) {
				t.Errorf("the model writes usage keys %v, and the recording carries %v", got, want)
			}
		})
	}
}

// numericUsageKeys is the sorted keys of the usage object in raw whose
// values are numbers: the result object's own, or, for JSONL, the
// turn.completed event's.
func numericUsageKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var line struct {
			Type  string                     `json:"type"`
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Usage == nil {
			continue
		}
		if line.Type != "result" && line.Type != "turn.completed" {
			continue
		}
		var keys []string
		for _, key := range slices.Sorted(maps.Keys(line.Usage)) {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(line.Usage[key]))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				continue
			}
			if _, number := value.(json.Number); number {
				keys = append(keys, key)
			}
		}
		return keys
	}
	t.Fatalf("no result or turn.completed line with a usage object in:\n%s", raw)
	return nil
}
