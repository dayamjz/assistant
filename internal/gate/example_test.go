package gate_test

import (
	"context"
	"fmt"
	"log"

	"github.com/dayamjz/assistant/internal/gate"
)

// exampleIndex stands for the ownership index a caller already has open. In
// this product it is the *store.Store for the home the gate lives in.
var exampleIndex gate.Index

// Initializing a gate is one call, and running it again repairs rather than
// fails, so a caller recovering a home does not have to know which parts of
// which gate are missing.
//
// The ownership index is the home's store, and it is required: whether another
// working copy is still bound to a gate is what decides whether this one may be
// given it, and there is no answer to fall back on.
func ExampleInitialize() {
	g, err := gate.Initialize(context.Background(), gate.Spec{
		Home:        "/home/example/.assistant",
		WorkingPath: "/home/example/projects/widget",
		Command:     "/usr/local/bin/assistant",
	}, gate.WithIndex(exampleIndex))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("push to", gate.RemoteName, "reaches", g.Repository())
	if g.Reattached() {
		fmt.Println("this working copy moved; its run history is intact")
	}
}
