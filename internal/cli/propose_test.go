package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// A Makefile declaring the targets init looks for, among the shapes it must
// not read as targets: a special target, an assignment, a recipe line, and a
// comment.
const declaringMakefile = `.PHONY: test lint build
VERSION := 1
# test: a comment is not a target
build:
	go build ./...
test: build
	go test ./...
lint:
	go vet ./...
`

// goModule is a module with one package and no tests, which the go tool
// tests and vets with an exit of zero and nothing else.
var goModule = map[string]string{
	"go.mod":     "module example.com/subject\n\ngo 1.21\n",
	"subject.go": "// Package subject is the code under validation.\npackage subject\n",
}

// plant writes files into the subject's working tree without committing them.
func plant(t *testing.T, subject string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(subject, name), []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

// commitAll commits everything in the subject's working tree.
func commitAll(t *testing.T, subject, message string) {
	t.Helper()
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", message)
}

// decodeInit decodes init's structured answer.
func decodeInit(t *testing.T, got invocation) machine.Init {
	t.Helper()
	var answer machine.Init
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("init does not decode: %v\n%s%s", err, got.stdout, got.stderr)
	}
	return answer
}

// offered is what an answer proposed, as key to command.
func offered(p machine.CommandProposals) map[string]string {
	out := map[string]string{}
	for _, c := range p.Proposed {
		out[c.Key] = c.Command
	}
	return out
}

// notProposed is the keys an answer offered nothing for.
func notProposed(p machine.CommandProposals) []string {
	var out []string
	for _, n := range p.NotProposed {
		out = append(out, n.Key)
	}
	return out
}

// documentAt reads the subject's configuration file, and reports false when
// there is none.
func documentAt(t *testing.T, subject string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(subject, config.RepositoryDocument))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("reading the configuration file: %v", err)
	}
	return string(data), true
}

// atTerminal drives the surface as a person at a terminal does: the process
// boundary established that standard input and standard error are one, and
// what the person types arrives on standard input.
func atTerminal(t *testing.T, h *home.Home, workingDir, typed string, args ...string) invocation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	var out, errs bytes.Buffer
	code := cli.Run(t.Context(), cli.Environment{
		Args:        append([]string{"--home", h.Root()}, args...),
		Stdout:      &out,
		Stderr:      &errs,
		Stdin:       strings.NewReader(typed),
		Getenv:      func(string) string { return "" },
		WorkingDir:  workingDir,
		Executable:  executable,
		Version:     "assistant (test)",
		Interactive: true,
	})
	return invocation{code: code, stdout: out.String(), stderr: errs.String()}
}

// TestInitOffersTheCommandsACheckoutDeclaresAndWritesNothingWithoutConsent
// is P1 for the offer: init reads a checkout's build files for the commands
// the test and lint stages run, offers them, and without consent writes
// nothing. Each subject differs from the next in one file, so each detector
// is shown both firing and, in a subject without its file, not: a Makefile's
// own targets, go.mod's convention, the Makefile preferred where both are
// there, a check target standing in for a missing test target, a
// package.json's scripts through the package manager its lock file names
// with npm's placeholder test script refused, and an assignment named like a
// target read as no target at all. commands.document is never offered.
func TestInitOffersTheCommandsACheckoutDeclaresAndWritesNothingWithoutConsent(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	for _, c := range []struct {
		name  string
		files map[string]string
		want  map[string]string
	}{
		{"a Makefile", map[string]string{"Makefile": declaringMakefile},
			map[string]string{"commands.test": "make test", "commands.lint": "make lint"}},
		{"go.mod", goModule,
			map[string]string{"commands.test": "go test ./...", "commands.lint": "go vet ./..."}},
		{"a Makefile with only a check target beside go.mod",
			map[string]string{"Makefile": "check:\n\tgo test ./...\n", "go.mod": goModule["go.mod"]},
			map[string]string{"commands.test": "make check", "commands.lint": "go vet ./..."}},
		{"package.json under yarn",
			map[string]string{"package.json": `{"scripts": {"test": "echo \"Error: no test specified\" && exit 1", "lint": "eslint ."}}`, "yarn.lock": ""},
			map[string]string{"commands.lint": "yarn run lint"}},
		{"an assignment named like a target",
			map[string]string{"Makefile": "test := 1\nlint ?= 2\n"},
			map[string]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			plant(t, subject, c.files)

			got := run(t, h, subject, "--json", "init")
			if got.code != machine.ExitOK {
				t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
			}
			answer := decodeInit(t, got).Commands
			if diff := offered(answer); !maps.Equal(diff, c.want) {
				t.Errorf("init offered %v, want %v", diff, c.want)
			}
			want := machine.CommandsPrinted
			if len(c.want) == 0 {
				want = machine.CommandsNone
			}
			if answer.Result != want {
				t.Errorf("the offer ended %q, want %q", answer.Result, want)
			}
			if !slices.Contains(notProposed(answer), string(config.KeyCommandsDocument)) {
				t.Errorf("init does not say it offered nothing for %s: %+v", config.KeyCommandsDocument, answer.NotProposed)
			}
			for _, p := range answer.Proposed {
				if p.Written {
					t.Errorf("%s is marked written with no consent given", p.Key)
				}
			}
			if _, there := documentAt(t, subject); there {
				t.Error("init wrote the configuration file with no consent given")
			}

			plain := run(t, h, subject, "init")
			if plain.code != machine.ExitOK {
				t.Fatalf("init exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
			}
			if !strings.Contains(plain.stdout, machine.CommandsPhrase(answer).String()) ||
				!strings.Contains(plain.stdout, machine.PhraseNoProseCommand.String()) {
				t.Errorf("init does not read the offer out:\n%s", plain.stdout)
			}
			for key, command := range c.want {
				if !strings.Contains(plain.stdout, key+" = "+command) {
					t.Errorf("init does not print %s = %s:\n%s", key, command, plain.stdout)
				}
			}
		})
	}
}

// TestAcceptedCommandsReachARunOnlyFromTheDefaultBranch is P1 and P7 for
// what init writes: --accept-commands merges the offered commands into the
// checkout's configuration file and keeps what the file already held, and
// the answer says nothing changes for a run until the file is on the default
// branch. Then the run shows both halves of that. With the file committed on
// a feature branch only, the commands are the pushed copy's and a run drops
// them, so the test stage holds with a report saying it established
// nothing. With the same file on the default branch on origin, the next run
// runs the test command, passes the stage, and holds where the configuration
// still names nothing, which is derived from stages.Holding rather than
// named.
func TestAcceptedCommandsReachARunOnlyFromTheDefaultBranch(t *testing.T) {
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	plant(t, subject, goModule)
	plant(t, subject, map[string]string{config.RepositoryDocument: "{\n  \"fix_rounds\": {\"review\": 1}\n}\n"})

	got := run(t, h, subject, "--json", "init", "--accept-commands")
	if got.code != machine.ExitOK {
		t.Fatalf("init --accept-commands exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	answer := decodeInit(t, got).Commands
	if answer.Result != machine.CommandsWritten {
		t.Fatalf("the offer ended %q, want %q: %+v", answer.Result, machine.CommandsWritten, answer)
	}
	for _, p := range answer.Proposed {
		if !p.Written {
			t.Errorf("%s was accepted and is not marked written", p.Key)
		}
	}
	written, there := documentAt(t, subject)
	if !there {
		t.Fatal("init --accept-commands wrote no configuration file")
	}
	layer, err := config.Parse(config.OriginTrusted, []byte(written))
	if err != nil {
		t.Fatalf("the written configuration file is refused: %v\n%s", err, written)
	}
	resolved, err := config.Resolve(config.Absent(config.OriginGlobal), layer)
	if err != nil {
		t.Fatalf("resolving the written file: %v", err)
	}
	if resolved.Config.Commands.Test != "go test ./..." || resolved.Config.Commands.Lint != "go vet ./..." ||
		resolved.Config.FixRounds.Review != 1 || layer.IsSet(config.KeyCommandsDocument) {
		t.Fatalf("the written file resolves to %+v and %+v, want the two commands beside the review limit it held:\n%s",
			resolved.Config.Commands, resolved.Config.FixRounds, written)
	}
	plain := run(t, h, subject, "init", "--accept-commands")
	if !strings.Contains(plain.stdout, machine.CommandsPhrase(machine.CommandProposals{Result: machine.CommandsNone}).String()) {
		t.Errorf("init offers again what the file now names:\n%s", plain.stdout)
	}

	// The file on a feature branch only: the pushed copy names the commands,
	// and a run drops them.
	serve(t, h)
	git(t, subject, "checkout", "--quiet", "-b", "carrying")
	commitAll(t, subject, "configure the checks on a branch")
	first := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change carrying its own commands").stdout)
	holding := stages.Holding(config.Defaults())
	if len(holding) == 0 || holding[0] != pipeline.StageTest {
		t.Fatalf("a run with no commands configured holds first at %v; this test reads the test stage's report there", holding)
	}
	if first.Decision == nil || first.Decision.Stage != pipeline.StageTest.String() {
		t.Fatalf("the run on the branch carrying the file did not hold at the test stage: %+v", first.Decision)
	}
	assertEstablishedNothing(t, first, pipeline.StageTest)
	if rejected, _ := first.Record.ConfigRejections.Get(); !slices.ContainsFunc(rejected, func(r string) bool {
		return strings.HasPrefix(r, string(config.KeyCommandsTest)+" ")
	}) {
		t.Errorf("the run does not record dropping the pushed %s: %v", config.KeyCommandsTest, rejected)
	}
	mustOK(t, run(t, h, subject, "--json", "--cancel"))

	// The same file landed on the default branch on origin.
	git(t, subject, "checkout", "--quiet", "main")
	git(t, subject, "merge", "--quiet", "--ff-only", "carrying")
	git(t, subject, "push", "--quiet", "origin", "main")
	standingOnChange(t, subject, "after")
	second := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change after the commands landed").stdout)
	configured := config.Defaults()
	configured.Commands.Test, configured.Commands.Lint = "go test ./...", "go vet ./..."
	stops := stages.Holding(configured)
	if len(stops) == 0 || stops[0] == pipeline.StageTest {
		t.Fatalf("a run with the commands configured holds first at %v; this test needs it past the test stage", stops)
	}
	if second.Decision == nil || second.Decision.Stage != stops[0].String() {
		t.Fatalf("the run after the commands landed holds at %+v, want %s", second.Decision, stops[0])
	}
	var tested bool
	for _, stage := range second.Stages {
		if stage.Stage == pipeline.StageTest.String() {
			tested = stage.Ran && stage.Outcome == pipeline.OutcomePassed
		}
	}
	if !tested {
		t.Errorf("the test stage did not run its command and pass: %+v", second.Stages)
	}
	assertEstablishedNothing(t, second, stops[0])
}

// assertEstablishedNothing holds a run's decision at stage to the report a
// stage with no command gives: one ask finding, and a summary saying it
// established nothing, so what was not checked is said rather than implied.
func assertEstablishedNothing(t *testing.T, held machine.Run, stage pipeline.Stage) {
	t.Helper()
	if held.Decision == nil || held.Decision.Stage != stage.String() {
		t.Fatalf("the run holds at %+v, want %s", held.Decision, stage)
	}
	var report *findings.Report
	for _, view := range held.Stages {
		if view.Stage == stage.String() {
			report = view.Report
		}
	}
	if report == nil || !strings.Contains(report.Summary, "established nothing") {
		t.Fatalf("the %s stage's report does not say it established nothing: %+v", stage, report)
	}
	if len(held.Decision.Findings) != 1 || held.Decision.Findings[0].Action != findings.ActionAsk {
		t.Fatalf("the %s hold carries %+v, want one ask finding", stage, held.Decision.Findings)
	}
}

// TestInitAsksAtATerminalAndWritesOnlyWhatWasAccepted is P1 for the
// interactive path: at a terminal init asks about each command in turn, and
// writes the ones answered y and nothing else. The same answers given to a
// structured invocation are never read, because a caller asking for a
// document is not prompted, so nothing is written there; and an invocation
// whose every answer is no writes nothing and leaves a run holding at the
// test stage with a report saying what was not checked.
func TestInitAsksAtATerminalAndWritesOnlyWhatWasAccepted(t *testing.T) {
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)

	h := newHome(t)
	subject := newSubject(t)
	plant(t, subject, goModule)
	structured := atTerminal(t, h, subject, "y\ny\n", "--json", "init")
	if answer := decodeInit(t, structured).Commands; answer.Result != machine.CommandsPrinted {
		t.Errorf("a structured init at a terminal ended its offer %q, want %q", answer.Result, machine.CommandsPrinted)
	}
	if strings.Contains(structured.stderr, machine.PhraseCommandsConsent.String()) {
		t.Errorf("a structured init asked for consent:\n%s", structured.stderr)
	}
	if _, there := documentAt(t, subject); there {
		t.Fatal("a structured init wrote the configuration file on answers it should never have read")
	}

	partly := atTerminal(t, h, subject, "y\nno\n", "init")
	if partly.code != machine.ExitOK {
		t.Fatalf("init at a terminal exited %s:\n%s%s", partly.code, partly.stdout, partly.stderr)
	}
	if !strings.Contains(partly.stderr, machine.PhraseCommandsConsent.String()) {
		t.Errorf("init at a terminal did not ask:\n%s", partly.stderr)
	}
	written, there := documentAt(t, subject)
	if !there {
		t.Fatal("init wrote nothing for a command answered y")
	}
	layer, err := config.Parse(config.OriginTrusted, []byte(written))
	if err != nil {
		t.Fatalf("the written file is refused: %v", err)
	}
	if !layer.IsSet(config.KeyCommandsTest) || layer.IsSet(config.KeyCommandsLint) {
		t.Fatalf("init wrote %v, want %s alone:\n%s", layer.SetKeys(), config.KeyCommandsTest, written)
	}
	if !strings.Contains(partly.stdout, "Written: commands.test = go test ./...") ||
		!strings.Contains(partly.stdout, "Offered: commands.lint = go vet ./...") {
		t.Errorf("init does not say which command it wrote:\n%s", partly.stdout)
	}

	// Every answer no, in a subject of its own: nothing is written and the
	// stage still holds, saying what it did not check.
	declinedHome := newHome(t)
	declinedSubject := newSubject(t)
	plant(t, declinedSubject, goModule)
	declined := atTerminal(t, declinedHome, declinedSubject, "n\n", "init")
	if declined.code != machine.ExitOK {
		t.Fatalf("init exited %s:\n%s%s", declined.code, declined.stdout, declined.stderr)
	}
	if !strings.Contains(declined.stdout, machine.CommandsPhrase(machine.CommandProposals{Result: machine.CommandsDeclined}).String()) {
		t.Errorf("init does not say every command was declined:\n%s", declined.stdout)
	}
	if _, there := documentAt(t, declinedSubject); there {
		t.Fatal("init wrote the configuration file after every command was declined")
	}
	serve(t, declinedHome)
	standingOnChange(t, declinedSubject, "declined")
	held := decodeRun(t, run(t, declinedHome, declinedSubject, "--json", "--intent", "a change with no commands").stdout)
	holding := stages.Holding(config.Defaults())
	if len(holding) == 0 {
		t.Fatal("a run with no commands configured holds nowhere, so there is no report to read")
	}
	assertEstablishedNothing(t, held, holding[0])
}

// TestInitOffersNothingItCannotWriteOrMustNotReplace is the offer's edges:
// a checkout that declares nothing is offered nothing and --accept-commands
// writes nothing; a key the configuration file already names is not offered,
// and accepting the rest leaves it as it was; and a configuration file
// internal/config refuses is not written into, which is reported without
// consent and refused with it, the second exiting as a failure.
func TestInitOffersNothingItCannotWriteOrMustNotReplace(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)

	t.Run("nothing declared", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		got := run(t, h, subject, "--json", "init", "--accept-commands")
		if got.code != machine.ExitOK {
			t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
		}
		answer := decodeInit(t, got).Commands
		if answer.Result != machine.CommandsNone || len(answer.Proposed) != 0 {
			t.Fatalf("init offered %+v in a checkout that declares nothing", answer)
		}
		want := []string{string(config.KeyCommandsTest), string(config.KeyCommandsLint), string(config.KeyCommandsDocument)}
		if !slices.Equal(notProposed(answer), want) {
			t.Errorf("init says it offered nothing for %v, want %v", notProposed(answer), want)
		}
		if _, there := documentAt(t, subject); there {
			t.Error("init wrote a configuration file with nothing to put in it")
		}
	})

	t.Run("a key already named", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		plant(t, subject, map[string]string{"Makefile": declaringMakefile,
			config.RepositoryDocument: `{"commands": {"test": "./run-tests"}}`})
		answer := decodeInit(t, run(t, h, subject, "--json", "init", "--accept-commands")).Commands
		if !slices.Equal(answer.Configured, []string{string(config.KeyCommandsTest)}) {
			t.Errorf("init reports %v already configured, want %s", answer.Configured, config.KeyCommandsTest)
		}
		if got := offered(answer); !maps.Equal(got, map[string]string{"commands.lint": "make lint"}) {
			t.Errorf("init offered %v, want the lint target alone", got)
		}
		written, _ := documentAt(t, subject)
		layer, err := config.Parse(config.OriginTrusted, []byte(written))
		if err != nil {
			t.Fatalf("the written file is refused: %v", err)
		}
		resolved, err := config.Resolve(config.Absent(config.OriginGlobal), layer)
		if err != nil {
			t.Fatalf("resolving the written file: %v", err)
		}
		if resolved.Config.Commands.Test != "./run-tests" || resolved.Config.Commands.Lint != "make lint" {
			t.Fatalf("the written file names %+v, want the person's test command kept beside make lint", resolved.Config.Commands)
		}
	})

	t.Run("a file it cannot read", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		const broken = `{"commands": "make test"}`
		plant(t, subject, map[string]string{"Makefile": declaringMakefile, config.RepositoryDocument: broken})

		unasked := run(t, h, subject, "--json", "init")
		if unasked.code != machine.ExitOK {
			t.Fatalf("init without consent exited %s over a file it cannot read:\n%s%s", unasked.code, unasked.stdout, unasked.stderr)
		}
		if answer := decodeInit(t, unasked).Commands; answer.Result != machine.CommandsUnreadable || answer.Detail == "" {
			t.Errorf("init without consent ended its offer %+v, want %q with why", answer, machine.CommandsUnreadable)
		}
		asked := run(t, h, subject, "--json", "init", "--accept-commands")
		if asked.code != machine.ExitFailure {
			t.Fatalf("init --accept-commands exited %s over a file it cannot write into, want %s", asked.code, machine.ExitFailure)
		}
		if answer := decodeInit(t, asked).Commands; answer.Result != machine.CommandsRefused || answer.Refusal != machine.CommandsFileUnreadable {
			t.Errorf("init --accept-commands ended its offer %+v, want refused as unreadable", answer)
		}
		if got, _ := documentAt(t, subject); got != broken {
			t.Fatalf("the file init could not read was changed to:\n%s", got)
		}
	})
}
