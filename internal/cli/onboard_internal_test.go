package cli

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// Every inspection refusal is a value a caller branches on, and failureExit
// is that branch: an origin with two addresses is incorrect usage because
// --upstream settles it, and a missing git or a disagreeing configuration
// file is an operational failure. The row a person reads does not spell the
// schema name the value carries for the caller.
func TestOnboardRefusalIsAValueTheCallerHandles(t *testing.T) {
	exits := map[machine.OnboardRefusal]machine.Code{
		machine.OnboardGitMissing:        machine.ExitFailure,
		machine.OnboardIdentityAmbiguous: machine.ExitUsage,
		machine.OnboardConfigConflict:    machine.ExitFailure,
	}
	if len(exits) != len(machine.OnboardRefusals()) {
		t.Fatalf("the exit table names %d refusals and the set has %d", len(exits), len(machine.OnboardRefusals()))
	}
	for _, reason := range machine.OnboardRefusals() {
		want, ok := exits[reason]
		if !ok {
			t.Errorf("no exit code is decided for %s", reason)
			continue
		}
		err := onboardRefused{reason: reason, keys: []string{string(configKeyForTest)}}
		var got onboardRefused
		if !errors.As(err, &got) {
			t.Fatalf("%s does not unwrap as onboardRefused", reason)
		}
		if len(got.keys) != 1 || got.keys[0] != string(configKeyForTest) {
			t.Errorf("%s dropped the keys a caller reads: %v", reason, got.keys)
		}
		if failureExit(err) != want {
			t.Errorf("%s exits %s, want %s", reason, failureExit(err), want)
		}
		if strings.Contains(err.Error(), string(configKeyForTest)) {
			t.Errorf("%s's row spells %s, which is for the caller and not the row:\n%s", reason, configKeyForTest, err.Error())
		}
	}
	missing := onboardRefused{reason: machine.OnboardGitMissing, cause: exec.ErrNotFound}
	if !errors.Is(missing, exec.ErrNotFound) {
		t.Fatal("a missing git does not match exec.ErrNotFound, so a caller cannot tell it from another refusal")
	}
}

// configKeyForTest is a schema name the row must not spell. It is one of the
// keys the conflict refusal records, and the stage word inside it is why it
// stays off the row.
const configKeyForTest = "commands.test"
