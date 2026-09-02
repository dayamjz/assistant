package graph

import (
	"fmt"
	"sort"
)

// Builder collects nodes, edges, and state key declarations, and refuses to
// produce a Graph from any of the combinations this package documents as
// invalid. Construction-time failure is the point: the defects it catches are
// close to invisible at run time and expensive to diagnose once they are.
//
// A Builder is not safe for concurrent use. Build may be called more than
// once and does not consume the Builder.
type Builder struct {
	start string
	keys  []Key
	nodes []Node
	edges []Edge
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder { return &Builder{} }

// Start declares the node a run begins at. It returns the Builder so
// declarations can be chained.
func (b *Builder) Start(name string) *Builder {
	b.start = name
	return b
}

// Key declares a state key. Declaring the same name twice is a construction
// error rather than a redefinition.
func (b *Builder) Key(k Key) *Builder {
	b.keys = append(b.keys, k)
	return b
}

// Node declares a node. Declaring the same name twice is a construction error.
func (b *Builder) Node(n Node) *Builder {
	b.nodes = append(b.nodes, n)
	return b
}

// Edge declares a transition. Edges leaving one node are evaluated in the
// order they are declared here.
func (b *Builder) Edge(e Edge) *Builder {
	b.edges = append(b.edges, e)
	return b
}

// Build checks every construction rule and returns the graph, or a *BuildError
// listing every rule broken. It never returns a partially valid graph.
func (b *Builder) Build() (*Graph, error) {
	c := &checker{b: b}
	c.checkKeys()
	c.checkNodes()
	c.checkForksAndJoins()
	c.checkStart()
	c.checkEdges()
	c.checkEdgeDeterminism()
	c.checkSingleWriter()
	c.analyze()
	c.checkBoundedCycles()
	c.checkJoinCycles()

	if len(c.violations) > 0 {
		return nil, &BuildError{Violations: c.violations}
	}
	return c.graph(), nil
}

// checker accumulates violations while it walks a builder's declarations. It
// keeps every check running so one refusal reports everything wrong at once.
type checker struct {
	b          *Builder
	violations []Violation

	keyIndex map[string]Key
	index    map[string]int
	outgoing [][]int
	adjacent [][]int
	dist     []int
	reaches  [][]bool
	back     []bool

	// structural is true once nodes, edges, and the start node are coherent
	// enough for the reachability analysis to mean anything.
	structural bool
}

func (c *checker) refuse(rule Rule, format string, args ...any) {
	c.violations = append(c.violations, Violation{Rule: rule, Detail: fmt.Sprintf(format, args...)})
}

func (c *checker) checkKeys() {
	c.keyIndex = make(map[string]Key, len(c.b.keys))
	for _, k := range c.b.keys {
		if k.Name == "" {
			c.refuse(RuleWellFormed, "a state key was declared with an empty name")
			continue
		}
		if _, dup := c.keyIndex[k.Name]; dup {
			c.refuse(RuleWellFormed, "state key %q is declared more than once", k.Name)
			continue
		}
		if k.Kind == KindInvalid {
			c.refuse(RuleDeclaredKey, "state key %q declares no kind", k.Name)
			continue
		}
		if !k.Merge.compatible(k.Kind) {
			c.refuse(RuleDeclaredKey, "state key %q declares merge rule %s, which cannot apply to a %s key",
				k.Name, k.Merge, k.Kind)
			continue
		}
		c.keyIndex[k.Name] = k
	}
}

func (c *checker) checkNodes() {
	c.index = make(map[string]int, len(c.b.nodes))
	if len(c.b.nodes) == 0 {
		c.refuse(RuleWellFormed, "the graph declares no nodes")
	}
	for i, n := range c.b.nodes {
		if n.Name == "" {
			c.refuse(RuleWellFormed, "the node at position %d was declared with an empty name", i)
			continue
		}
		if _, dup := c.index[n.Name]; dup {
			c.refuse(RuleWellFormed, "node %q is declared more than once", n.Name)
			continue
		}
		c.index[n.Name] = i
		if n.NewBody == nil {
			c.refuse(RuleWellFormed, "node %q declares no body constructor", n.Name)
		}
		c.checkNodeKeys(n)
		c.checkHalt(n)
	}
}

func (c *checker) checkNodeKeys(n Node) {
	for _, list := range []struct {
		what string
		keys []string
	}{{"reads", n.Reads}, {"writes", n.Writes}} {
		seen := make(map[string]struct{}, len(list.keys))
		for _, key := range list.keys {
			if _, ok := c.keyIndex[key]; !ok {
				c.refuse(RuleDeclaredKey, "node %q %s state key %q, which the graph does not declare",
					n.Name, list.what, key)
				continue
			}
			if _, dup := seen[key]; dup {
				c.refuse(RuleWellFormed, "node %q lists state key %q in its %s more than once",
					n.Name, key, list.what)
				continue
			}
			seen[key] = struct{}{}
		}
	}
}

func (c *checker) checkHalt(n Node) {
	if n.Halt == nil {
		return
	}
	if n.Halt.Question == "" {
		c.refuse(RuleWellFormed, "the halt point on node %q asks no question", n.Name)
	}
	spec, ok := c.keyIndex[n.Halt.Into]
	if !ok {
		c.refuse(RuleDeclaredKey, "the halt point on node %q writes its answer into state key %q, which the graph does not declare",
			n.Name, n.Halt.Into)
	} else if spec.Kind != KindText {
		c.refuse(RuleDeclaredKey, "the halt point on node %q writes its answer into state key %q, which declares %s rather than text",
			n.Name, n.Halt.Into, spec.Kind)
	}
	seen := make(map[string]struct{}, len(n.Halt.Options))
	for _, opt := range n.Halt.Options {
		if opt == "" {
			c.refuse(RuleWellFormed, "the halt point on node %q declares an empty answer option", n.Name)
			continue
		}
		if _, dup := seen[opt]; dup {
			c.refuse(RuleWellFormed, "the halt point on node %q declares answer option %q more than once", n.Name, opt)
			continue
		}
		seen[opt] = struct{}{}
	}
}

func (c *checker) checkForksAndJoins() {
	forks := make(map[string]string, len(c.b.nodes))
	joins := make(map[string]string, len(c.b.nodes))
	for _, n := range c.b.nodes {
		if n.Fork != "" {
			if other, dup := forks[n.Fork]; dup {
				c.refuse(RuleWellFormed, "fan-out %q is opened by both node %q and node %q", n.Fork, other, n.Name)
			} else {
				forks[n.Fork] = n.Name
			}
		}
		if n.Join != "" {
			if other, dup := joins[n.Join]; dup {
				c.refuse(RuleWellFormed, "fan-out %q is closed by both node %q and node %q", n.Join, other, n.Name)
			} else {
				joins[n.Join] = n.Name
			}
		}
		if n.Fork != "" && n.Fork == n.Join {
			c.refuse(RuleWellFormed, "node %q both opens and closes fan-out %q", n.Name, n.Fork)
		}
	}
	names := make([]string, 0, len(joins))
	for name := range joins {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := forks[name]; !ok {
			c.refuse(RuleWellFormed, "node %q closes fan-out %q, which no node opens", joins[name], name)
		}
	}
}

func (c *checker) checkStart() {
	if c.b.start == "" {
		c.refuse(RuleWellFormed, "the graph declares no start node")
		return
	}
	if _, ok := c.index[c.b.start]; !ok {
		c.refuse(RuleWellFormed, "the start node %q is not a declared node", c.b.start)
	}
}

func (c *checker) checkEdges() {
	for i, e := range c.b.edges {
		if _, ok := c.index[e.From]; !ok {
			c.refuse(RuleWellFormed, "edge %d leaves %q, which is not a declared node", i, e.From)
		}
		if _, ok := c.index[e.To]; !ok {
			c.refuse(RuleWellFormed, "edge %d enters %q, which is not a declared node", i, e.To)
		}
		if e.Rounds < 0 {
			c.refuse(RuleBoundedCycle, "edge %d from %q to %q declares a negative bound of %d",
				i, e.From, e.To, e.Rounds)
		}
		if e.Guard == nil {
			continue
		}
		spec, ok := c.keyIndex[e.Guard.Key]
		if !ok {
			c.refuse(RuleDeclaredKey, "edge %d from %q to %q is guarded on state key %q, which the graph does not declare",
				i, e.From, e.To, e.Guard.Key)
			continue
		}
		if err := e.Guard.check(spec); err != nil {
			c.refuse(RuleDeclaredKey, "edge %d from %q to %q: %s", i, e.From, e.To, err)
		}
	}
}

func (c *checker) checkEdgeDeterminism() {
	unconditional := make(map[string]int)
	for i, e := range c.b.edges {
		if prev, ok := unconditional[e.From]; ok {
			c.refuse(RuleDeterministicEdges,
				"edge %d from %q to %q can never be taken: edge %d already leaves %q unconditionally",
				i, e.From, e.To, prev, e.From)
			continue
		}
		if e.Guard == nil {
			unconditional[e.From] = i
		}
	}
}

func (c *checker) checkSingleWriter() {
	writers := make(map[string][]string)
	for _, n := range c.b.nodes {
		if n.Name == "" {
			continue
		}
		for _, key := range n.Writes {
			writers[key] = appendWriter(writers[key], n.Name)
		}
		if n.Halt != nil && n.Halt.Into != "" {
			writers[n.Halt.Into] = appendWriter(writers[n.Halt.Into], n.Name)
		}
	}
	names := make([]string, 0, len(writers))
	for key := range writers {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		spec, ok := c.keyIndex[key]
		if !ok {
			// Already refused as an undeclared key.
			continue
		}
		if len(writers[key]) > 1 && spec.Merge == MergeNone {
			c.refuse(RuleSingleWriter,
				"state key %q is written by %v and declares no merge rule; a key more than one node can write must declare one",
				key, writers[key])
		}
	}
}

func appendWriter(existing []string, name string) []string {
	for _, n := range existing {
		if n == name {
			return existing
		}
	}
	return append(existing, name)
}

// analyze computes the outgoing edge lists, hop distances from the start node,
// the reachability closure, and which edges are back edges. It runs only when
// the declarations are coherent enough for the answers to mean anything.
func (c *checker) analyze() {
	if c.b.start == "" {
		return
	}
	if _, ok := c.index[c.b.start]; !ok {
		return
	}
	for _, e := range c.b.edges {
		if _, ok := c.index[e.From]; !ok {
			return
		}
		if _, ok := c.index[e.To]; !ok {
			return
		}
	}
	if len(c.b.nodes) != len(c.index) {
		// Duplicate or unnamed nodes; already refused.
		return
	}
	c.structural = true

	count := len(c.b.nodes)
	c.outgoing = make([][]int, count)
	c.adjacent = make([][]int, count)
	for i, e := range c.b.edges {
		from := c.index[e.From]
		c.outgoing[from] = append(c.outgoing[from], i)
		c.adjacent[from] = append(c.adjacent[from], c.index[e.To])
	}

	c.dist = bfs(c.adjacent, c.index[c.b.start])
	for i, node := range c.b.nodes {
		if c.dist[i] < 0 {
			c.refuse(RuleWellFormed, "node %q is unreachable from the start node %q", node.Name, c.b.start)
			c.structural = false
		}
	}
	if !c.structural {
		return
	}

	c.reaches = make([][]bool, count)
	for i := range c.reaches {
		c.reaches[i] = bfsFromEdges(c.adjacent, i)
	}

	c.back = make([]bool, len(c.b.edges))
	for i, e := range c.b.edges {
		from, to := c.index[e.From], c.index[e.To]
		c.back[i] = c.reaches[to][from] && c.dist[to] <= c.dist[from]
	}
}

func (c *checker) checkBoundedCycles() {
	if !c.structural {
		return
	}
	for i, e := range c.b.edges {
		if c.back[i] && e.Rounds <= 0 {
			c.refuse(RuleBoundedCycle,
				"edge %d from %q to %q closes a cycle and declares no bound; every back edge must carry one",
				i, e.From, e.To)
		}
	}
}

// checkJoinCycles enforces that a cycle through a join includes that join's
// fork. A cycle through join J that avoids fork F exists exactly when J can
// still reach itself once F is removed, so that is what it tests.
func (c *checker) checkJoinCycles() {
	if !c.structural {
		return
	}
	forks := make(map[string]int, len(c.b.nodes))
	for i, n := range c.b.nodes {
		if n.Fork != "" {
			forks[n.Fork] = i
		}
	}
	for j, n := range c.b.nodes {
		if n.Join == "" {
			continue
		}
		f, ok := forks[n.Join]
		if !ok {
			// Already refused: the join closes a fan-out nothing opens.
			continue
		}
		if reachesWithout(c.adjacent, j, j, f) {
			c.refuse(RuleJoinCycle,
				"node %q closes fan-out %q and lies on a cycle that does not pass through %q, which opens it",
				n.Name, n.Join, c.b.nodes[f].Name)
		}
	}
}

func (c *checker) graph() *Graph {
	g := &Graph{
		start:    c.b.start,
		nodes:    make([]Node, len(c.b.nodes)),
		index:    c.index,
		edges:    make([]Edge, len(c.b.edges)),
		outgoing: c.outgoing,
		back:     c.back,
		keys:     make([]Key, len(c.b.keys)),
		keyIndex: c.keyIndex,
		reads:    make([]map[string]struct{}, len(c.b.nodes)),
		writes:   make([]map[string]struct{}, len(c.b.nodes)),
	}
	for i, n := range c.b.nodes {
		g.nodes[i] = n.clone()
		g.reads[i] = setOf(n.Reads)
		writes := setOf(n.Writes)
		if n.Halt != nil {
			writes[n.Halt.Into] = struct{}{}
		}
		g.writes[i] = writes
	}
	for i, e := range c.b.edges {
		g.edges[i] = e.clone()
	}
	copy(g.keys, c.b.keys)
	sort.Slice(g.keys, func(i, j int) bool { return g.keys[i].Name < g.keys[j].Name })
	return g
}

func setOf(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

// bfs returns the hop distance from src to every node, or -1 where there is no
// path.
func bfs(adjacent [][]int, src int) []int {
	dist := make([]int, len(adjacent))
	for i := range dist {
		dist[i] = -1
	}
	dist[src] = 0
	queue := []int{src}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adjacent[cur] {
			if dist[next] == -1 {
				dist[next] = dist[cur] + 1
				queue = append(queue, next)
			}
		}
	}
	return dist
}

// bfsFromEdges reports which nodes are reachable from src by at least one
// edge, so a self-loop makes src reach itself.
func bfsFromEdges(adjacent [][]int, src int) []bool {
	seen := make([]bool, len(adjacent))
	queue := append([]int(nil), adjacent[src]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		queue = append(queue, adjacent[cur]...)
	}
	return seen
}

// reachesWithout reports whether dst is reachable from src by at least one
// edge in the graph with the node at index excluded.
func reachesWithout(adjacent [][]int, src, dst, excluded int) bool {
	if src == excluded || dst == excluded {
		return false
	}
	seen := make([]bool, len(adjacent))
	var queue []int
	for _, next := range adjacent[src] {
		if next != excluded {
			queue = append(queue, next)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == dst {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for _, next := range adjacent[cur] {
			if next != excluded {
				queue = append(queue, next)
			}
		}
	}
	return false
}
