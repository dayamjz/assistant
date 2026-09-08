package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// attachOrStart stands for the row of PRD section 9's table that has no word
// of its own: attach to this branch's active run, and start one when there is
// none. A command line of flags alone drives it.
const attachOrStart = "(no verb: attach or start)"

// notAVerb stands for a command line that drives no row of that table. The
// section has the surface answer --help and --version, which are answers it
// gives rather than verbs it dispatches.
const notAVerb = "(not a verb)"

// section9Verbs is the eleven rows of PRD section 9's command table.
//
// The owner of the list is the specification, so it is restated here rather
// than asked of internal/cli: that package's verbs table is unexported, and a
// harness that read the table off the package under validation would be
// reporting that package agreeing with itself, which is the reason this one
// reads the subject through the fixture's git rather than through internal/vcs.
// internal/cli/cli_test.go holds the same eleven against the same section from
// the other side.
var section9Verbs = []string{
	attachOrStart, "init", "status", "runs", "rerun", "sync",
	"tasks", "watch", "doctor", "service", "eject",
}

// asked is the row of section 9's table a command line drove.
func asked(args []string) string {
	if len(args) == 0 {
		return attachOrStart
	}
	if !strings.HasPrefix(args[0], "-") {
		return args[0]
	}
	if args[0] == "--help" || args[0] == "--version" {
		return notAVerb
	}
	return attachOrStart
}

// spoken is one verb driven as a process: what it exited with, and whether the
// document it wrote decoded into the shape that verb answers.
type spoken struct {
	// verb names the command line, for a failure that says which one.
	verb string
	// drove is the row of PRD section 9's table this invocation drove, which
	// is what makes the set of rows reached readable. Counting invocations
	// would not: one run is attached to three times and the doctor is asked
	// twice, so a count of eleven is reached with three of the table's rows
	// never driven at all.
	drove string
	// code is what the process exited with.
	code machine.Code
	// decoded is whether the first document on standard output decoded into
	// the shape this verb answers, under a decoder that refuses a field the
	// shape does not declare and refuses an object carrying none. What it does
	// not establish is a required field: firstDocument says what that leaves.
	decoded bool
	// shaped re-answers decoded over other bytes, so a counterfeit can hand
	// this verb a document of another shape and derive what the real decoder
	// makes of it rather than stating the answer.
	shaped func(string) error
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
	// build does not honour. versionAsLine is decided by what the output is
	// rather than by what it begins with: something was written and it is not
	// the document that verb answers. Anchoring it on a literal prefix would
	// make the disclosure below disappear the day the line's wording changed,
	// which is the limit going unreported rather than being closed.
	versionAsDocument bool
	versionAsLine     bool
	// usageDocument is the document the surface answered a wrong command line
	// with, kept because it is the one wrong-shape answer this run produced:
	// a counterfeit hands it to a verb that promised a run rather than writing
	// a shape from nothing.
	usageDocument string
	// usageAsRun is what the verb-shape decoder makes of those same bytes when
	// it is asked for a run. A failure envelope accepted as a run is how
	// "decoded" comes to mean only that standard output began with an object.
	usageAsRun bool
	// promisedRunAt is where in spoke the first verb that answers a run
	// stands, so a counterfeit can put another shape in its place.
	promisedRunAt int
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
	requiresIdentifiedPeer(t)

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
	// The skip is the one walkableRun documents: answering the first hold
	// carries the run into the review stage, which fails on the isolated copy
	// nothing creates, and this test needs the run holding afterwards.
	started := drive[machine.Run](t, &observed, j, "--skip", pipeline.StageReview.String(),
		"--intent", "a change driven a command at a time")
	observed.promisedRunAt = len(observed.spoke) - 1
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
		drove:   "watch",
		code:    watching.Code,
		decoded: firstDocument(watching.Stdout, &fleet) == nil,
		shaped:  func(stdout string) error { return firstDocument(stdout, &machine.Tasks{}) },
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
	observed.versionAsLine = strings.TrimSpace(plain.Stdout) != "" &&
		firstDocument(plain.Stdout, &machine.Version{}) != nil
	drive[machine.Help](t, &observed, j, "--help")

	// The two failure codes, each from something that produces it for its own
	// reason. The wrong command line's own document is kept: it is the shape
	// no verb here promised, and the counterfeit that hands it to one that did
	// is what shows the decoder tells them apart.
	usage := j.Command("summon")
	observed.usageCode = usage.Code
	observed.usageDocument = strings.TrimSpace(usage.Stdout)
	observed.usageAsRun = firstDocument(observed.usageDocument, &machine.Run{}) == nil
	drive[machine.Eject](t, &observed, j, "eject", "--confirm")

	answers := journey.Check[surface]{
		What: "the command surface, verb by verb",
		Clauses: []journey.Clause[surface]{
			{
				States: "every verb PRD section 9's table names was driven",
				Holds: func(s surface) error {
					drove := map[string]bool{}
					for _, verb := range s.spoke {
						drove[verb.drove] = true
					}
					var missing []string
					for _, want := range section9Verbs {
						if !drove[want] {
							missing = append(missing, want)
						}
					}
					if len(missing) > 0 {
						return fmt.Errorf("%d of the %d verbs the specification's table names were not "+
							"driven: %v", len(missing), len(section9Verbs), missing)
					}
					return nil
				},
			},
			{
				States: "every verb answered a document of its own shape",
				Holds: func(s surface) error {
					for _, verb := range s.spoke {
						if !verb.decoded {
							return fmt.Errorf("%s did not answer a document of its own shape:\n  stdout: %s\n  stderr: %s",
								verb.verb, verb.stdout, verb.stderr)
						}
					}
					return nil
				},
			},
			{
				States: "every verb wrote something a driving agent could read",
				Holds: func(s surface) error {
					for _, verb := range s.spoke {
						if verb.documents == 0 {
							return fmt.Errorf("%s wrote nothing a driving agent could read:\n  stderr: %s",
								verb.verb, verb.stderr)
						}
					}
					return nil
				},
			},
			{
				States: "a verb that answers one thing wrote exactly one document",
				Holds: func(s surface) error {
					for _, verb := range s.spoke {
						if !verb.streams && verb.documents != 1 {
							return fmt.Errorf("%s wrote %d documents, and a driving agent reads one per "+
								"invocation:\n%s", verb.verb, verb.documents, verb.stdout)
						}
					}
					return nil
				},
			},
			{
				States:  "no verb wrote its answer to standard error",
				Absence: true,
				// A surface that wrote nothing to standard error at all would
				// show no document there whatever it did with its answers.
				// What makes the stream worth reading is that this product
				// does write to it, which its progress output shows.
				Possible: func(s surface) error {
					for _, verb := range s.spoke {
						if strings.TrimSpace(verb.stderr) != "" {
							return nil
						}
					}
					return errors.New("no verb wrote anything to standard error, so a stream nothing " +
						"reaches carrying no document says nothing about where answers go")
				},
				Holds: func(s surface) error {
					for _, verb := range s.spoke {
						if strings.Contains(verb.stderr, "{\"") {
							return fmt.Errorf("%s wrote a document to standard error, where a caller "+
								"redirecting standard output would not find it:\n%s", verb.verb, verb.stderr)
						}
					}
					return nil
				},
			},
			{
				States: "one run was carried across separate invocations with the service replaced between two",
				Holds: func(s surface) error {
					if !s.resumedAcrossInvocations {
						return errors.New("one run was not carried across separate invocations of the binary " +
							"with the service replaced in between")
					}
					return nil
				},
			},
			{
				States: "a decisive report that a run cannot start exits as an operational failure",
				Holds: func(s surface) error {
					if s.failureCode != machine.ExitFailure {
						return fmt.Errorf("a decisive report that a run cannot start exited %d, and an "+
							"operational failure is %d", s.failureCode, machine.ExitFailure)
					}
					return nil
				},
			},
			{
				States: "a command that is not a command exits as incorrect usage",
				Holds: func(s surface) error {
					if s.usageCode != machine.ExitUsage {
						return fmt.Errorf("a command that is not a command exited %d, and incorrect usage is %d",
							s.usageCode, machine.ExitUsage)
					}
					return nil
				},
			},
			{
				States: "incorrect usage and an operational failure are different codes",
				Holds: func(s surface) error {
					if s.usageCode == s.failureCode {
						return fmt.Errorf("incorrect usage and an operational failure both exit %d, and a "+
							"driving agent cannot tell a request it can fix from one it cannot", s.usageCode)
					}
					return nil
				},
			},
			{
				States: "a wrong command line answers a document rather than a bare exit code",
				Holds: func(s surface) error {
					if s.usageDocument == "" {
						return errors.New("a wrong command line wrote nothing on standard output, so a " +
							"driving agent is left with an exit code and no answer")
					}
					return nil
				},
			},
			{
				States: "the failure a wrong command line answers is not accepted as the shape a verb " +
					"answering a run promises",
				Absence: true,
				// The distinction is only worth anything if there were bytes of
				// another shape to try it on, which the wrong command line's
				// own answer supplies.
				Possible: func(s surface) error {
					if s.usageDocument == "" {
						return errors.New("no wrong-shape document was produced by this run, so nothing " +
							"was offered to the decoder that it could have wrongly accepted")
					}
					return nil
				},
				Holds: func(s surface) error {
					if s.usageAsRun {
						return fmt.Errorf("the failure envelope %s decodes into the shape a verb answering "+
							"a run promises, so recording a verb as having answered its own shape means "+
							"only that standard output began with an object", s.usageDocument)
					}
					return nil
				},
			},
			{
				States: "--json written before --version answered a document",
				Holds: func(s surface) error {
					if !s.versionAsDocument {
						return errors.New("--json written before --version did not answer a document")
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[surface]{
			{Named: "one of the verbs answered nothing a driving agent could decode",
				Break: func(s surface) surface {
					s.spoke = slices.Clone(s.spoke)
					s.spoke[0].decoded = false
					return s
				}},
			{Named: "a verb that promised a run answered the failure envelope this run produced instead",
				Break: func(s surface) surface {
					s.spoke = slices.Clone(s.spoke)
					at := s.promisedRunAt
					s.spoke[at].stdout = s.usageDocument
					s.spoke[at].decoded = s.spoke[at].shaped(s.usageDocument) == nil
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
			{Named: "a verb the specification names was never driven", Break: func(s surface) surface {
				s.spoke = slices.DeleteFunc(slices.Clone(s.spoke),
					func(verb spoken) bool { return verb.drove == "eject" })
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
			{Named: "a wrong command line answered nothing on standard output",
				Break: func(s surface) surface {
					s.usageDocument = ""
					return s
				}},
			{Named: "the failure envelope decoded into the shape a verb answering a run promises",
				Break: func(s surface) surface {
					s.usageAsRun = true
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
		drove:     asked(args),
		code:      answer.Code,
		decoded:   firstDocument(answer.Stdout, &into) == nil,
		shaped:    func(stdout string) error { var of T; return firstDocument(stdout, &of) },
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
