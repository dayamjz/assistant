package stages

import (
	"context"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Rebase is the rebase stage: it fetches fresh upstream and rebases onto it.
func Rebase(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{
						Findings: []findings.Finding{
							{
								Description: "Rebase stage not yet implemented. Manual rebase required onto latest upstream.",
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
