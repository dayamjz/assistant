package judge_test

// This file is the loader for the labeled corpus under testdata/corpus, the
// labeled sets behind the cutoffs in check.go. It is test code on purpose:
// nothing in the shipped binary reads the corpus, and its one consumer is the
// measurement in harness_test.go, which measure_test.go runs in make check
// against the stand-in provider and measure_live_test.go runs against the
// provider under a build tag make check never sets. testdata/corpus/README.md
// says where the cases came from and what they do and do not show.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/judge"
)

// corpusDir is where the labeled sets live, relative to this package.
const corpusDir = "testdata/corpus"

// family is the kind of failure a screen positive belongs to. The held-out
// split is by family, because the tuning rule in PRD section 5 holds a change
// out by kind of failure rather than at random.
type family string

const (
	familyBlunt  family = "blunt"
	familySubtle family = "subtle"
)

// families is every family a corpus may name, in report order. A case naming
// any other is a malformed corpus.
var families = []family{familyBlunt, familySubtle}

// labeledCase is one question with its label, as the measurement asks it.
type labeledCase struct {
	id     string
	path   string
	commit string
	diff   string
	// reason is the reason put to the trace check; empty for the screen.
	reason string
	// label is the right answer to the check's question: the reason is a
	// true description of the diff, or the diff carries an injected payload.
	label bool
	// family, template, and base are set on a screen positive only: which
	// payload family and template was injected, and which negative case the
	// payload was injected into.
	family   family
	template int
	base     string
}

// corpus is one check's labeled set.
type corpus struct {
	check     judge.Check
	cases     []labeledCase
	templates map[family][]string
}

// loadCorpus reads the labeled set for check from dir, which holds one file
// per check named after the check.
func loadCorpus(dir string, check judge.Check) (corpus, error) {
	data, err := os.ReadFile(filepath.Join(dir, string(check)+".json"))
	if err != nil {
		return corpus{}, err
	}
	return parseCorpus(check, data)
}

// parseCorpus decodes and validates one check's labeled set. Anything a
// measurement could silently miscount is refused: an unknown field, a case
// without a label, a trace pair that is not one true and one false reason per
// diff, a payload family or template the corpus does not declare.
func parseCorpus(check judge.Check, data []byte) (corpus, error) {
	switch check {
	case judge.CheckTrace:
		return parseTrace(data)
	case judge.CheckScreen:
		return parseScreen(data)
	default:
		return corpus{}, fmt.Errorf("no corpus is defined for check %q", check)
	}
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after the corpus")
	}
	return nil
}

// traceFile is the trace-reason corpus on disk. Diffs come in pairs: diffs
// 2k and 2k+1 are two commits' changes to the same path, and each carries the
// reason written for it. Every diff is asked twice, once with its own reason
// (label true) and once with its pair's (label false).
type traceFile struct {
	Check string `json:"check"`
	Diffs []struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		Commit string `json:"commit"`
		Reason string `json:"reason"`
		Diff   string `json:"diff"`
	} `json:"diffs"`
	Cases []struct {
		ID       string `json:"id"`
		Diff     string `json:"diff"`
		ReasonOf string `json:"reason_of"`
		Label    *bool  `json:"label"`
	} `json:"cases"`
}

func parseTrace(data []byte) (corpus, error) {
	var f traceFile
	if err := decodeStrict(data, &f); err != nil {
		return corpus{}, fmt.Errorf("trace corpus: %w", err)
	}
	if f.Check != string(judge.CheckTrace) {
		return corpus{}, fmt.Errorf("trace corpus names check %q", f.Check)
	}
	if len(f.Diffs) == 0 || len(f.Diffs)%2 != 0 {
		return corpus{}, fmt.Errorf("trace corpus has %d diffs; they come in pairs", len(f.Diffs))
	}
	index := map[string]int{}
	for i, d := range f.Diffs {
		if d.ID == "" || d.Path == "" || d.Commit == "" || strings.TrimSpace(d.Reason) == "" || strings.TrimSpace(d.Diff) == "" {
			return corpus{}, fmt.Errorf("trace diff %d is missing its id, path, commit, reason, or diff", i)
		}
		if _, dup := index[d.ID]; dup {
			return corpus{}, fmt.Errorf("trace diff id %q appears twice", d.ID)
		}
		index[d.ID] = i
	}
	for i := 0; i < len(f.Diffs); i += 2 {
		if f.Diffs[i].Path != f.Diffs[i+1].Path {
			return corpus{}, fmt.Errorf("trace diffs %q and %q are a pair but change different paths", f.Diffs[i].ID, f.Diffs[i+1].ID)
		}
	}
	partner := func(i int) int { return i ^ 1 }

	c := corpus{check: judge.CheckTrace}
	seenCase := map[string]bool{}
	seen := map[string]map[bool]int{}
	for _, tc := range f.Cases {
		if tc.ID == "" || seenCase[tc.ID] {
			return corpus{}, fmt.Errorf("trace case id %q is empty or appears twice", tc.ID)
		}
		seenCase[tc.ID] = true
		if tc.Label == nil {
			return corpus{}, fmt.Errorf("trace case %q has no label", tc.ID)
		}
		di, ok := index[tc.Diff]
		if !ok {
			return corpus{}, fmt.Errorf("trace case %q names unknown diff %q", tc.ID, tc.Diff)
		}
		ri, ok := index[tc.ReasonOf]
		if !ok {
			return corpus{}, fmt.Errorf("trace case %q names unknown reason %q", tc.ID, tc.ReasonOf)
		}
		if ri != di && ri != partner(di) {
			return corpus{}, fmt.Errorf("trace case %q pairs diff %q with the reason of %q, which is not its pair", tc.ID, tc.Diff, tc.ReasonOf)
		}
		if want := ri == di; *tc.Label != want {
			return corpus{}, fmt.Errorf("trace case %q is labeled %v, but a diff with its own reason is true and with its pair's is false", tc.ID, *tc.Label)
		}
		if seen[tc.Diff] == nil {
			seen[tc.Diff] = map[bool]int{}
		}
		seen[tc.Diff][*tc.Label]++
		d := f.Diffs[di]
		c.cases = append(c.cases, labeledCase{
			id: tc.ID, path: d.Path, commit: d.Commit, diff: d.Diff,
			reason: f.Diffs[ri].Reason, label: *tc.Label, template: -1,
		})
	}
	for _, d := range f.Diffs {
		if seen[d.ID][true] != 1 || seen[d.ID][false] != 1 {
			return corpus{}, fmt.Errorf("trace diff %q is asked %d times with its own reason and %d with its pair's; the pairs are unbalanced unless both are 1",
				d.ID, seen[d.ID][true], seen[d.ID][false])
		}
	}
	return c, nil
}

// screenFile is the screen corpus on disk. A negative is a real per-file
// diff. A positive names the negative it was built from, the line index it
// inserts one added line at, the line, and the payload family and template
// the line was rendered from; the loader rebuilds its diff, so a reviewer
// reads exactly the one line that makes it positive.
type screenFile struct {
	Check     string              `json:"check"`
	Templates map[family][]string `json:"templates"`
	Cases     []struct {
		ID       string `json:"id"`
		Label    *bool  `json:"label"`
		Path     string `json:"path"`
		Commit   string `json:"commit"`
		Diff     string `json:"diff"`
		Family   family `json:"family"`
		Template *int   `json:"template"`
		Base     string `json:"base"`
		At       *int   `json:"at"`
		Line     string `json:"line"`
	} `json:"cases"`
}

func parseScreen(data []byte) (corpus, error) {
	var f screenFile
	if err := decodeStrict(data, &f); err != nil {
		return corpus{}, fmt.Errorf("screen corpus: %w", err)
	}
	if f.Check != string(judge.CheckScreen) {
		return corpus{}, fmt.Errorf("screen corpus names check %q", f.Check)
	}
	for fam := range f.Templates {
		if !knownFamily(fam) {
			return corpus{}, fmt.Errorf("screen corpus declares unknown payload family %q", fam)
		}
	}
	for _, fam := range families {
		if len(f.Templates[fam]) == 0 {
			return corpus{}, fmt.Errorf("screen corpus declares no templates for family %q", fam)
		}
		for i, t := range f.Templates[fam] {
			if strings.TrimSpace(t) == "" {
				return corpus{}, fmt.Errorf("screen template %s/%d is empty", fam, i)
			}
		}
	}

	c := corpus{check: judge.CheckScreen, templates: f.Templates}
	negatives := map[string]labeledCase{}
	ids := map[string]bool{}
	for _, sc := range f.Cases {
		if sc.ID == "" || ids[sc.ID] {
			return corpus{}, fmt.Errorf("screen case id %q is empty or appears twice", sc.ID)
		}
		ids[sc.ID] = true
		if sc.Label == nil {
			return corpus{}, fmt.Errorf("screen case %q has no label", sc.ID)
		}
		if *sc.Label {
			continue
		}
		if sc.Family != "" || sc.Template != nil || sc.Base != "" || sc.At != nil || sc.Line != "" {
			return corpus{}, fmt.Errorf("screen case %q is labeled clean but carries a payload", sc.ID)
		}
		if sc.Path == "" || sc.Commit == "" || strings.TrimSpace(sc.Diff) == "" {
			return corpus{}, fmt.Errorf("screen case %q is missing its path, commit, or diff", sc.ID)
		}
		lc := labeledCase{id: sc.ID, path: sc.Path, commit: sc.Commit, diff: sc.Diff, template: -1}
		negatives[sc.ID] = lc
		c.cases = append(c.cases, lc)
	}
	used := map[string]string{}
	usedTemplate := map[family]map[int]bool{}
	for _, sc := range f.Cases {
		if !*sc.Label {
			continue
		}
		if sc.Path != "" || sc.Commit != "" || sc.Diff != "" {
			return corpus{}, fmt.Errorf("screen case %q is a positive and carries its own diff; it names its base instead", sc.ID)
		}
		if !knownFamily(sc.Family) {
			return corpus{}, fmt.Errorf("screen case %q names unknown payload family %q", sc.ID, sc.Family)
		}
		if sc.Template == nil || *sc.Template < 0 || *sc.Template >= len(f.Templates[sc.Family]) {
			return corpus{}, fmt.Errorf("screen case %q names no template, or one family %q does not declare", sc.ID, sc.Family)
		}
		base, ok := negatives[sc.Base]
		if !ok {
			return corpus{}, fmt.Errorf("screen case %q names base %q, which is not a clean case", sc.ID, sc.Base)
		}
		if prev, dup := used[sc.Base]; dup {
			return corpus{}, fmt.Errorf("screen cases %q and %q are both built from %q", prev, sc.ID, sc.Base)
		}
		used[sc.Base] = sc.ID
		if !strings.HasPrefix(sc.Line, "+") || strings.Contains(sc.Line, "\n") {
			return corpus{}, fmt.Errorf("screen case %q injects %q, which is not one added line", sc.ID, sc.Line)
		}
		if tmpl := f.Templates[sc.Family][*sc.Template]; !strings.Contains(sc.Line, tmpl) {
			return corpus{}, fmt.Errorf("screen case %q injects a line that does not carry template %s/%d", sc.ID, sc.Family, *sc.Template)
		}
		lines := strings.Split(base.diff, "\n")
		if sc.At == nil || *sc.At < 1 || *sc.At > len(lines) {
			return corpus{}, fmt.Errorf("screen case %q inserts at a line its base does not have", sc.ID)
		}
		at := *sc.At
		diff := strings.Join(append(append(append([]string{}, lines[:at]...), sc.Line), lines[at:]...), "\n")
		if usedTemplate[sc.Family] == nil {
			usedTemplate[sc.Family] = map[int]bool{}
		}
		usedTemplate[sc.Family][*sc.Template] = true
		c.cases = append(c.cases, labeledCase{
			id: sc.ID, path: base.path, commit: base.commit, diff: diff, label: true,
			family: sc.Family, template: *sc.Template, base: sc.Base,
		})
	}
	for _, fam := range families {
		for i := range f.Templates[fam] {
			if !usedTemplate[fam][i] {
				return corpus{}, fmt.Errorf("screen template %s/%d has no case", fam, i)
			}
		}
	}
	return c, nil
}

func knownFamily(f family) bool {
	for _, k := range families {
		if f == k {
			return true
		}
	}
	return false
}
