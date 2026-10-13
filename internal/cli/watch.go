package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// watch is the fleet view: everything in flight, everything waiting on you.
//
// It reports what is in flight now and then follows the service's event
// stream, so a caller sees the current state before it sees a delta. That
// order is not a nicety: internal/ipc opens every subscription gapped, and a
// consumer that applied a delta before reading state back would be applying it
// to a base it never established.
//
// PRD section 9's four-question screen is the terminal interface's, which is
// internal/ui's and does not exist here. This prints, which is the same
// answers in the rendering this surface has.
func watch(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("watch", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	client, err := in.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	var tasks machine.Tasks
	if err := client.Call(ctx, ipc.MethodTasksList, nil, &tasks); err != nil {
		return nil, err
	}
	stream, err := client.Subscribe(ctx, machine.SubscribeRequest{})
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	in.report(tasks)
	in.progressf("watching; interrupt to stop")
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ipc.ErrStreamClosed) {
				return nil, nil
			}
			return nil, err
		}
		in.report(event)
	}
}

// report writes one thing the fleet view saw, in whichever rendering this
// invocation asked for. It writes as it goes rather than collecting an answer,
// because a view that only printed once the stream ended would be a log rather
// than a view.
func (in *invocation) report(v any) {
	if in.json {
		if err := machine.NewEncoder(in.env.Stdout).Encode(v); err != nil {
			in.progressf("%v", err)
		}
		return
	}
	readOut(in.env.Stdout, v)
}

// eventLine renders one event for a person, through internal/machine's
// phrase table: the fleet view speaks the same vocabulary the fleet listing
// does, because a stream is a sequence of the states that listing shows and a
// second vocabulary for it would be a second owner of the same phrases. The
// type and the revision are mechanics and are the structured rendering's.
//
// Each payload is the record its owner published - a store.Run for a run, a
// machine.Task for a task, a store.Hold for a hold - and is read as that
// type. A payload that does not read as its type, an event of a type this
// build names but nothing produces, and a type this build does not know all
// read as the one row for an update the view cannot describe; the stream's
// own two control events have rows of their own; and output arriving as
// activity is said to have arrived and is not relayed.
//
// The words that are not the table's go through the same escaping every
// other line a person reads does: a branch, a project, a hold's subject and
// what a worker said it is waiting on are text a person or an agent wrote.
func eventLine(event ipc.Event) string {
	switch event.Type {
	case ipc.TypeRunState:
		var run store.Run
		if err := json.Unmarshal(event.Payload, &run); err != nil {
			return machine.PhraseUnfamiliarUpdate.String()
		}
		return printable(run.Branch) + ": " + printable(machine.RecordPhrase(run).String())
	case ipc.TypeTaskState:
		var task machine.Task
		if err := json.Unmarshal(event.Payload, &task); err != nil {
			return machine.PhraseUnfamiliarUpdate.String()
		}
		return printable(task.Record.Project) + ": " + printable(machine.TaskPhrase(task).String())
	case ipc.TypeHoldState:
		var hold store.Hold
		if err := json.Unmarshal(event.Payload, &hold); err != nil {
			return machine.PhraseUnfamiliarUpdate.String()
		}
		return printable(machine.HoldPhrase(hold).String())
	case ipc.TypeGap:
		return machine.PhraseUpdatesMissed.String()
	case ipc.TypeServiceStopping:
		return machine.PhraseServiceStopping.String()
	}
	if ipc.Classify(event.Type) == ipc.ClassActivity {
		return machine.PhraseActivity.String()
	}
	return machine.PhraseUnfamiliarUpdate.String()
}
