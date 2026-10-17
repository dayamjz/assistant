package runs

import "testing"

func TestFixerResumeKeepsASessionOnlyForTheAgentThatHoldsIt(t *testing.T) {
	cases := []struct {
		name                                     string
		session, sessionAgent, resolved, current string
		hasSession, hasAgent, hasResolved        bool
		want                                     string
	}{
		{name: "same agent", session: "sess", sessionAgent: "scripted", resolved: "scripted", current: "scripted", hasSession: true, hasAgent: true, hasResolved: true, want: "sess"},
		{name: "other agent", session: "sess", sessionAgent: "claude", resolved: "scripted", current: "scripted", hasSession: true, hasAgent: true, hasResolved: true, want: ""},
		{name: "unknown agent, resolved matches", session: "sess", resolved: "scripted", current: "scripted", hasSession: true, hasResolved: true, want: "sess"},
		{name: "unknown agent, resolved differs", session: "sess", resolved: "scripted", current: "claude", hasSession: true, hasResolved: true, want: ""},
		{name: "unknown agent and resolved", session: "sess", current: "scripted", hasSession: true, want: "sess"},
		{name: "no session", current: "scripted", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fixerResume(tc.session, tc.sessionAgent, tc.resolved, tc.current, tc.hasSession, tc.hasAgent, tc.hasResolved)
			if got != tc.want {
				t.Fatalf("fixerResume = %q, want %q", got, tc.want)
			}
		})
	}
}
