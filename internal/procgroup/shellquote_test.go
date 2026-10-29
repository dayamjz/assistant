package procgroup_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/procgroup"
)

func TestShellQuotePreservesBytesForPOSIXShell(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"plain", "'plain'"},
		{"it's", `'it'\''s'`},
		{"$VAR", "'$VAR'"},
		{"$(cmd)", "'$(cmd)'"},
		{"`tick`", "'`tick`'"},
		{"a\nb", "'a\nb'"},
		{"-leading", "'-leading'"},
	}
	for _, c := range cases {
		if got := procgroup.ShellQuote(c.in); got != c.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
