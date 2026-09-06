package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
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

// eventLine renders one event for a person. The payload is another package's
// record travelling as written, so what is printed is its type, its revision,
// and the payload as it stands rather than this package's account of it.
func eventLine(event ipc.Event) string {
	if len(event.Payload) == 0 {
		return fmt.Sprintf("%s (revision %d)", event.Type, event.Revision)
	}
	return fmt.Sprintf("%s (revision %d) %s", event.Type, event.Revision, event.Payload)
}
