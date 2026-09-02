package graph

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Merge is the rule that combines an existing state value with an incoming
// write. A key with MergeNone declares no rule and may therefore be written by
// exactly one node; any key more than one node can write must declare a rule,
// because a shared key with no declared rule produces results that depend on
// scheduling.
type Merge uint8

const (
	// MergeNone declares no merge rule. Exactly one node may write the key.
	MergeNone Merge = iota
	// MergeLastWriteWins replaces the existing value with the incoming one.
	MergeLastWriteWins
	// MergeSum adds the incoming value to the existing one. KindInt only.
	MergeSum
	// MergeAppend appends the incoming items to the existing ones. KindList only.
	MergeAppend
)

// String returns the name of the merge rule as it appears in errors.
func (m Merge) String() string {
	switch m {
	case MergeNone:
		return "none"
	case MergeLastWriteWins:
		return "last-write-wins"
	case MergeSum:
		return "sum"
	case MergeAppend:
		return "append"
	default:
		return "merge(unknown)"
	}
}

// compatible reports whether the rule can combine values of kind k.
func (m Merge) compatible(k Kind) bool {
	switch m {
	case MergeNone, MergeLastWriteWins:
		return true
	case MergeSum:
		return k == KindInt
	case MergeAppend:
		return k == KindList
	default:
		return false
	}
}

// apply combines old and incoming under the rule. Callers guarantee both
// values have the key's declared kind.
func (m Merge) apply(old, incoming Value) Value {
	switch m {
	case MergeSum:
		return IntValue(old.num + incoming.num)
	case MergeAppend:
		merged := make([]string, 0, len(old.list)+len(incoming.list))
		merged = append(merged, old.list...)
		merged = append(merged, incoming.list...)
		return Value{kind: KindList, list: merged}
	case MergeNone, MergeLastWriteWins:
		return incoming
	default:
		return incoming
	}
}

// Key declares one state key: its name, the kind of value it holds, and how
// writes to it combine. The declaration is checked when the graph is built,
// so a node cannot read or write a key this list does not contain.
type Key struct {
	// Name is the key, unique within a graph.
	Name string
	// Kind is the type every value stored under Name must have.
	Kind Kind
	// Merge is the rule combining an existing value with an incoming write.
	// MergeNone means no rule is declared, which limits the key to one writer.
	Merge Merge
}

// State is the typed record a run advances. It always holds every key the
// graph declares: a key no node has written yet holds the zero value of its
// declared kind, so a read of a declared key is always defined.
//
// A State is a value: Clone, and every accessor that returns a composite,
// copy rather than alias, so handing one to a node cannot mutate a run.
type State struct {
	values map[string]Value
}

// Get returns the value stored under key. The second result is false when the
// graph does not declare the key.
func (s State) Get(key string) (Value, bool) {
	v, ok := s.values[key]
	return v, ok
}

// Keys returns the declared keys this state holds, sorted.
func (s State) Keys() []string {
	out := make([]string, 0, len(s.values))
	for k := range s.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Clone returns an independent copy of the state.
func (s State) Clone() State {
	out := State{values: make(map[string]Value, len(s.values))}
	for k, v := range s.values {
		if v.kind == KindList {
			v = ListValue(v.list...)
		}
		out.values[k] = v
	}
	return out
}

// Equal reports whether two states hold the same keys with the same values.
func (s State) Equal(other State) bool {
	if len(s.values) != len(other.values) {
		return false
	}
	for k, v := range s.values {
		o, ok := other.values[k]
		if !ok || !v.Equal(o) {
			return false
		}
	}
	return true
}

// set stores v under key without consulting any declaration. Callers in this
// package check the declaration first.
func (s State) set(key string, v Value) {
	s.values[key] = v
}

// MarshalJSON writes the state as a plain object of tagged values. Map keys
// are emitted in sorted order, which is what makes fingerprint deterministic.
func (s State) MarshalJSON() ([]byte, error) {
	if s.values == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(s.values)
}

// UnmarshalJSON reads a plain object of tagged values. Whether those keys are
// the ones a particular graph declares is checked separately by Graph.Validate.
func (s *State) UnmarshalJSON(b []byte) error {
	var values map[string]Value
	if err := json.Unmarshal(b, &values); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	if values == nil {
		values = map[string]Value{}
	}
	s.values = values
	return nil
}

// fingerprint returns a canonical encoding of the state. Two states with the
// same fingerprint are equal, which is how the convergence check decides that
// a round changed nothing.
func (s State) fingerprint() string {
	b, err := s.MarshalJSON()
	if err != nil {
		// Every value in a live state carries a declared kind, so encoding
		// cannot fail here. Returning a value that never compares equal keeps
		// a corrupted state from being mistaken for a converged one.
		return "unencodable:" + err.Error()
	}
	return string(b)
}

// Reader gives a node read access to exactly the state keys it declared. A read
// of any other key is refused, which is what makes the declaration load-bearing
// rather than documentation.
type Reader interface {
	// Get returns the current value of key, including anything this node has
	// already written to it during this execution. It returns an error
	// wrapping ErrUndeclaredRead when the node did not declare reading it.
	Get(key string) (Value, error)
}

// Writer gives a node write access to exactly the state keys it declared. A
// write of any other key, or of a value whose kind does not match the key's
// declaration, is refused.
type Writer interface {
	// Set stores v under key, combining it with the current value under the
	// key's declared merge rule. It returns an error wrapping
	// ErrUndeclaredWrite or ErrKindMismatch when the write is not permitted.
	Set(key string, v Value) error
}

// access is the Reader and Writer handed to one node execution. It records the
// first refusal so the executor fails the step even if the node ignores the
// error it was handed.
type access struct {
	keys   map[string]Key
	reads  map[string]struct{}
	writes map[string]struct{}
	node   string
	work   State
	err    error
}

func (a *access) fail(err error) error {
	if a.err == nil {
		a.err = err
	}
	return err
}

// Get implements Reader.
func (a *access) Get(key string) (Value, error) {
	if _, ok := a.reads[key]; !ok {
		return Value{}, a.fail(fmt.Errorf("%w: node %q, key %q", ErrUndeclaredRead, a.node, key))
	}
	v, ok := a.work.Get(key)
	if !ok {
		// Unreachable for a built graph: every declared read is a declared key
		// and every declared key is present in state.
		return Value{}, a.fail(fmt.Errorf("%w: node %q, key %q", ErrUndeclaredRead, a.node, key))
	}
	return v, nil
}

// Set implements Writer.
func (a *access) Set(key string, v Value) error {
	if _, ok := a.writes[key]; !ok {
		return a.fail(fmt.Errorf("%w: node %q, key %q", ErrUndeclaredWrite, a.node, key))
	}
	spec := a.keys[key]
	if v.Kind() != spec.Kind {
		return a.fail(fmt.Errorf("%w: node %q, key %q declares %s, got %s",
			ErrKindMismatch, a.node, key, spec.Kind, v.Kind()))
	}
	old, _ := a.work.Get(key)
	a.work.set(key, spec.Merge.apply(old, v))
	return nil
}
