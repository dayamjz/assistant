package agents

import "context"

// StageAgent is the agent as a stage body holds it: it runs one invocation at
// a time, with no session, and it is not a route to one.
//
// It is deliberately not a Runner and does not embed one. P4 keeps reviewing
// and fixing in separate memory, and the type split on Runner and Fixer is
// where that lives, but the split alone does not survive a stage body being
// handed a Runner: OpenFixer opens a fixer session from any Runner it is
// given, so a body holding one reaches a session by calling a function this
// package exports. Handing it a StageAgent removes the value that call needs.
// There is no Runner here to pass, and this is a struct rather than an
// interface over one, so an assertion back to Runner or SessionRunner finds
// nothing. Keeping the reviewer out of a fixer session is therefore something
// a stage body cannot do rather than something it is asked not to.
//
// It restricts the route to a session and nothing else. Run takes the same
// purpose Runner.Run takes, including PurposeFix, which on a Runner means a
// fix round that keeps no memory across rounds. That is the mode PRD section 8
// leaves an adapter without resumable sessions, and refusing it here would
// narrow a path P4 does not ask to be narrowed.
//
// # Where this type lives, and why here rather than beside the stage bodies
//
// The runner is an unexported field, so code outside this package cannot read
// it, and code inside this package can. That is the whole reason this type is
// in internal/agents: the nine stage bodies live in internal/stages, and a
// wrapper declared there would be readable by the bodies it is meant to bound,
// which would put the guarantee back in a reviewer's hands. P14 agrees, since
// this package already owns the P4 split.
//
// The residual gap is that package boundary and not a claim beyond it. Nothing
// stops a caller that already holds a Runner from using it directly; what this
// removes is a stage body's route to one, given wiring that hands bodies this
// and never a Runner. TestStageDepsIsNoRouteToAFixerSession in internal/stages
// is what holds that wiring to it.
//
// The zero StageAgent has no runner and refuses with ErrNoStageAgent, so a
// value that was never given one fails as a typed result rather than panicking
// at the first invocation.
type StageAgent struct {
	runner Runner
}

// NewStageAgent returns the agent as a stage body may hold it.
//
// The wiring that resolved an agent is what calls this. A stage body cannot:
// it has no Runner to call it with, which is the point of the type.
func NewStageAgent(r Runner) StageAgent { return StageAgent{runner: r} }

// Run executes one invocation with no session, exactly as Runner.Run does, and
// refuses with ErrNoStageAgent when this value was never given a runner.
//
// The invocation is validated and its process tree is owned by the Runner
// underneath, so what a body gets here is that contract and not a second one.
func (s StageAgent) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if s.runner == nil {
		return Result{}, ErrNoStageAgent
	}
	return s.runner.Run(ctx, purpose, inv)
}
