package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SkippedAgent is one entry an agent selection passed over before it chose,
// as recorded beside the selection: the run's resolution on the run row and a
// worker's route on its attempt, both written once by the accessor that
// records the selection itself.
//
// The vocabulary is internal/agents', on AgentInvocation's terms: Reason is
// one of that package's skip reason codes and Capability one of its
// capability names, and this package stores what it was handed without
// decoding either. Only the adapter name is kept and never the entry's
// arguments, so a flag an operator wrote into an entry does not reach the
// record.
type SkippedAgent struct {
	// Agent is the adapter name the entry began with. It is empty for an
	// entry that named none, which is the one entry that has no name.
	Agent string `json:"agent"`
	// Reason is the stable code for why the entry was passed over.
	Reason string `json:"reason"`
	// Capability is the capability the adapter did not declare, for the one
	// reason that is about a capability, and empty otherwise.
	Capability string `json:"capability,omitempty"`
}

// skippedText encodes the list for its column. A nil list is recorded as
// the empty list, because a selection always knows what it passed over and
// passing over nothing is a fact rather than an unknown. An entry with no
// reason is refused: a skip whose cause was not stated is the record
// fabricating one.
func skippedText(column string, skipped []SkippedAgent) (string, error) {
	if skipped == nil {
		skipped = []SkippedAgent{}
	}
	for i, s := range skipped {
		if strings.TrimSpace(s.Reason) == "" {
			return "", fmt.Errorf("store: %s entry %d (%q) names no reason", column, i, s.Agent)
		}
	}
	encoded, err := json.Marshal(skipped)
	if err != nil {
		return "", fmt.Errorf("store: encoding %s: %w", column, err)
	}
	return string(encoded), nil
}

// skippedValue decodes the column. Text that is present but not the stored
// encoding is an error rather than an unknown, on rejectionsValue's terms.
func skippedValue(column string, o Optional[string]) (Optional[[]SkippedAgent], error) {
	text, ok := o.Get()
	if !ok {
		return Unknown[[]SkippedAgent](), nil
	}
	var list []SkippedAgent
	if err := json.Unmarshal([]byte(text), &list); err != nil || list == nil {
		if err == nil {
			err = fmt.Errorf("not a list: %q", text)
		}
		return Unknown[[]SkippedAgent](), fmt.Errorf("store: column %s: %w", column, err)
	}
	return Known(list), nil
}
