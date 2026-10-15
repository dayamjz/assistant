package cli

import (
	"testing"
	"time"
)

// ShortenReceiveWait makes a receive command wait d rather than internal/gate's
// bound for the rest of the test, so a test reaches the refusal past the bound
// without waiting the bound out. It reaches only cli.Run in this process: a
// push's receive command is another process and still waits the real bound.
func ShortenReceiveWait(t *testing.T, d time.Duration) {
	t.Helper()
	was := receiveWait
	receiveWait = d
	t.Cleanup(func() { receiveWait = was })
}

// Subcommands is each verb that takes a subcommand and its table, read from
// the tables the verbs dispatch on, as name and summary pairs in table
// order, so a test holds a verb's help to every row it dispatches rather than
// to a list written beside it.
func Subcommands() map[string][][2]string {
	out := map[string][][2]string{}
	for _, action := range serviceActions {
		out["service"] = append(out["service"], [2]string{action.name, action.summary})
	}
	for _, hook := range gateHooks {
		out[gateVerbName] = append(out[gateVerbName], [2]string{hook.name, hook.summary})
	}
	for _, hook := range hostHooks {
		out[hostVerbName] = append(out[hostVerbName], [2]string{hook.name, hook.summary})
	}
	for _, action := range coordinatorActions {
		out[coordinatorVerbName] = append(out[coordinatorVerbName], [2]string{action.name, action.summary})
	}
	for _, action := range workerActions {
		out[workerVerbName] = append(out[workerVerbName], [2]string{action.name, action.summary})
	}
	return out
}

// WorkerRoute is the route the service start gives a worker for a binary at
// executable serving the home at root, so a test that opens the service
// itself gives its workers the route the product would.
func WorkerRoute(executable, root string) string { return workerRoute(executable, root) }
