package stages

import (
	"context"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Document is the document stage: it updates documentation the change made stale.
func Document(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{
						Findings: []findings.Finding{
							{
								Description: "Document stage not yet implemented. Manual documentation review required.",
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
