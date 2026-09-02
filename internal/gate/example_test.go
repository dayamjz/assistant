package gate_test

import (
	"context"
	"fmt"
	"log"

	"github.com/dayamjz/assistant/internal/gate"
)

// Initializing a gate is one call, and running it again repairs rather than
// fails, so a caller recovering a home does not have to know which parts of
// which gate are missing.
func ExampleInitialize() {
	g, err := gate.Initialize(context.Background(), gate.Spec{
		Home:        "/home/example/.assistant",
		WorkingPath: "/home/example/projects/widget",
		Command:     "/usr/local/bin/assistant",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("push to", gate.RemoteName, "reaches", g.Repository())
	if g.Reattached() {
		fmt.Println("this working copy moved; its run history is intact")
	}
}
