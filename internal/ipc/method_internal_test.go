package ipc

import "testing"

// TestTheTableRefusesWhatThisServerCannotServe covers the two shapes a method
// table may not have. The second is the one a reader is likely to think
// arbitrary: a restricted streaming method would have to be authorized on the
// goroutine that reads the connection, which is where this server refuses to
// run a caller's Ancestry.
func TestTheTableRefusesWhatThisServerCannotServe(t *testing.T) {
	unservable := map[string][]Spec{
		"a method declared twice": {
			{MethodHealth, KindRequest, AccessOpen, "one"},
			{MethodHealth, KindRequest, AccessRestricted, "and again"},
		},
		"a restricted streaming method": {
			{MethodEventsSubscribe, KindStream, AccessRestricted, "a stream a contained caller may not open"},
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
