//go:build judgelive

package judge_test

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/judge"
)

// The live measurement's options, read after go test's -args. They are
// declared only under the judgelive tag, so the test binary make check builds
// has none of them.
var (
	liveCheck   = flag.String("check", "all", "the check to measure: trace-reason, reviewer-directed-text, or all")
	liveHoldout = flag.String("holdout", "", "a held-out split by payload family for the screen, as train:test, e.g. blunt:subtle")
	liveSubset  = flag.Int("subset", 0, "how many cases to ask again to show call-to-call variation; 0 asks none again")
	liveRepeat  = flag.Int("repeat", 0, "how many more times each subset case is asked")
	liveWorkers = flag.Int("workers", 8, "how many requests are in flight at once")
)

// TestMeasureLive runs the measurement against the provider with the
// operator's key, through the client the product builds: the same
// FromEnvironment, the same endpoint, the same timeout. It is never run by
// make check or CI, which build without the judgelive tag, and it fails
// rather than skips when the key is missing, because a measurement that
// quietly did nothing would read as one that passed.
func TestMeasureLive(t *testing.T) {
	client := judge.FromEnvironment(os.LookupEnv)
	opts := measureOptions{dir: corpusDir, subset: *liveSubset, repeat: *liveRepeat, workers: *liveWorkers}
	switch *liveCheck {
	case "all":
		opts.checks = []judge.Check{judge.CheckTrace, judge.CheckScreen}
	case string(judge.CheckTrace), string(judge.CheckScreen):
		opts.checks = []judge.Check{judge.Check(*liveCheck)}
	default:
		t.Fatalf("-check %q is not trace-reason, reviewer-directed-text, or all", *liveCheck)
	}
	if *liveHoldout != "" {
		train, test, ok := strings.Cut(*liveHoldout, ":")
		if !ok {
			t.Fatalf("-holdout %q is not train:test", *liveHoldout)
		}
		opts.train, opts.test = family(train), family(test)
	}
	start := time.Now()
	if _, err := measure(t.Context(), client, opts, os.Stdout); err != nil {
		t.Fatal(err)
	}
	t.Logf("measured in %s", time.Since(start).Round(time.Second))
}
