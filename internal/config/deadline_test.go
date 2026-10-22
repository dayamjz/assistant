package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// invocationDeadline is one of the two keys that bound a program the service
// runs, git_deadline and gh_deadline, with the range PRD section 10's limits
// table gives it and the field it resolves into.
type invocationDeadline struct {
	key            Key
	def            time.Duration
	floor, ceiling time.Duration
	field          func(Config) time.Duration
}

var invocationDeadlines = []invocationDeadline{
	{KeyGitDeadline, 10 * time.Minute, time.Minute, 2 * time.Hour, func(c Config) time.Duration { return c.GitDeadline }},
	{KeyGHDeadline, 2 * time.Minute, 10 * time.Second, 30 * time.Minute, func(c Config) time.Duration { return c.GHDeadline }},
}

// document is a JSON document setting k to the duration text value.
func (d invocationDeadline) document(value string) string {
	return `{"` + string(d.key) + `": "` + value + `"}`
}

// PRD principle P7 for git_deadline and gh_deadline: each bounds a program on
// the operator's machine, so a repository copy, trusted or pushed, in either
// format, naming one is refused when it is parsed, naming the key and saying
// it is global-only, rather than dropped. A value inside the range is refused
// too, so the refusal is the key's class and not its value.
func TestARepositoryCopyCannotNameAnInvocationDeadline(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	for _, d := range invocationDeadlines {
		parsers := map[string]func(Origin, []byte) (Layer, error){"JSON": Parse, "YAML": ParseYAML}
		for format, parse := range parsers {
			for _, origin := range []Origin{OriginTrusted, OriginPushed} {
				_, err := parse(origin, []byte(d.document(d.def.String())))
				var ke *KeyError
				if !errors.As(err, &ke) || ke.Key != d.key || !strings.Contains(err.Error(), "global-only") {
					t.Errorf("%s %v copy naming %s = %v, want a global-only refusal of the key", format, origin, d.key, err)
				}
			}
		}
	}
}

// The operator's own file sets each deadline anywhere in its range, floor and
// ceiling included, and a run resolved over repository copies that set other
// keys keeps the operator's value. A file that sets neither resolves to PRD
// section 10's defaults.
func TestTheOperatorsFileSetsTheInvocationDeadlines(t *testing.T) {
	t.Parallel()
	for _, d := range invocationDeadlines {
		if got := d.field(Defaults()); got != d.def {
			t.Errorf("the default %s is %v, want %v", d.key, got, d.def)
		}
		for _, want := range []time.Duration{d.floor, d.ceiling} {
			global := mustParse(t, OriginGlobal, d.document(want.String()))
			trusted := mustParse(t, OriginTrusted, `{"run_budget": 10}`)
			pushed := mustParse(t, OriginPushed, `{"fix_rounds": {"test": 1}}`)
			if got := d.field(resolveRun(t, global, trusted, pushed).Config); got != want {
				t.Errorf("a run resolved %s to %v, want the operator's %v", d.key, got, want)
			}
		}
	}
	yaml, err := ParseYAML(OriginGlobal, []byte("git_deadline: 45m\ngh_deadline: 90s\n"))
	if err != nil {
		t.Fatalf("ParseYAML of the operator's file: %v", err)
	}
	res, err := Resolve(yaml, Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Config.GitDeadline != 45*time.Minute || res.Config.GHDeadline != 90*time.Second {
		t.Errorf("the operator's YAML file resolved to git %v and gh %v, want 45m and 90s",
			res.Config.GitDeadline, res.Config.GHDeadline)
	}
}

// A deadline outside its range, or one that is not a duration, is refused
// when the operator's file is loaded, naming the key and the value, rather
// than clamped to the nearest bound or replaced by the default: a bound the
// operator did not get is one they should see refused.
func TestAnInvocationDeadlineOutsideItsRangeIsRefusedAtLoad(t *testing.T) {
	t.Parallel()
	for _, d := range invocationDeadlines {
		cases := []struct {
			value, detail string
		}{
			{(d.floor - time.Second).String(), "at least " + d.floor.String()},
			{(d.ceiling + time.Second).String(), "at most " + d.ceiling.String()},
			{"0s", "at least " + d.floor.String()},
			{"-1m", "at least " + d.floor.String()},
			{"soon", "not a duration"},
		}
		for _, c := range cases {
			_, err := Parse(OriginGlobal, []byte(d.document(c.value)))
			var ke *KeyError
			if !errors.As(err, &ke) || ke.Key != d.key || ke.Value != `"`+c.value+`"` || !strings.Contains(ke.Detail, c.detail) {
				t.Errorf("%s = %q in the operator's file: %v, want a refusal naming the key, the value and %q",
					d.key, c.value, err, c.detail)
			}
		}
		_, err := Parse(OriginGlobal, []byte(`{"`+string(d.key)+`": 600}`))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != d.key {
			t.Errorf("%s as a bare number = %v, want a refusal naming the key", d.key, err)
		}
	}
}
