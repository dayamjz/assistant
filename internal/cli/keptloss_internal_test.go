package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// A discard shown reads out its row and then exactly what would be lost, in
// keptLossLines' order with the digest last; a loss naming nothing reads
// out the row saying so before the digest, so the row's "listed below" is
// never followed by nothing; a file of a kind no label names is still
// printed, under its kind; and every other answer prints its row alone.
func TestADiscardShownReadsOutExactlyWhatWouldBeLost(t *testing.T) {
	t.Parallel()
	shown := machine.KeptWorkAnswered{Effect: machine.KeptWorkDiscardShown, Loss: &machine.KeptWorkLoss{
		Lacks:   []string{"c1", "c2"},
		Objects: []machine.KeptWorkLostObject{{Object: "o1", Type: "tree", Names: []string{"refs/x", "HEAD@{1}"}}, {Object: "o2", Type: "blob"}},
		Files:   []machine.KeptWorkLostPath{{Kind: "changed", Path: "a.go"}, {Kind: "deleted", Path: "gone/"}, {Kind: "novel", Path: "b\x1bc"}},
		Digest:  "0a1b2c3d4e5f",
	}}
	row := machine.KeptWorkAnsweredPhrase(shown).String()
	for _, tc := range []struct {
		name   string
		answer machine.KeptWorkAnswered
		want   []string
	}{
		{"a loss listed", shown, []string{row,
			"Commit: c1", "Commit: c2", "Other object: o1 (tree) refs/x, HEAD@{1}", "Other object: o2 (blob)",
			"Changed file: a.go", "Deleted file: gone/", `File (novel): b\x1bc`, "Digest: 0a1b2c3d4e5f"}},
		{"nothing lost", machine.KeptWorkAnswered{Effect: machine.KeptWorkDiscardShown, Loss: &machine.KeptWorkLoss{Digest: "ffffffffffff"}},
			[]string{row, machine.PhraseKeptWorkLosesNothing.String(), "Digest: ffffffffffff"}},
		{"a landing", machine.KeptWorkAnswered{Effect: machine.KeptWorkLanded, Run: "r"},
			[]string{machine.KeptWorkAnsweredPhrase(machine.KeptWorkAnswered{Effect: machine.KeptWorkLanded}).String()}},
	} {
		var out bytes.Buffer
		if err := readOut(&out, tc.answer); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, want := out.String(), strings.Join(tc.want, "\n")+"\n"; got != want {
			t.Errorf("%s reads out:\n%s\nwant:\n%s", tc.name, got, want)
		}
	}
}
