package service_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A service that stopped between a read's fetch of the default branch and
// the removal after it left that read's reference in the gate, and the next
// open removes it (P15's restart rule). Each such reference is named for its
// read alone (#552), so nothing later overwrites it, and without the sweep
// they would pile up one per interrupted read. One is planted under every
// namespace a read fetches into, including the name a dispatch used before
// its reads had names of their own, beside a run's trusted reference and
// its submitted one, which stay with the run: the open removes exactly the
// reads' references, and says so in the service's log.
func TestOpenRemovesWhatAnInterruptedReadLeftInTheGate(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	gatePath := gateRepositoryOf(t, h, subject)

	left := []string{
		"refs/assistant/trusted/dispatch/abcdefghijkl-READ1",
		"refs/assistant/trusted/dispatch/abcdefghijkl",
		"refs/assistant/trusted/task/abcdefghijkl-READ2",
		"refs/assistant/trusted/consent/0123456789abcdef",
		"refs/assistant/knowledge/abcdefghijkl/1",
	}
	kept := []string{
		"refs/assistant/trusted/0123456789abcdef",
		"refs/assistant/runs/0123456789abcdef",
	}
	fetch := []string{"fetch", "--quiet", subject}
	for _, ref := range slices.Concat(left, kept) {
		fetch = append(fetch, "+refs/heads/main:"+ref)
	}
	git(t, gatePath, fetch...)
	if got := assistantRefs(t, gatePath); len(got) != len(left)+len(kept) {
		t.Fatalf("the gate holds %v before the open, want every planted reference", got)
	}

	withServiceOptions(t, workerlessOptions(t, h), func(serviceUnderTest) {})

	got := assistantRefs(t, gatePath)
	slices.Sort(kept)
	if !slices.Equal(got, kept) {
		t.Fatalf("after the open the gate holds %v, want exactly the run's references %v", got, kept)
	}
	log := serviceLog(t, h)
	for _, ref := range left {
		if !strings.Contains(log, "recovery removed "+ref+",") {
			t.Errorf("the service's log does not say it removed %s:\n%s", ref, log)
		}
	}
}

// assistantRefs is the sorted names of every reference under
// refs/assistant/ in the repository at path.
func assistantRefs(t *testing.T, path string) []string {
	t.Helper()
	out := strings.TrimSpace(git(t, path, "for-each-ref", "--format=%(refname)", "refs/assistant/"))
	if out == "" {
		return nil
	}
	refs := strings.Split(out, "\n")
	slices.Sort(refs)
	return refs
}
