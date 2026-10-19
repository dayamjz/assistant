package config

import (
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// reduced_path is one row: trusted-only and off by default, so the nine-stage
// gate stays what a run does until a default branch names a reduced path.
func TestReducedPathIsATrustedOnlyRowDefaultingToOff(t *testing.T) {
	trust, ok := TrustOf(KeyReducedPath)
	if !ok || trust != TrustTrusted {
		t.Fatalf("TrustOf(reduced_path) = %v, %v; it must be one a pushed branch cannot set", trust, ok)
	}
	if Defaults().ReducedPath != ReducedPathOff {
		t.Fatalf("default reduced path = %q, want off", Defaults().ReducedPath)
	}
	mode, ok := Defaults().ReducedPath.Mode()
	if !ok || mode.Validation != ValidationNineStage || mode.Path.Named() {
		t.Fatalf("the default mode is %+v, %v; want the nine-stage row", mode, ok)
	}
	for _, key := range Keys() {
		if key == KeyReducedPath {
			return
		}
	}
	t.Fatal("reduced_path is not in Keys()")
}

// Each mode states the validation it requires, what it may publish, and who
// may authorize a merge. The statements are the table, and decode accepts
// exactly those paths, so a value outside it fails at parse time. "direct"
// stays refused: it is the deferred delivery mode, not a value of this key.
func TestEachReducedPathStatesValidationPublicationAndMerge(t *testing.T) {
	modes := DeliveryModes()
	if len(modes) == 0 {
		t.Fatal("the mode table is empty, so nothing is held to anything")
	}
	publishers := 0
	for _, mode := range modes {
		if mode.Path == "" || mode.Validation == "" || mode.Publication == "" || mode.Merge == "" {
			t.Errorf("mode %q leaves a statement empty: %+v", mode.Path, mode)
		}
		if mode.Merge != MergeByAutonomy {
			t.Errorf("mode %q names merge authority %q, want %q", mode.Path, mode.Merge, MergeByAutonomy)
		}
		got, ok := mode.Path.Mode()
		if !ok || got != mode {
			t.Errorf("Mode(%q) = %+v, %v; want the table row", mode.Path, got, ok)
		}
		parsed := mustParse(t, OriginTrusted, `{"reduced_path": "`+string(mode.Path)+`"}`)
		if resolve(t, Absent(OriginGlobal), parsed).Config.ReducedPath != mode.Path {
			t.Errorf("the trusted copy named %q and the resolution does not", mode.Path)
		}
		if mode.MayPublish() {
			publishers++
			if mode.Path != ReducedPathPublish || mode.Publication != PublicationPullRequest {
				t.Errorf("a mode that may publish is %+v, want publish", mode)
			}
		}
	}
	if publishers != 1 {
		t.Fatalf("%d modes may publish, want only publish", publishers)
	}
	for _, value := range []string{"direct", "fast", ""} {
		_, err := Parse(OriginTrusted, []byte(`{"reduced_path": "`+value+`"}`))
		if err == nil {
			t.Errorf("reduced_path %q was accepted", value)
		}
	}
}

// A pushed branch cannot name a reduced path for itself. The pushed key is
// dropped and reported, and a trusted record stays record when the branch
// asks to publish.
func TestAPushedBranchCannotNameItsReducedPath(t *testing.T) {
	principles.Cite(t, principles.P7)
	pushed := mustParse(t, OriginPushed, `{"reduced_path": "publish"}`)
	res, err := ResolveRun(Absent(OriginGlobal), Absent(OriginTrusted), pushed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.ReducedPath != ReducedPathOff {
		t.Fatalf("a pushed copy named %q for its own run", res.Config.ReducedPath)
	}
	rejected := false
	for _, r := range res.Rejected {
		if r.Key == KeyReducedPath && r.Origin == OriginPushed {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the pushed key was dropped without a rejection: %v", res.Rejected)
	}
	trusted := mustParse(t, OriginTrusted, `{"reduced_path": "record"}`)
	res, err = ResolveRun(Absent(OriginGlobal), trusted, pushed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.ReducedPath != ReducedPathRecord {
		t.Fatalf("the trusted copy's record was resolved as %q", res.Config.ReducedPath)
	}
	mode, ok := res.Config.ReducedPath.Mode()
	if !ok || mode.MayPublish() {
		t.Fatalf("a pushed publish turned a trusted record into %+v", mode)
	}
}
