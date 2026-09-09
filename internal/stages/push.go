package stages

import (
	"context"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Push is the push stage: it forwards the verified commit to the target.
func Push(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{
						Findings: []findings.Finding{
							{
								Description: "Push stage not yet implemented. Push manually after verification.",
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
