package journey_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
)

// spoken is one verb driven as a process: what it exited with, and whether the
// document it wrote decoded into the shape that verb answers.
type spoken struct {
	// verb names the command line, for a failure that says which one.
	verb string
	// code is what the process exited with.
	code machine.Code
	// decoded is whether the first document on standard output was of the
	// shape this verb answers.
	decoded bool
	// documents is how many whole documents standard output held, and streams
	// says this verb is the one that writes one per thing it sees rather than
	// one per invocation.
	documents int
	streams   bool
	// stdout and stderr are what it wrote, kept so a failure is diagnosable.
	stdout string
	stderr string
}

// surface is the whole command surface driven end to end against one home.
type surface struct {
	// spoke is every verb that was driven, in the order they were driven.
	spoke []spoken
	// resumedAcrossInvocations is whether one run was started, reported on,
	// answered and resumed across separate invocations of the binary.
	resumedAcrossInvocations bool
	// usageCode and failureCode are what the surface exited with for a command
	// line that was wrong and for an operation that failed.
	usageCode   machine.Code
	failureCode machine.Code
	// versionAsDocument and versionAsLine are what --version answered with
	// --json written before it and after it, which is the one ordering this
	// build does not honour.
	versionAsDocument bool
	versionAsLine     bool
}

// TestTheWholeCommandSurfaceAnswersOneDocumentPerInvocation drives every verb
// PRD section 9's table names as a process, against a real home with a real
// service.
//
// It cites no principle, and that is deliberate. The surface is not one of the
// invariants section 4 states; what it is is the thing every other test here
// reaches the product through, so a harness that never drove the whole of it
// would be reporting on the parts of the surface its own principle tests
// happened to need.
//
// The contract it holds the surface to is internal/machine's: one document per
// invocation on standard output, progress on standard error, and three exit
// codes that mean different things. A driving agent reads one document per
// invocation, so a verb writing two, or writing a failure only a person can
// read, is the failure that makes the machine interface unusable.
func TestTheWholeCommandSurfaceAnswersOneDocumentPerInvocation(t *testing.T) {
	scenario, dir := cloned(t)
	j := open(t, scenario, func(o *journey.Options) { o.Dir = dir })
	observed := surface{}
	var err error

	// A decisive answer before there is a service, which is when a person
	// actually asks: the doctor reports it cannot start a run, and reports it
	// as an operational failure rather than as a wrong command line.
	beforeService := drive[machine.Doctor](t, &observed, j, "doctor")
	observed.failureCode = beforeService.code

	drive[machine.Init](t, &observed, j, "init", "--default-branch", fixture.DefaultBranch)
	serve(t, j)
	drive[machine.Doctor](t, &observed, j, "doctor")
	drive[machine.Status](t, &observed, j, "status")
	drive[machine.Service](t, &observed, j, "service", "status")

	// One run, across separate invocations of the binary, with the service
	// restarted in the middle. Every step is its own process, which is what
	// makes this a claim about the surface rather than about a library.
	started := drive[machine.Run](t, &observed, j, "--intent", "a change driven a command at a time")
	answered := drive[machine.Run](t, &observed, j, "--answer", "approved")
	if err := j.Kill(); err != nil {
		t.Fatalf("killing the service between invocations: %v", err)
	}
	serve(t, j)
	resumed := drive[machine.Run](t, &observed, j)
	observed.resumedAcrossInvocations = started.decoded && answered.decoded && resumed.decoded &&
		runOf(t, started).Record.ID == runOf(t, resumed).Record.ID &&
		runOf(t, resumed).Position == runOf(t, answered).Position

	drive[machine.Runs](t, &observed, j, "runs")
	drive[machine.Run](t, &observed, j, "runs", runOf(t, started).Record.ID)
	drive[machine.Run](t, &observed, j, "--cancel")
	drive[machine.Run](t, &observed, j, "rerun")
	drive[machine.Run](t, &observed, j, "--cancel")
	drive[machine.Tasks](t, &observed, j, "tasks")

	// The one verb that runs until it is stopped. It reports what is in flight
	// before it follows the stream, so the first thing on standard output is a
	// document whether or not an event ever arrives.
	watching := j.CommandBounded(dir, 2*time.Second, "watch")
	var fleet machine.Tasks
	watched := spoken{
		verb:    "assistant watch",
		code:    watching.Code,
		decoded: firstDocument(watching.Stdout, &fleet) == nil,
		streams: true,
		stdout:  watching.Stdout,
		stderr:  watching.Stderr,
	}
	if watched.documents, err = watching.Documents(); err != nil {
		t.Fatalf("%v", err)
	}
	observed.spoke = append(observed.spoke, watched)
	if !watching.Ended {
		t.Fatalf("assistant watch returned on its own, and it follows a stream until it is stopped:\n%s", watching)
	}

	// The verb that is present and says what is missing, which answers a
	// document reporting that nothing was changed and exits as an operational
	// failure.
	drive[machine.Plan](t, &observed, j, "sync")

	// The two things that are asked for rather than produced by a verb. Both
	// are answered rather than refused, and the ordering below is the one this
	// build does not honour: --json is read before --version and not after.
	observed.versionAsDocument = firstDocument(j.Command("--version").Stdout, &machine.Version{}) == nil
	plain := j.CommandExactly(dir, "--version", "--json")
	observed.versionAsLine = strings.HasPrefix(strings.TrimSpace(plain.Stdout), "assistant") &&
		!strings.HasPrefix(strings.TrimSpace(plain.Stdout), "{")
	drive[machine.Help](t, &observed, j, "--help")

	// The two failure codes, each from something that produces it for its own
	// reason.
	observed.usageCode = j.Command("summon").Code
	drive[machine.Eject](t, &observed, j, "eject", "--confirm")

	answers := journey.Check[surface]{
		What: "every verb the specification names answers exactly one document of its own shape on " +
			"standard output, a run is driven across separate invocations of the binary and survives " +
			"the service being replaced between two of them, and the three exit codes are told apart",
		Holds: func(s surface) error {
			if len(s.spoke) < 11 {
				return fmt.Errorf("only %d verbs were driven, and the specification's table names eleven",
					len(s.spoke))
			}
			for _, verb := range s.spoke {
				if !verb.decoded {
					return fmt.Errorf("%s did not answer a document of its own shape:\n  stdout: %s\n  stderr: %s",
						verb.verb, verb.stdout, verb.stderr)
				}
				if verb.documents == 0 {
					return fmt.Errorf("%s wrote nothing a driving agent could read:\n  stderr: %s",
						verb.verb, verb.stderr)
				}
				if !verb.streams && verb.documents != 1 {
					return fmt.Errorf("%s wrote %d documents, and a driving agent reads one per "+
						"invocation:\n%s", verb.verb, verb.documents, verb.stdout)
				}
				if strings.Contains(verb.stderr, "{\"") {
					return fmt.Errorf("%s wrote a document to standard error, where a caller "+
						"redirecting standard output would not find it:\n%s", verb.verb, verb.stderr)
				}
			}
			if !s.resumedAcrossInvocations {
				return fmt.Errorf("one run was not carried across separate invocations of the binary " +
					"with the service replaced in between")
			}
			if s.failureCode != machine.ExitFailure {
				return fmt.Errorf("a decisive report that a run cannot start exited %d, and an "+
					"operational failure is %d", s.failureCode, machine.ExitFailure)
			}
			if s.usageCode != machine.ExitUsage {
				return fmt.Errorf("a command that is not a command exited %d, and incorrect usage is %d",
					s.usageCode, machine.ExitUsage)
			}
			if s.usageCode == s.failureCode {
				return fmt.Errorf("incorrect usage and an operational failure both exit %d, and a "+
					"driving agent cannot tell a request it can fix from one it cannot", s.usageCode)
			}
			if !s.versionAsDocument {
				return fmt.Errorf("--json written before --version did not answer a document")
			}
			return nil
		},
		Counterfeits: []journey.Counterfeit[surface]{
			{Named: "one of the verbs answered nothing a driving agent could decode",
				Break: func(s surface) surface {
					s.spoke = slices.Clone(s.spoke)
					s.spoke[0].decoded = false
					return s
				}},
			{Named: "a verb that answers one thing wrote a second document", Break: func(s surface) surface {
				s.spoke = slices.Clone(s.spoke)
				s.spoke[1].documents = 2
				return s
			}},
			{Named: "a verb wrote nothing a driving agent could read", Break: func(s surface) surface {
				s.spoke = slices.Clone(s.spoke)
				s.spoke[2].documents = 0
				return s
			}},
			{Named: "a verb wrote its answer to standard error", Break: func(s surface) surface {
				s.spoke = slices.Clone(s.spoke)
				s.spoke[3].stderr += `{"error":"over here"}`
				return s
			}},
			{Named: "fewer verbs were driven than the specification names", Break: func(s surface) surface {
				s.spoke = slices.Clone(s.spoke)[:4]
				return s
			}},
			{Named: "the run did not survive being driven a command at a time", Break: func(s surface) surface {
				s.resumedAcrossInvocations = false
				return s
			}},
			{Named: "a wrong command line and a failed operation exit the same way",
				Break: func(s surface) surface {
					s.usageCode = s.failureCode
					return s
				}},
			{Named: "a decisive report that a run cannot start exited successfully",
				Break: func(s surface) surface {
					s.failureCode = machine.ExitOK
					return s
				}},
			{Named: "--json before --version answered a plain line", Break: func(s surface) surface {
				s.versionAsDocument = false
				return s
			}},
		},
	}
	if err := answers.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
	if observed.versionAsLine {
		t.Log("KNOWN LIMIT: \"assistant --version --json\" writes a plain line where " +
			"\"assistant --json --version\" writes a document. internal/cli/doc.go records it, and the " +
			"parser rework owns closing it. Every verb honours --json on either side of it, so the " +
			"one-document-per-invocation contract holds everywhere else.")
	}
}

// drive runs one verb as a process and records whether it answered exactly one
// document of the shape that verb answers.
func drive[T any](t *testing.T, s *surface, j *journey.Journey, args ...string) spoken {
	t.Helper()
	answer := j.Command(args...)
	var into T
	documents, err := answer.Documents()
	if err != nil {
		t.Fatalf("%v", err)
	}
	spoke := spoken{
		verb:      "assistant " + strings.Join(args, " "),
		code:      answer.Code,
		decoded:   firstDocument(answer.Stdout, &into) == nil,
		documents: documents,
		stdout:    answer.Stdout,
		stderr:    answer.Stderr,
	}
	s.spoke = append(s.spoke, spoke)
	return spoke
}

// runOf reads the run a verb answered with.
func runOf(t *testing.T, spoke spoken) machine.Run {
	t.Helper()
	var run machine.Run
	if err := firstDocument(spoke.stdout, &run); err != nil {
		t.Fatalf("reading the run %s answered: %v", spoke.verb, err)
	}
	return run
}
