package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// Asking a verb that takes a subcommand for help is answered rather than
// refused: --help on service, gate, host and coordinator exits successfully and lists
// every row of that verb's subcommand table, name and summary, the way the
// top-level help lists the verb table. The table is read from what each verb
// dispatches on, so a row added there and left out of the help fails here.
// Each spelling the flag package answers to is asked, and the structured form
// is the help document.
func TestHelpOnAVerbWithSubcommandsListsThem(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	tables := cli.Subcommands()
	if len(tables) != 4 {
		t.Fatalf("the verbs that take a subcommand are %v, want service, gate, host and coordinator", tables)
	}
	for verb, rows := range tables {
		if len(rows) == 0 {
			t.Fatalf("assistant %s has no subcommands to list", verb)
		}
		for _, flag := range []string{"--help", "-h", "-help"} {
			got := run(t, h, subject, verb, flag)
			if got.code != machine.ExitOK {
				t.Fatalf("assistant %s %s exited %s, want ok:\n%s%s", verb, flag, got.code, got.stdout, got.stderr)
			}
			for _, row := range rows {
				if !strings.Contains(got.stdout, row[0]) || !strings.Contains(got.stdout, row[1]) {
					t.Errorf("assistant %s %s does not list %s, %q:\n%s", verb, flag, row[0], row[1], got.stdout)
				}
			}
		}
		structured := run(t, h, subject, "--json", verb, "--help")
		var help machine.Help
		if structured.code != machine.ExitOK || json.Unmarshal([]byte(structured.stdout), &help) != nil {
			t.Fatalf("assistant --json %s --help exited %s with:\n%s", verb, structured.code, structured.stdout)
		}
		if !strings.Contains(help.Usage, rows[0][1]) {
			t.Errorf("assistant --json %s --help does not carry the table:\n%s", verb, help.Usage)
		}
	}
}

// A verb that takes a subcommand, given none or given a word that is not one,
// stays incorrect usage, and the refusal is phrased as one: the phrase-table
// row saying what the verb needs, with the subcommands to pick from filled
// in. The row is held to P13's derived vocabulary with the names it lists,
// since those are what the person reads.
func TestAVerbGivenNoSubcommandIsRefusedSayingWhatItNeeds(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	for verb, rows := range cli.Subcommands() {
		var names []string
		for _, row := range rows {
			names = append(names, row[0])
		}
		for _, args := range [][]string{{verb}, {verb, "nonsense"}} {
			got := run(t, h, subject, args...)
			if got.code != machine.ExitUsage {
				t.Fatalf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
			}
			phrase := machine.SubcommandPhrase(len(args) > 1, names)
			if !strings.Contains(got.stderr, phrase.String()) {
				t.Errorf("assistant %s said:\n%s\nwant the row %q", strings.Join(args, " "), got.stderr, phrase)
			}
			vocabtest.Assert(t, phrase.String())
		}
	}
}
