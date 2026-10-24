package vcs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestMarkerLinesReadsPastItsBuffer feeds lines longer than the reader's
// buffer. A long line that is not a marker is passed over and the marker
// after it is still found; a long marker line is kept whole; a marker on a
// last line with no newline counts; a marker run shorter than seven, or one
// that does not start the line, does not.
func TestMarkerLinesReadsPastItsBuffer(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	longMarker := ">>>>>>> " + long
	content := long + "\n" +
		"<<<<<<< HEAD\r\n" +
		"<<<<<< short\n" +
		" >>>>>>> indented\n" +
		"=======\n" +
		longMarker + "\n" +
		"<<<<<<< HEAD\n" +
		"||||||| base"
	got, err := markerLines(context.Background(), strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"<<<<<<< HEAD", longMarker, "||||||| base"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %d lines starting %q, want %d", len(got), firstBytes(got), len(want))
	}
}

// TestMarkerLinesEndsWithItsContext reads under a context that has ended,
// which returns the context's error rather than the lines.
func TestMarkerLinesEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := markerLines(ctx, strings.NewReader("<<<<<<< HEAD\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want the cancellation", err)
	}
}

// firstBytes is each line cut to a length a failure message can show.
func firstBytes(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line[:min(len(line), 20)]
	}
	return out
}
