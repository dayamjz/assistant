package tasks

import (
	"strings"
	"testing"
)

// ErrPostureUnreadable's sentinel names establishment from the trusted
// configuration, not a read failure alone, so a loosening trusted copy and
// a copy that could not be fetched share one class a coordinator can match on.
func TestErrPostureUnreadableSentinelNamesEstablishment(t *testing.T) {
	msg := ErrPostureUnreadable.Error()
	for _, want := range []string{"established", "trusted configuration", "loosened"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("ErrPostureUnreadable = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "could not be read from its trusted") {
		t.Fatalf("ErrPostureUnreadable still reads as a fetch failure alone: %q", msg)
	}
}
