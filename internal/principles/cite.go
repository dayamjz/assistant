package principles

import "testing"

// Cite records that the test calling it claims to check the given principles.
//
// It is the whole citation vocabulary, and the call is the citation: Check
// finds it by parsing the test source for a call to this function inside the
// body of a test function, so a citation exists exactly where a reader sees
// one. Call it from the test itself rather than from a helper the test calls,
// because the unit a citation names is the test a reader can run.
//
//	func TestOrdinaryPushToOriginIsUnaffected(t *testing.T) {
//		principles.Cite(t, principles.P1)
//		...
//	}
//
// What the call does when it runs is small and deliberate. It refuses a value
// this package has no constant for, so a citation cannot name a principle
// that does not exist, and it writes the claim into the test's log, where it
// appears beside the failure of a test that claimed it. Neither of those is
// evidence that the test checks the principle. Nothing here can be: a
// citation is a claim a test makes about itself, and this package checks that
// the claim was made and by whom, never that it is true.
func Cite(tb testing.TB, p Principle, more ...Principle) {
	tb.Helper()
	for _, q := range append([]Principle{p}, more...) {
		if !q.known() {
			tb.Fatalf("principles.Cite: %s is not a principle this package has a constant for", q)
		}
		tb.Logf("claims PRD principle %s", q)
	}
}
