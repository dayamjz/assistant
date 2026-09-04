package pipeline

import (
	"context"

	"github.com/dayamjz/assistant/internal/findings"
)

// Constant returns a stage implementation that reads nothing, writes nothing,
// and returns the same report every round. It exists so the wiring can be
// exercised without a real stage: the nine real stages are separate
// implementations of the same contract, and each one decides what it reads,
// what it writes, and what it reports.
//
// It panics when the report it would produce is one the pipeline refuses,
// which it decides by normalizing and validating exactly as the stage node
// adapter does. A caller who asks for such a report made a programming error
// rather than supplied input, and this helper exists so a test can start from
// a shape the real mechanism accepts; buildSchema panics on a defect in this
// package's own table for the same reason.
//
// The report is returned as given otherwise. The pipeline normalizes it again
// before recording it, so a finding built here with no action still becomes an
// ask and still holds the stage, which is P3 applying to this implementation
// exactly as it applies to a real one.
func Constant(summary string, found ...findings.Finding) Implementation {
	report := findings.Report{Summary: summary, Findings: found}
	if err := report.Normalize().Validate(); err != nil {
		panic("pipeline: Constant: " + err.Error())
	}
	return Implementation{
		NewBody: func() Body {
			return func(context.Context, Input) (Output, error) {
				return Output{Report: report}, nil
			}
		},
	}
}

// ConstantStages returns the nine stages, each of them Constant with the same
// report. It is the smallest complete Stages, and it is what a test that cares
// about the topology rather than about any stage's behaviour starts from.
func ConstantStages(summary string, found ...findings.Finding) Stages {
	var s Stages
	for _, row := range stageTable {
		*row.implementation(&s) = Constant(summary, found...)
	}
	return s
}
