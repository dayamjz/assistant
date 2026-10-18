package agents

import (
	"encoding/json"
	"testing"
)

func TestCursorWorkerPermissionPolicyFailsClosed(t *testing.T) {
	w := acpWorker{route: "/opt/assistant --home /tmp/home worker"}
	for _, test := range []struct {
		kind, command string
		allowed       bool
	}{
		{"read", "", true}, {"edit", "", true}, {"search", "", true},
		{"execute", "git status", true},
		{"execute", "/opt/assistant --home /tmp/home worker event working", true},
		{"execute", "git status; rm -rf /tmp/subject", false},
		{"execute", "git status $(touch /tmp/unexpected)", false},
		{"execute", "echo git status", false},
		{"execute", "gitfoo status", false},
		{"execute", "/opt/assistant --home /tmp/home worker-unrelated", false},
		{"other", "", false},
	} {
		raw, _ := json.Marshal(map[string]any{"toolCall": map[string]any{"kind": test.kind, "rawInput": map[string]string{"command": test.command}}, "options": []any{map[string]string{"kind": "allow_once", "optionId": "yes"}}})
		if got := w.permission(raw); (got == "yes") != test.allowed {
			t.Errorf("%s %q: %q", test.kind, test.command, got)
		}
	}
	if got := w.permission(json.RawMessage(`{"toolCall":{"kind":"edit"},"options":[{"kind":"allow_always","optionId":"yes"}]}`)); got != "" {
		t.Fatal("permission was widened beyond once")
	}
}
