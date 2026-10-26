package cli

import (
	"bytes"
	"strconv"
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
			"Changed file: a.go", "Deleted file: gone/", `File (novel): "b\x1bc"`, "Digest: 0a1b2c3d4e5f"}},
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

// exactText prints a plain value as it is and quotes every other, so a
// person can tell any two values apart and read each back exactly: the
// reviewer's pair, a line break and the backslash spelling of its escape,
// prints apart, and so do values differing only by a quote, a space at
// either end, a space other than the ASCII one, a character a terminal
// draws as nothing, or a byte that is not UTF-8. Every quoted form reads
// back with strconv.Unquote to the value it came from, and no two values
// here print the same.
func TestExactTextNeverPrintsTwoValuesTheSame(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{
		"notes.txt":         "notes.txt",
		"a b/c-d_é.go":      "a b/c-d_é.go",
		"0a1b2c3d4e5f":      "0a1b2c3d4e5f",
		"refs/tags/v1":      "refs/tags/v1",
		"HEAD@{1}":          "HEAD@{1}",
		"notes\n.txt":       `"notes\n.txt"`,
		`notes\x0a.txt`:     `"notes\\x0a.txt"`,
		`say "hi"`:          `"say \"hi\""`,
		" lead":             `" lead"`,
		"trail ":            `"trail "`,
		"nb\u00a0sp":        `"nb\u00a0sp"`,
		"zero\u200bwidth":   `"zero\u200bwidth"`,
		"bad\xffbyte":       `"bad\xffbyte"`,
		"esc\x1b[2J":        `"esc\x1b[2J"`,
		"":                  `""`,
		`"quoted"`:          `"\"quoted\""`,
		"c1\u0085control":   `"c1\u0085control"`,
		"tab\there":         `"tab\there"`,
		"rtl\u202eoverride": `"rtl\u202eoverride"`,
	} {
		got := exactText(value)
		if got != want {
			t.Errorf("exactText(%q) = %s, want %s", value, got, want)
		}
		if strings.HasPrefix(got, `"`) {
			if back, err := strconv.Unquote(got); err != nil || back != value {
				t.Errorf("%s reads back as %q, %v; want %q", got, back, err, value)
			}
		}
	}
	values := []string{"notes\n.txt", `notes\x0a.txt`, "a", `"a"`, " a", "a ", "a\u00a0", "a\u200b", "a\xff", "a\xc3", "a\u00ff", `a\xff`}
	seen := map[string]string{}
	for _, v := range values {
		got := exactText(v)
		if other, ok := seen[got]; ok {
			t.Errorf("%q and %q both print as %s", other, v, got)
		}
		seen[got] = v
	}
}
