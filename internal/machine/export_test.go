package machine

import "github.com/dayamjz/assistant/internal/agents"

// CeilingCountReports is the capability each ceilingCounts row reads an
// agent's declaration of, in the table's order, for the test that holds the
// table to agents.CountCapabilities.
func CeilingCountReports() []agents.Capability {
	out := make([]agents.Capability, len(ceilingCounts))
	for i, row := range ceilingCounts {
		out[i] = row.reports
	}
	return out
}
