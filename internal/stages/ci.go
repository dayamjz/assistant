package stages

import (
	"context"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// CI is the checks stage: it watches CI and mergeability.
func CI(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{
						Findings: []findings.Finding{
							{
								Description: "CI stage not yet implemented. Monitor CI in your PR interface.",
								Action:      findings.ActionNote,
								Severity:    findings.SeverityInfo,
							},
						},
					},
				}, nil
			}
		},
	}
}
