package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// autonomy.merge is one row: global-only and empty by default, so the
// program merges nowhere until the operator lists a repository. A
// repository file carrying it is refused in either copy, so a repository
// can never grant itself merge authority; the operator's file is the
// positive control, which is taken.
func TestAutonomyMergeIsGlobalOnlyAndEmptyByDefault(t *testing.T) {
	principles.Cite(t, principles.P7)
	trust, ok := TrustOf(KeyAutonomyMerge)
	if !ok || trust != TrustGlobal {
		t.Fatalf("TrustOf(autonomy.merge) = %v, %v; PRD section 10 has the key global-only", trust, ok)
	}
	if got := Defaults().AutonomyMerge; len(got) != 0 || Defaults().MergesIn("owner/name") {
		t.Fatalf("the default merge authority is %v; PRD section 10 says empty", got)
	}
	doc := `{"autonomy": {"merge": ["owner/name"]}}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		if _, err := Parse(origin, []byte(doc)); err == nil {
			t.Fatalf("a %s repository file granted itself merge authority", origin)
		}
	}
	c := resolve(t, mustParse(t, OriginGlobal, doc), Absent(OriginTrusted)).Config
	if !c.MergesIn("owner/name") || !c.MergesIn("Owner/Name") || c.MergesIn("owner/other") || c.MergesIn("") {
		t.Fatalf("MergesIn over %v answers wrongly", c.AutonomyMerge)
	}
}

// Every entry is owner/name, the list is bounded at 64 and a repeat is
// refused, each at parse time naming the key.
func TestAutonomyMergeRefusesWhatIsNotARepositoryList(t *testing.T) {
	many := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = `"owner/r` + itoa(i) + `"`
		}
		return `{"autonomy": {"merge": [` + strings.Join(out, ",") + `]}}`
	}
	if _, err := Parse(OriginGlobal, []byte(many(MaxAutonomyMerge))); err != nil {
		t.Fatalf("Parse refused %d entries, which is the limit: %v", MaxAutonomyMerge, err)
	}
	for name, doc := range map[string]string{
		"over the bound":   many(MaxAutonomyMerge + 1),
		"not a list":       `{"autonomy": {"merge": "owner/name"}}`,
		"not a string":     `{"autonomy": {"merge": [7]}}`,
		"no owner":         `{"autonomy": {"merge": ["name"]}}`,
		"a url":            `{"autonomy": {"merge": ["https://github.com/owner/name"]}}`,
		"three segments":   `{"autonomy": {"merge": ["a/b/c"]}}`,
		"empty segment":    `{"autonomy": {"merge": ["owner/"]}}`,
		"an option":        `{"autonomy": {"merge": ["-x/name"]}}`,
		"whitespace":       `{"autonomy": {"merge": ["owner/na me"]}}`,
		"repeated by case": `{"autonomy": {"merge": ["owner/name", "Owner/Name"]}}`,
	} {
		_, err := Parse(OriginGlobal, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyAutonomyMerge {
			t.Errorf("%s: %v, want a refusal naming autonomy.merge", name, err)
		}
	}
}
