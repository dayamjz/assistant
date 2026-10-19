package knowledge

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/store"
)

// DefaultBudget is the generated-section budget internal/service passes.
// The trusted document is not counted against it.
const DefaultBudget = 4096

// Pin kinds. A pin is a reference the caller is keeping, not a knowledge row.
const (
	// PinHold is a reference to an open hold.
	PinHold = "hold"
	// PinTask is a reference to an unfinished task.
	PinTask = "task"
)

// ErrRepositoryOverrides is returned by Compose when the repository file
// would occupy the trusted document's place. Nothing is composed.
var ErrRepositoryOverrides = errors.New("knowledge: a repository file cannot replace the trusted operator document")

// ErrPin is returned by Compose when a pin has no kind, no identifier, or
// no text, or a kind outside the two this package names.
var ErrPin = errors.New("knowledge: a pinned reference names its kind, its identifier, and its text")

// ErrBudget is returned by Compose when the budget is negative.
var ErrBudget = errors.New("knowledge: the prompt budget is negative")

// Pin is a reference Compose must keep. The caller decides which holds are
// open and which tasks are unfinished and passes them here.
type Pin struct {
	// Kind is PinHold or PinTask.
	Kind string
	// ID is the hold key or the task identifier.
	ID string
	// Text is what the prompt says about it.
	Text string
}

// Material is what Compose is given. Knowledge is the whole history of one
// project, oldest first, not its last row.
type Material struct {
	// Trusted is the operator document, byte for byte. It stays the prefix
	// of the result. Empty means the document is absent, which is not filled
	// from RepositoryFile.
	Trusted string
	// RepositoryFile is a file from the project checkout. It is not copied
	// into the prompt.
	RepositoryFile string
	// Knowledge is the project's knowledge history.
	Knowledge []store.Knowledge
	// Pinned is kept whole.
	Pinned []Pin
	// Budget is the most bytes the generated section may carry. Zero means
	// nothing but the pins fits. Negative is ErrBudget. The trusted document
	// is not counted.
	Budget int
}

// Context is the prompt block Compose produced.
type Context struct {
	// Text is the trusted document followed by the generated section. When
	// Trusted was non-empty, it is a prefix of Text.
	Text string
	// Dropped is the identifiers of rules that did not fit, oldest first.
	Dropped []int64
}

// Rules returns the rows that may be injected as rules, in the order rows
// were passed. An operator row is a rule. A task row is a rule only when an
// operator row adopts its identifier. Any other task row is left out,
// including the newest.
func Rules(rows []store.Knowledge) []store.Knowledge {
	adopted := map[int64]struct{}{}
	for _, row := range rows {
		if row.Author != store.AuthoredByOperator() {
			continue
		}
		id, ok := row.Adopts.Get()
		if ok {
			adopted[id] = struct{}{}
		}
	}
	var out []store.Knowledge
	for _, row := range rows {
		switch row.Author {
		case store.AuthoredByOperator():
			out = append(out, row)
		case store.AuthoredByTask():
			if _, ok := adopted[row.ID]; ok {
				out = append(out, row)
			}
		}
	}
	return out
}

// Compose builds the prompt block. The trusted document stays the prefix.
// Generated text is appended after it. The repository file is not written
// into either part, and it is refused when it is the trusted document.
func Compose(m Material) (Context, error) {
	if m.Budget < 0 {
		return Context{}, ErrBudget
	}
	repo := m.RepositoryFile
	if strings.TrimSpace(repo) != "" && m.Trusted == repo {
		return Context{}, ErrRepositoryOverrides
	}
	for _, pin := range m.Pinned {
		if pin.Kind != PinHold && pin.Kind != PinTask {
			return Context{}, fmt.Errorf("%w: kind %q", ErrPin, pin.Kind)
		}
		if strings.TrimSpace(pin.ID) == "" || strings.TrimSpace(pin.Text) == "" {
			return Context{}, ErrPin
		}
	}
	rules := Rules(m.Knowledge)
	kept, dropped := fit(m.Pinned, rules, m.Budget)
	generated := renderGenerated(m.Pinned, kept)
	text := m.Trusted
	if generated != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += generated
	}
	if m.Trusted != "" && !strings.HasPrefix(text, m.Trusted) {
		return Context{}, ErrRepositoryOverrides
	}
	if strings.TrimSpace(repo) != "" && strings.Contains(text, repo) && !strings.Contains(m.Trusted, repo) && !bodiesContain(kept, repo) {
		return Context{}, ErrRepositoryOverrides
	}
	return Context{Text: text, Dropped: dropped}, nil
}

func fit(pins []Pin, rules []store.Knowledge, budget int) (kept []store.Knowledge, dropped []int64) {
	start := 0
	for start < len(rules) && len(renderGenerated(pins, rules[start:])) > budget {
		start++
	}
	for _, row := range rules[:start] {
		dropped = append(dropped, row.ID)
	}
	return rules[start:], dropped
}

func bodiesContain(rules []store.Knowledge, needle string) bool {
	for _, row := range rules {
		if strings.Contains(row.Body, needle) {
			return true
		}
	}
	return false
}

func renderGenerated(pins []Pin, rules []store.Knowledge) string {
	pinsText := renderPins(pins)
	rulesText := renderRules(rules)
	switch {
	case pinsText == "" && rulesText == "":
		return ""
	case pinsText == "":
		return rulesText
	case rulesText == "":
		return pinsText
	default:
		return pinsText + "\n" + rulesText
	}
}

func renderPins(pins []Pin) string {
	var b strings.Builder
	for _, pin := range pins {
		fmt.Fprintf(&b, "%s %s\n%s\n", pin.Kind, pin.ID, pin.Text)
	}
	return b.String()
}

func renderRules(rules []store.Knowledge) string {
	var b strings.Builder
	for _, row := range rules {
		fmt.Fprintf(&b, "rule %d\n%s\n", row.ID, row.Body)
	}
	return b.String()
}
