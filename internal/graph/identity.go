package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// edgeDigest fingerprints the edge vector a run's per-edge counters are
// indexed against. A checkpoint carries the digest of the graph it accrued its
// counters under, and a graph that renders a different one is refused rather
// than resumed, because Traversals and Fingerprints are positional: index 3
// means whatever edge 3 is, and nothing in the vectors says which edge that
// was.
//
// The digest covers, per index, the edge's endpoints, its guard, whether it is
// a back edge, and the bound it declares. Endpoints, guard, and back-edge
// status are what make a count and a fingerprint belong to one edge. The bound
// is in for a different reason: it is the value the count is compared against,
// so a bound that changed between the count accruing and the comparison fires
// early or late with nothing to say it did.
//
// It covers nothing else. Nodes and state keys are outside it, and a
// checkpoint's position and state are checked against them separately. Node
// bodies are outside it and outside every other check a checkpoint meets: a
// graph rebuilt with different bodies under the same node names resumes, on
// purpose, because a body is code the caller supplies rather than something a
// checkpoint records.
func edgeDigest(edges []Edge, back []bool) string {
	// The version prefix is what keeps a later change to this rendering from
	// digesting alike with the current one.
	rendered := []byte(fmt.Sprintf("graph.edges/1\n%d\n", len(edges)))
	for i, e := range edges {
		isBack := i < len(back) && back[i]
		// Every string is quoted, so no combination of names, keys, or values
		// can be run together into the rendering of another edge.
		rendered = append(rendered, fmt.Sprintf("%d %q %q rounds=%d back=%t %s\n",
			i, e.From, e.To, e.Rounds, isBack, guardDigest(e.Guard))...)
	}
	sum := sha256.Sum256(rendered)
	return hex.EncodeToString(sum[:])
}

// guardDigest renders an edge's guard for the digest. A guard is data, so the
// whole predicate is rendered: two edges that differ only in what they route
// on are different edges.
func guardDigest(g *Guard) string {
	if g == nil {
		return "unguarded"
	}
	value, err := json.Marshal(g.Value)
	if err != nil {
		// A built graph's guards have been checked against their key's
		// declared kind, which no unencodable value passes. Rendering the
		// failure rather than dropping it keeps two guards this could not
		// encode from digesting alike.
		return fmt.Sprintf("guard %q %s unencodable=%q", g.Key, g.Op, err.Error())
	}
	return fmt.Sprintf("guard %q %s %s", g.Key, g.Op, value)
}
