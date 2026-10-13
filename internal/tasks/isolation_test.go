package tasks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// rawGit runs git directly in dir, for building the primary checkout and the
// counterfeits the assertion is shown refusing. It shells out on the terms
// internal/stages' and internal/vcs's own test helpers do: a copy assembled
// only with the code under test could not show that code wrong. The
// configuration it runs under is the file isolationSubject wrote, which
// turns automatic maintenance off so no git a test started outlives its
// temporary directory (#119).
func rawGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// subject is a home with a gate repository holding a primary checkout's main
// branch, and the task identifier a copy is expected under.
type subject struct {
	home    *home.Home
	primary string
	gate    string
	id      string
	copy    string
}

// isolationSubject builds the subject every assertion test starts from. The
// gate carries the primary's main branch, pushed there the way a person's
// push would put it, so a clone of the gate is the delivery shape's source.
func isolationSubject(t *testing.T) *subject {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Test\n\temail = test@example.invalid\n"+
		"[init]\n\tdefaultBranch = main\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("home.Open: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("home.Create: %v", err)
	}
	primary := filepath.Join(t.TempDir(), "primary")
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, primary, "init", "-q")
	if err := os.WriteFile(filepath.Join(primary, "README"), []byte("subject\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, primary, "add", "README")
	rawGit(t, primary, "commit", "-q", "-m", "first")

	gatePath := filepath.Join(root, "repos", "subject.git")
	if err := os.MkdirAll(filepath.Dir(gatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.InitBare(context.Background(), gatePath); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	rawGit(t, primary, "push", "-q", gatePath, "main:main")

	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	return &subject{home: h, primary: primary, gate: gatePath, id: id, copy: copyPath}
}

// deliveryClone cuts the delivery shape at the expected path: a clone of the
// gate with the assistant remote, on the task's branch.
func (s *subject) deliveryClone(t *testing.T, branch string) {
	t.Helper()
	if _, err := vcs.Clone(context.Background(), s.gate, s.copy, gate.RemoteName); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, s.copy, "switch", "-q", "-c", branch)
}

// investigationClone cuts the investigation shape at the expected path: a
// clone with no remote, detached.
func (s *subject) investigationClone(t *testing.T) {
	t.Helper()
	if _, err := vcs.Clone(context.Background(), s.gate, s.copy, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, s.copy, "switch", "-q", "--detach")
}

// expect is the expectation and second check for the subject's task.
func (s *subject) expect(t *testing.T, shape store.TaskShape, branch string) (Expectation, string) {
	t.Helper()
	e, check, err := Expect(s.home, s.id, shape, s.gate, s.primary, branch)
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	return e, check
}

// observe takes the real observation of dir.
func observe(t *testing.T, dir string) vcs.Observation {
	t.Helper()
	obs, err := vcs.Observe(context.Background(), dir)
	if err != nil {
		t.Fatalf("Observe %s: %v", dir, err)
	}
	return obs
}

// cleanEnv is a launch environment carrying nothing StripRedirecting removes,
// so a refusal in a test using it is never the environment comparison.
func cleanEnv() []string {
	return vcs.StripRedirecting(os.Environ())
}

// refused asserts that err is an *IsolationError naming the comparison.
func refused(t *testing.T, err error, want Comparison) {
	t.Helper()
	var ie *IsolationError
	if !errors.As(err, &ie) {
		t.Fatalf("got %v, want an *IsolationError naming %s", err, want)
	}
	if !errors.Is(err, ErrNotIsolated) {
		t.Errorf("%v does not match ErrNotIsolated", err)
	}
	if ie.Comparison != want {
		t.Errorf("refused on %s (%s), want %s", ie.Comparison, ie.Detail, want)
	}
}

// PRD principle P11 says a worker does not launch unless the resolved path is
// a real isolated copy, distinct from the primary checkout, and that being a
// linked copy is not proof on its own. This test establishes the assertion
// half and only that: AssertIsolated, given a real observation of each shape
// the principle names, refuses it with the comparison that discriminates it,
// and passes a proper clone of either shape. It does not establish that
// nothing launches unproven. The launch edge is #91's seam, which will take
// the Isolation this returns, and until it lands a worker started through
// internal/agents is started where it is told.
//
// Every observation here is taken by vcs.Observe of a repository the test
// built, never stated by hand, so a shape the mechanism cannot produce
// cannot be what the assertion is shown refusing.
func TestAssertIsolatedRefusesEverythingButAnIsolatedCopy(t *testing.T) {
	principles.Cite(t, principles.P11)

	t.Run("a proper delivery clone passes", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		proof, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		if err != nil {
			t.Fatalf("a proper delivery clone was refused: %v", err)
		}
		if !proof.Proven() || proof.Shape != store.TaskDelivery || proof.Branch != "topic" {
			t.Errorf("proof %+v, want proven for delivery on topic", proof)
		}
		if resolved, _ := filepath.EvalSymlinks(s.copy); proof.Copy != resolved {
			t.Errorf("proof names %s, want the resolved copy %s", proof.Copy, resolved)
		}
		if (Isolation{}).Proven() {
			t.Error("a zero Isolation reports proven")
		}
	})

	t.Run("a proper investigation clone passes", func(t *testing.T) {
		s := isolationSubject(t)
		s.investigationClone(t)
		want, _ := s.expect(t, store.TaskInvestigation, "")
		proof, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		if err != nil {
			t.Fatalf("a proper investigation clone was refused: %v", err)
		}
		if !proof.Proven() || proof.Shape != store.TaskInvestigation || proof.Branch != "" {
			t.Errorf("proof %+v, want proven for investigation", proof)
		}
	})

	t.Run("the assistant remote spelled as a file URL with a trailing separator passes", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		rawGit(t, s.copy, "remote", "set-url", gate.RemoteName, "file://"+filepath.ToSlash(s.gate)+"/")
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		if _, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want); err != nil {
			t.Fatalf("a file URL naming the gate was refused: %v", err)
		}
	})

	t.Run("equal to the primary checkout refuses", func(t *testing.T) {
		s := isolationSubject(t)
		want := Expectation{Copy: s.primary, Gate: s.gate, Primary: s.primary, Shape: store.TaskDelivery, Branch: "main"}
		_, err := AssertIsolated(observe(t, s.primary), cleanEnv(), want)
		refused(t, err, ComparisonPrimary)
	})

	t.Run("a clone standing somewhere else than the expected copy refuses", func(t *testing.T) {
		s := isolationSubject(t)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if _, err := vcs.Clone(context.Background(), s.gate, elsewhere, gate.RemoteName); err != nil {
			t.Fatal(err)
		}
		want, _ := s.expect(t, store.TaskDelivery, "main")
		_, err := AssertIsolated(observe(t, elsewhere), cleanEnv(), want)
		refused(t, err, ComparisonToplevel)
	})

	t.Run("a clone inside the primary checkout refuses", func(t *testing.T) {
		s := isolationSubject(t)
		nested := filepath.Join(s.primary, "nested")
		if _, err := vcs.Clone(context.Background(), s.gate, nested, gate.RemoteName); err != nil {
			t.Fatal(err)
		}
		want := Expectation{Copy: nested, Gate: s.gate, Primary: s.primary, Shape: store.TaskDelivery, Branch: "main"}
		_, err := AssertIsolated(observe(t, nested), cleanEnv(), want)
		refused(t, err, ComparisonPrimary)
	})

	t.Run("a copy containing the primary checkout refuses", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		inner := filepath.Join(s.copy, "inner")
		if err := os.Mkdir(inner, 0o700); err != nil {
			t.Fatal(err)
		}
		rawGit(t, inner, "init", "-q")
		want := Expectation{Copy: s.copy, Gate: s.gate, Primary: inner, Shape: store.TaskDelivery, Branch: "topic"}
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonPrimary)
	})

	t.Run("a linked worktree of the primary at the expected path refuses", func(t *testing.T) {
		s := isolationSubject(t)
		rawGit(t, s.primary, "worktree", "add", "-q", "-b", "topic", s.copy)
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		obs := observe(t, s.copy)
		_, err := AssertIsolated(obs, cleanEnv(), want)
		refused(t, err, ComparisonCommonDir)
	})

	t.Run("a linked worktree of the gate at the expected path refuses", func(t *testing.T) {
		s := isolationSubject(t)
		bare, err := vcs.OpenBare(context.Background(), s.gate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bare.AddWorktree(context.Background(), vcs.WorktreeSpec{Path: s.copy, Commit: "main", Branch: "topic"}); err != nil {
			t.Fatalf("AddWorktree: %v", err)
		}
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		_, err = AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonCommonDir)
	})

	t.Run("a symbolic link into the primary at the expected path refuses", func(t *testing.T) {
		s := isolationSubject(t)
		if err := os.Symlink(s.primary, s.copy); err != nil {
			t.Skipf("cannot make a symbolic link here: %v", err)
		}
		want, _ := s.expect(t, store.TaskDelivery, "main")
		// The spelling is the copy's throughout; only identity sees through it.
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonPrimary)
	})

	t.Run("a case variant of the primary refuses on a case-insensitive filesystem", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("a case variant names a different file here; the comparison is by identity, so there is nothing to show")
		}
		s := isolationSubject(t)
		variant := swapCase(s.primary)
		if variant == s.primary {
			t.Skip("the primary's path has no letter to vary")
		}
		want := Expectation{Copy: variant, Gate: s.gate, Primary: s.primary, Shape: store.TaskDelivery, Branch: "main"}
		_, err := AssertIsolated(observe(t, variant), cleanEnv(), want)
		refused(t, err, ComparisonPrimary)
	})

	t.Run("a copy inside the gate repository refuses", func(t *testing.T) {
		s := isolationSubject(t)
		inside := filepath.Join(s.gate, "copy")
		if _, err := vcs.Clone(context.Background(), s.gate, inside, gate.RemoteName); err != nil {
			t.Fatal(err)
		}
		want := Expectation{Copy: inside, Gate: s.gate, Primary: s.primary, Shape: store.TaskDelivery, Branch: "main"}
		_, err := AssertIsolated(observe(t, inside), cleanEnv(), want)
		refused(t, err, ComparisonGate)
	})

	t.Run("assistant pointing at a different home's gate refuses", func(t *testing.T) {
		s := isolationSubject(t)
		other := filepath.Join(t.TempDir(), "other.git")
		if _, err := vcs.InitBare(context.Background(), other); err != nil {
			t.Fatal(err)
		}
		rawGit(t, s.primary, "push", "-q", other, "main:main")
		if _, err := vcs.Clone(context.Background(), other, s.copy, gate.RemoteName); err != nil {
			t.Fatal(err)
		}
		rawGit(t, s.copy, "switch", "-q", "-c", "topic")
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonRemotes)
	})

	t.Run("a second remote refuses", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		rawGit(t, s.copy, "remote", "add", "upstream", s.primary)
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonRemotes)
	})

	t.Run("a push URL on the assistant remote refuses", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		rawGit(t, s.copy, "remote", "set-url", "--push", gate.RemoteName, s.primary)
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonRemotes)
	})

	t.Run("an assistant remote naming a host refuses", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		rawGit(t, s.copy, "remote", "set-url", gate.RemoteName, "https://example.invalid/gate.git")
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonRemotes)
	})

	t.Run("an investigation clone with any remote refuses", func(t *testing.T) {
		s := isolationSubject(t)
		if _, err := vcs.Clone(context.Background(), s.gate, s.copy, gate.RemoteName); err != nil {
			t.Fatal(err)
		}
		rawGit(t, s.copy, "switch", "-q", "--detach")
		want, _ := s.expect(t, store.TaskInvestigation, "")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonRemotes)
	})

	t.Run("a delivery clone on another branch or detached refuses", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "other")
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonHead)
		rawGit(t, s.copy, "switch", "-q", "--detach")
		_, err = AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonHead)
	})

	t.Run("an investigation clone on a branch refuses", func(t *testing.T) {
		s := isolationSubject(t)
		if _, err := vcs.Clone(context.Background(), s.gate, s.copy, ""); err != nil {
			t.Fatal(err)
		}
		want, _ := s.expect(t, store.TaskInvestigation, "")
		_, err := AssertIsolated(observe(t, s.copy), cleanEnv(), want)
		refused(t, err, ComparisonHead)
	})

	t.Run("an environment carrying GIT_DIR refuses before anything is compared", func(t *testing.T) {
		// That the hazard is real, that a git started directly in the copy
		// under such a GIT_DIR answers for the other repository and under
		// vcs.StripRedirecting answers for the copy, is internal/vcs's
		// TestStripRedirectingKeepsGitInTheRepositoryItWasStartedIn, which
		// runs that git. This checks only that the assertion refuses the
		// environment, and that the one list it refuses from is that one.
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		obs := observe(t, s.copy)
		env := append(cleanEnv(), "GIT_DIR="+filepath.Join(s.primary, ".git"))
		_, err := AssertIsolated(obs, env, want)
		refused(t, err, ComparisonEnvironment)
		if _, err := AssertIsolated(obs, vcs.StripRedirecting(env), want); err != nil {
			t.Errorf("the same environment under StripRedirecting was refused: %v", err)
		}
	})

	t.Run("a missing copy or primary refuses rather than passing on an absence", func(t *testing.T) {
		s := isolationSubject(t)
		s.deliveryClone(t, "topic")
		obs := observe(t, s.copy)
		want, _ := s.expect(t, store.TaskDelivery, "topic")
		want.Primary = filepath.Join(t.TempDir(), "gone")
		_, err := AssertIsolated(obs, cleanEnv(), want)
		refused(t, err, ComparisonPrimary)
		want, _ = s.expect(t, store.TaskDelivery, "topic")
		want.Gate = filepath.Join(t.TempDir(), "gone.git")
		_, err = AssertIsolated(obs, cleanEnv(), want)
		refused(t, err, ComparisonGate)
	})
}

// An expectation the assertion cannot be made against is ErrExpectation, not
// a refusal: nothing was compared.
func TestAssertIsolatedRefusesAMalformedExpectation(t *testing.T) {
	s := isolationSubject(t)
	s.deliveryClone(t, "topic")
	obs := observe(t, s.copy)
	good, _ := s.expect(t, store.TaskDelivery, "topic")
	cases := map[string]func(e *Expectation){
		"empty copy":                  func(e *Expectation) { e.Copy = "" },
		"relative gate":               func(e *Expectation) { e.Gate = "repos/x.git" },
		"empty primary":               func(e *Expectation) { e.Primary = "" },
		"unknown shape":               func(e *Expectation) { e.Shape = "review" },
		"delivery without a branch":   func(e *Expectation) { e.Branch = "" },
		"investigation with a branch": func(e *Expectation) { e.Shape = store.TaskInvestigation },
	}
	for name, mutate := range cases {
		e := good
		mutate(&e)
		if _, err := AssertIsolated(obs, cleanEnv(), e); !errors.Is(err, ErrExpectation) {
			t.Errorf("%s: %v, want ErrExpectation", name, err)
		}
	}
	if _, _, err := Expect(s.home, "not an id", store.TaskDelivery, s.gate, s.primary, "topic"); !errors.Is(err, home.ErrTaskID) {
		t.Errorf("Expect with a bad identifier: %v, want ErrTaskID", err)
	}
}

// The second check Expect builds is run with a real shell and a real git in
// the three places PRD principle P11 cares about: it fails in the primary
// checkout, fails in a linked worktree of the primary, and passes in the
// copy, for both shapes. It is detection an obedient worker performs and
// nothing more, which is what Expect's doc says.
func TestSecondCheckCommandDiscriminatesTheCopy(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("the second check is a POSIX shell line and there is no sh on PATH here")
	}
	run := func(t *testing.T, dir, command string) error {
		t.Helper()
		cmd := exec.Command(sh, "-c", command)
		cmd.Dir = dir
		cmd.Env = cleanEnv()
		out, err := cmd.CombinedOutput()
		if err != nil && len(out) > 0 {
			t.Logf("in %s: %s", dir, out)
		}
		return err
	}
	for _, tc := range []struct {
		shape  store.TaskShape
		branch string
		cut    func(*subject, *testing.T)
	}{
		{store.TaskDelivery, "topic", func(s *subject, t *testing.T) { s.deliveryClone(t, "topic") }},
		{store.TaskInvestigation, "", func(s *subject, t *testing.T) { s.investigationClone(t) }},
	} {
		t.Run(string(tc.shape), func(t *testing.T) {
			s := isolationSubject(t)
			tc.cut(s, t)
			want, command := s.expect(t, tc.shape, tc.branch)
			if !strings.Contains(command, want.Copy) {
				t.Errorf("the command %q does not name the expected copy %s", command, want.Copy)
			}
			if err := run(t, s.copy, command); err != nil {
				t.Errorf("the second check failed in the copy: %v", err)
			}
			if err := run(t, s.primary, command); err == nil {
				t.Error("the second check passed in the primary checkout")
			}
			linked := filepath.Join(t.TempDir(), "linked")
			rawGit(t, s.primary, "worktree", "add", "-q", "--detach", linked)
			if err := run(t, linked, command); err == nil {
				t.Error("the second check passed in a linked worktree of the primary")
			}
			// The remote clause is the shape's: a delivery copy with a
			// second remote and an investigation copy with any remote both
			// fail it.
			rawGit(t, s.copy, "remote", "add", "extra", s.primary)
			if err := run(t, s.copy, command); err == nil {
				t.Error("the second check passed in the copy with an extra remote")
			}
		})
	}
}

// swapCase flips the case of the first letter in the last element of path,
// which names the same file on a case-insensitive filesystem and a different
// one elsewhere.
func swapCase(path string) string {
	dir, base := filepath.Split(path)
	runes := []rune(base)
	for i, r := range runes {
		switch {
		case unicode.IsLower(r):
			runes[i] = unicode.ToUpper(r)
		case unicode.IsUpper(r):
			runes[i] = unicode.ToLower(r)
		default:
			continue
		}
		return dir + string(runes)
	}
	return path
}
