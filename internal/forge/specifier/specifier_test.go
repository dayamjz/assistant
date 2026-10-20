package specifier

import (
	"strings"
	"testing"
)

// The two forms accept what they say and refuse the rest, and each table
// carries both, so a grammar that accepted everything or nothing fails it.
func TestEachFormAcceptsItsOwnShapeOnly(t *testing.T) {
	for spec, want := range map[string]bool{
		"owner/name":            true,
		"Owner.Org/name_2-x":    true,
		"owner":                 false,
		"owner/":                false,
		"/name":                 false,
		"a/b/c":                 false,
		"-x/name":               false,
		"owner/na me":           false,
		"https://x/owner/name":  false,
		"gitlab.com/group/name": false,
	} {
		if got := GitHub(spec); got != want {
			t.Errorf("GitHub(%q) = %v, want %v", spec, got, want)
		}
	}
	for spec, want := range map[string]string{
		"gitlab.com/group/name":                            "group/name",
		"gitlab.com/group/sub/name":                        "group/sub/name",
		"gitlab.com/name":                                  "",
		"GitLab.com/group/name":                            "",
		"gitlab.com/group/../name":                         "",
		"gitlab.com/group/-/merge":                         "",
		"gitlab.com/group//name":                           "",
		"github.com/group/name":                            "",
		"group/name":                                       "",
		"https://gitlab.com/group/name":                    "",
		"gitlab.com/group/name/":                           "",
		"gitlab.com/group/na me":                           "",
		"gitlab.com/" + strings.Repeat("a", 101) + "/name": "",
	} {
		path, ok := GitLab(spec)
		if ok != (want != "") || path != want {
			t.Errorf("GitLab(%q) = %q, %v, want %q", spec, path, ok, want)
		}
	}
}

// No string is both a GitHub and a GitLab specifier, and no GitHub specifier
// equals a GitLab one with letter case ignored, which is how
// internal/config's MergesIn compares an autonomy.merge entry with a run's
// specifier. The seeds include the GitLab specifier whose path is a GitHub
// specifier, the case a host-less GitLab specifier would have confused.
func FuzzNoGitHubSpecifierIsAGitLabOne(f *testing.F) {
	for _, seed := range []string{
		"group/name", "gitlab.com/group/name", "gitlab.com/name", "GITLAB.COM/group/name",
		"gitlab.com/a/b/c", "a/b", "",
	} {
		f.Add(seed, "gitlab.com/"+seed)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		for _, s := range []string{a, b} {
			if _, lab := GitLab(s); lab && GitHub(s) {
				t.Fatalf("%q is both a GitHub and a GitLab specifier", s)
			}
		}
		_, labB := GitLab(b)
		if GitHub(a) && labB && strings.EqualFold(a, b) {
			t.Fatalf("GitHub specifier %q equals GitLab specifier %q with case ignored", a, b)
		}
	})
}
