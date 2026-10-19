package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// A home that names no ceiling has none: every count is zero, which is no
// ceiling, the window is the default, and Set says nothing is compared. A
// document naming one count sets that count alone and keeps the default
// window, and a ceiling of zero is accepted and stays off.
func TestTheUsageCeilingIsOffUntilTheOperatorSetsACount(t *testing.T) {
	t.Parallel()
	defaults := Defaults().UsageCeiling
	if defaults != (UsageCeiling{Window: DefaultUsageCeilingWindow}) || defaults.Set() {
		t.Fatalf("the default ceiling is %+v (set %v), want no count and the default window", defaults, defaults.Set())
	}
	zero := resolve(t, mustParse(t, OriginGlobal, `{"usage_ceiling": {"input_tokens": 0}}`), Absent(OriginTrusted)).Config.UsageCeiling
	if zero.Set() {
		t.Fatalf("a ceiling of zero reads as set: %+v", zero)
	}
	for _, c := range []struct {
		doc  string
		want UsageCeiling
	}{
		{`{"usage_ceiling": {"input_tokens": 5}}`, UsageCeiling{Window: DefaultUsageCeilingWindow, InputTokens: 5}},
		{`{"usage_ceiling": {"output_tokens": 6}}`, UsageCeiling{Window: DefaultUsageCeilingWindow, OutputTokens: 6}},
		{`{"usage_ceiling": {"cache_read_tokens": 7}}`, UsageCeiling{Window: DefaultUsageCeilingWindow, CacheReadTokens: 7}},
		{`{"usage_ceiling": {"cache_creation_tokens": 8, "window": "90m"}}`,
			UsageCeiling{Window: 90 * time.Minute, CacheCreationTokens: 8}},
	} {
		got := resolve(t, mustParse(t, OriginGlobal, c.doc), Absent(OriginTrusted)).Config.UsageCeiling
		if got != c.want || !got.Set() {
			t.Errorf("%s resolved to %+v (set %v), want %+v set", c.doc, got, got.Set(), c.want)
		}
	}
}

// PRD principle P7 for the usage ceiling: a repository copy, trusted or
// pushed, naming any of its keys is refused when it is parsed, so neither copy
// can raise or lift the operator's ceiling, and a run resolved over a pushed
// copy that sets other keys keeps the ceiling the operator's own file set.
// The refusal names the key and says why.
func TestARepositoryCopyCannotSetTheUsageCeiling(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	docs := map[Key]string{
		KeyUsageCeilingWindow:              `{"usage_ceiling": {"window": "1m"}}`,
		KeyUsageCeilingInputTokens:         `{"usage_ceiling": {"input_tokens": 1000000000}}`,
		KeyUsageCeilingOutputTokens:        `{"usage_ceiling": {"output_tokens": 0}}`,
		KeyUsageCeilingCacheReadTokens:     `{"usage_ceiling": {"cache_read_tokens": 0}}`,
		KeyUsageCeilingCacheCreationTokens: `{"usage_ceiling": {"cache_creation_tokens": 0}}`,
	}
	for _, k := range Keys() {
		if strings.HasPrefix(string(k), "usage_ceiling.") {
			if _, ok := docs[k]; !ok {
				t.Fatalf("%s has no document in this test", k)
			}
		}
	}
	for key, doc := range docs {
		for _, origin := range []Origin{OriginTrusted, OriginPushed} {
			_, err := Parse(origin, []byte(doc))
			var ke *KeyError
			if !errors.As(err, &ke) || ke.Key != key || !strings.Contains(err.Error(), "global-only") {
				t.Errorf("Parse(%v, %s) = %v, want a global-only refusal of %s", origin, doc, err, key)
			}
		}
	}
	global := mustParse(t, OriginGlobal, `{"usage_ceiling": {"input_tokens": 10}}`)
	pushed := mustParse(t, OriginPushed, `{"fix_rounds": {"test": 1}}`)
	got := resolveRun(t, global, Absent(OriginTrusted), pushed).Config.UsageCeiling
	if got != (UsageCeiling{Window: DefaultUsageCeilingWindow, InputTokens: 10}) {
		t.Errorf("a run resolved its ceiling to %+v, want the operator's 10 input tokens", got)
	}
}
