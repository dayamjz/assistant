package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// readHostInput reads the turn-end event the coordinator's host wrote to the
// hook's standard input and reports whether it carries the re-entry signal,
// hostReentryField true, and, when input was there and could not be read as
// one object, why. hostReentryField's comment is the contract: absent or
// empty input and an object without the member are no signal and no
// explanation; everything else that is not a clean true is no signal with
// an explanation. Nothing here ever reports a signal it did not read.
//
// The read is bounded by hostInputLimit bytes and by hostInputWait, and by
// the invocation's context, so neither a stream nobody closes nor a large one
// holds the turn. A standard input that is a character device - a terminal a
// person typed the command at, or the null device - is not read at all and
// counts as absent, since a host writing its event does not hand the hook a
// terminal and waiting on a person's keyboard would only spend the wait.
//
// A read the wait gave up on is left running on its goroutine, because an
// io.Reader offers no way to cancel one. The process this command is ends
// right after answering, which ends it; a caller of cli.Run that lives on,
// as a test does, keeps that goroutine until the reader returns.
func readHostInput(ctx context.Context, r io.Reader) (bool, string) {
	if r == nil {
		return false, ""
	}
	if f, ok := r.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return false, ""
		}
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, hostInputLimit+1))
		done <- result{data, err}
	}()
	timer := time.NewTimer(hostInputWait)
	defer timer.Stop()
	var got result
	select {
	case got = <-done:
	case <-timer.C:
		return false, fmt.Sprintf("standard input did not end within %s, so it was not read", hostInputWait)
	case <-ctx.Done():
		return false, "standard input was not read before the invocation ended: " + ctx.Err().Error()
	}
	if got.err != nil {
		return false, "standard input could not be read: " + got.err.Error()
	}
	if len(got.data) > hostInputLimit {
		return false, fmt.Sprintf("standard input is longer than %d bytes, so it was not read", hostInputLimit)
	}
	return parseHostInput(got.data)
}

// parseHostInput is readHostInput's parse, over input already read whole:
// one JSON object and nothing after it, its members walked in order so a
// member written twice is seen rather than resolved to its last value, and
// hostReentryField's value accepted only as the literal true or false.
func parseHostInput(data []byte) (bool, string) {
	if len(bytes.TrimSpace(data)) == 0 {
		return false, ""
	}
	unread := func(why string) (bool, string) {
		return false, "standard input is not the turn-end event as one JSON object: " + why
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	open, err := dec.Token()
	if err != nil {
		return unread(err.Error())
	}
	if open != json.Delim('{') {
		return unread("it does not begin with an object")
	}
	seen := 0
	signal := false
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return unread(err.Error())
		}
		name, ok := token.(string)
		if !ok {
			return unread("a member has no name")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return unread(err.Error())
		}
		if name != hostReentryField {
			continue
		}
		seen++
		switch string(value) {
		case "true":
			signal = true
		case "false":
			signal = false
		default:
			return false, fmt.Sprintf("standard input's %s is not the literal true or false, so it is no signal", hostReentryField)
		}
	}
	if closing, err := dec.Token(); err != nil || closing != json.Delim('}') {
		return unread("the object does not end")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return unread("something follows the object")
	}
	if seen > 1 {
		return false, fmt.Sprintf("standard input names %s %d times, so it is no signal", hostReentryField, seen)
	}
	return signal, ""
}
