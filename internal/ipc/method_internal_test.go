package ipc

import "testing"

// TestTheTableRefusesWhatThisServerCannotServe covers the shape a method
// table may not have, a method declared twice, and that the table this build
// serves, which declares a restricted streaming method, is servable: such a
// stream is decided off the goroutine reading the connection, in
// startAdmittedStream, so the table no longer has to refuse one.
func TestTheTableRefusesWhatThisServerCannotServe(t *testing.T) {
	unservable := map[string][]Spec{
		"a method declared twice": {
			{MethodHealth, KindRequest, AccessOpen, "one"},
			{MethodHealth, KindRequest, AccessRestricted, "and again"},
		},
	}
	for name, rows := range unservable {
		if _, err := indexSpecs(rows); err == nil {
			t.Errorf("a table with %s was accepted", name)
		}
	}
	if _, err := indexSpecs(specs); err != nil {
		t.Errorf("the table this build serves is unservable: %v", err)
	}
}
