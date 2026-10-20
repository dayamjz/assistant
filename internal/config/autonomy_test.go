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

// Every entry is owner/name or gitlab.com/group/project, the list is bounded
// at 64 and a repeat is refused, each at parse time naming the key.
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
		"a gitlab url":     `{"autonomy": {"merge": ["https://gitlab.com/group/project"]}}`,
		"gitlab, no path":  `{"autonomy": {"merge": ["gitlab.com/project"]}}`,
		"gitlab, cased":    `{"autonomy": {"merge": ["GitLab.com/group/project"]}}`,
		"gitlab, dot dot":  `{"autonomy": {"merge": ["gitlab.com/group/../project"]}}`,
		"gitlab, inside":   `{"autonomy": {"merge": ["gitlab.com/group/project/-/merge_requests"]}}`,
		"gitlab, trailing": `{"autonomy": {"merge": ["gitlab.com/group/project/"]}}`,
		"another host":     `{"autonomy": {"merge": ["gitlab.example.com/group/project"]}}`,
		"gitlab repeated":  `{"autonomy": {"merge": ["gitlab.com/group/project", "gitlab.com/Group/Project"]}}`,
	} {
		_, err := Parse(OriginGlobal, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyAutonomyMerge {
			t.Errorf("%s: %v, want a refusal naming autonomy.merge", name, err)
		}
	}
}

// A GitLab project is listed by the specifier internal/forge writes for it,
// gitlab.com/group/project, beside the bare GitHub form, and neither form
// ever matches the other: listing the GitLab project's path as owner/name
// grants no authority over the GitLab project, and listing the GitLab
// project grants none over the GitHub repository with that path. The
// positive controls are each form matching itself, a subgroup path among
// them.
func TestAutonomyMergeNamesAGitLabProjectByItsHost(t *testing.T) {
	const (
		github = "group/project"
		gitlab = "gitlab.com/" + github
		nested = "gitlab.com/group/sub/project"
	)
	doc := `{"autonomy": {"merge": ["` + github + `", "` + gitlab + `", "` + nested + `"]}}`
	c := resolve(t, mustParse(t, OriginGlobal, doc), Absent(OriginTrusted)).Config
	for _, listed := range []string{github, gitlab, nested, "gitlab.com/Group/Project"} {
		if !c.MergesIn(listed) {
			t.Fatalf("MergesIn(%q) over %v = false, want listed", listed, c.AutonomyMerge)
		}
	}
	only := func(entry string) Config {
		return resolve(t, mustParse(t, OriginGlobal, `{"autonomy": {"merge": ["`+entry+`"]}}`),
			Absent(OriginTrusted)).Config
	}
	if c := only(github); c.MergesIn(gitlab) || !c.MergesIn(github) {
		t.Fatalf("a GitHub entry over %v answers the GitLab project %v and itself %v",
			c.AutonomyMerge, c.MergesIn(gitlab), c.MergesIn(github))
	}
	if c := only(gitlab); c.MergesIn(github) || !c.MergesIn(gitlab) {
		t.Fatalf("a GitLab entry over %v answers the GitHub repository %v and itself %v",
			c.AutonomyMerge, c.MergesIn(github), c.MergesIn(gitlab))
	}
}
