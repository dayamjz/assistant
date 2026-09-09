package stages

import (
	"context"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Lint is the lint stage: it runs static analysis over the change.
func Lint(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{
						Findings: []findings.Finding{
							{
								Description: "Lint stage not yet implemented. Manual linting required.",
								Action:      findings.ActionAsk,
								Severity:    findings.SeverityWarning,
							},
						},
					},
				}, nil
			}
		},
	}
}
