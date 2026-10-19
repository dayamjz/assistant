// Command releasetag is what the release workflow invokes to refuse a tag that
// is not a well-formed SemVer release version before anything is built.
package main

import (
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/buildinfo"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: releasetag VERSION")
		os.Exit(2)
	}
	if !buildinfo.WellFormedReleaseTag(os.Args[1]) {
		os.Exit(1)
	}
}
