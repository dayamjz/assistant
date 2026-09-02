package agents

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Purpose names the role an invocation plays in a run. Every invocation
// records one, and P4 is stated in terms of it: a PurposeReview record is
// never a record of an invocation that carried a session.
//
// The values are the stages of PRD section 5 that reason with an agent, plus
// the fixer role. Push and pull request are absent because they run no agent.
type Purpose string

const (
	// PurposeIntent establishes what the change set out to do.
	PurposeIntent Purpose = "intent"
	// PurposeRebase reasons about a rebase that did not apply cleanly.
	PurposeRebase Purpose = "rebase"
	// PurposeReview reviews the change. It is the role P4 protects, so it is
	// always session-free; see the package documentation for how that is
	// arranged rather than remembered.
	PurposeReview Purpose = "review"
	// PurposeTest establishes the intent with a targeted check.
	PurposeTest Purpose = "test"
	// PurposeDocument updates documentation the change made stale.
	PurposeDocument Purpose = "document"
	// PurposeLint runs static analysis and reads its output.
	PurposeLint Purpose = "lint"
	// PurposeChecks reads the forge's checks and works on their failures.
	PurposeChecks Purpose = "checks"
	// PurposeFix applies findings. It is the one role that may keep a session
	// across rounds, and a Fixer is the only thing that produces it with one.
	PurposeFix Purpose = "fix"
)

// purposes is the set Recognized answers from. A purpose outside it is refused
// at the call rather than recorded, because an unrecognized purpose would make
// the P4 assertion over invocation records unanswerable.
var purposes = map[Purpose]struct{}{
	PurposeIntent:   {},
	PurposeRebase:   {},
	PurposeReview:   {},
	PurposeTest:     {},
	PurposeDocument: {},
	PurposeLint:     {},
	PurposeChecks:   {},
	PurposeFix:      {},
}

// Recognized reports whether this is one of the purposes this package defines.
func (p Purpose) Recognized() bool {
	_, ok := purposes[p]
	return ok
}

// String renders the purpose as it is recorded.
func (p Purpose) String() string { return string(p) }

// Shape is what an invocation asks the agent to return.
type Shape uint8

const (
	// ShapeText asks for the agent's own words. Result.Text carries them and
	// Result.Report is zero.
	ShapeText Shape = iota
	// ShapeReport asks for a stage report. The agent's final text is parsed by
	// internal/findings, which owns that vocabulary and its fail-closed
	// default; output that does not yield a valid report is a refusal, never a
	// zero Report returned with a nil error.
	ShapeReport
)

// String renders the shape for a diagnostic.
func (s Shape) String() string {
	switch s {
	case ShapeText:
		return "text"
	case ShapeReport:
		return "report"
	default:
		return "shape(" + fmt.Sprint(uint8(s)) + ")"
	}
}

// MaxPromptBytes bounds one prompt. The prompt is delivered on the agent's
// standard input rather than on its command line, so this is not a stand-in
// for an argument list ceiling: it bounds what one invocation may hand an
// agent, so a prompt assembled from something unbounded, such as a diff of
// whatever the branch happens to contain, is refused by name here rather than
// sent. It is far above any prompt a stage assembles from bounded
// configuration.
//
// It bounds the prompt this package is handed. What the agent then does with
// a prompt of that size, including refusing it for its own reasons, is the
// agent's own limit and not this one.
const MaxPromptBytes = 1 << 20

// Invocation is one prompt and everything the agent needs to answer it. It
// carries no session and no way to name one: session reuse lives on Fixer, so
// a caller cannot attach memory to a review by filling in a field.
type Invocation struct {
	// Prompt is what the agent is asked. It is written to the agent's standard
	// input rather than placed on its command line. It is content: it is never
	// recorded.
	Prompt string
	// Shape is what the agent must return.
	Shape Shape
	// Dir is the absolute working directory the agent runs in, normally the
	// isolated copy for the run. It is required, and it is the identity the
	// sweep seam is given, so an invocation that ran nowhere in particular
	// would leave a tree nothing could later match.
	Dir string
	// Env are environment variables set for this invocation only, applied over
	// the environment the runner was built with. A value may be a credential,
	// so no part of it reaches a Record.
	Env map[string]string
	// Model names the model to run, empty to leave the choice to the agent.
	Model string
}

// Validate reports whether the invocation can be run at all. Every refusal
// wraps ErrInvalidInvocation and names the field.
func (inv Invocation) Validate() error {
	if strings.TrimSpace(inv.Prompt) == "" {
		return &invocationFieldError{field: "Prompt", reason: "a prompt is required"}
	}
	if len(inv.Prompt) > MaxPromptBytes {
		return &invocationFieldError{
			field:  "Prompt",
			reason: fmt.Sprintf("%d bytes exceeds the limit of %d", len(inv.Prompt), MaxPromptBytes),
		}
	}
	if inv.Shape != ShapeText && inv.Shape != ShapeReport {
		return &invocationFieldError{field: "Shape", reason: "unrecognized shape " + inv.Shape.String()}
	}
	if inv.Dir == "" {
		return &invocationFieldError{field: "Dir", reason: "a working directory is required"}
	}
	if !filepath.IsAbs(inv.Dir) {
		return &invocationFieldError{field: "Dir", reason: "must be absolute, got " + inv.Dir}
	}
	for name := range inv.Env {
		switch {
		case name == "":
			return &invocationFieldError{field: "Env", reason: "an environment variable name may not be empty"}
		case strings.ContainsAny(name, "=\x00"):
			return &invocationFieldError{field: "Env", reason: "environment variable name " + name + " contains = or NUL"}
		}
	}
	for name, value := range inv.Env {
		if strings.ContainsRune(value, 0) {
			return &invocationFieldError{field: "Env", reason: "the value of " + name + " contains NUL"}
		}
	}
	return nil
}
