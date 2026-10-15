package standin

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/dayamjz/assistant/internal/agents"
)

// exitStreamParse is the status a stream-mode worker exits with on a line that
// is not JSON. It is 1 because that is the status the Claude Code CLI exited
// with when it was handed such a line in this mode; the package documentation
// says what that observation is and is not.
const exitStreamParse = 1

// streamInput reports whether args select streaming input: --print together with
// --input-format stream-json, in either of the two spellings an argument
// parser accepts for a flag with a value. The adapter writes the first.
func streamInput(args []string) bool {
	print, stream := false, false
	for i, a := range args {
		switch {
		case a == "--print":
			print = true
		case a == "--input-format=stream-json":
			stream = true
		case a == "--input-format" && i+1 < len(args) && args[i+1] == "stream-json":
			stream = true
		}
	}
	return print && stream
}

// streamLine is what the stand-in reads of one line in stream mode: the type,
// and for a user message the message it carries. Every other field a line
// holds is ignored, as a reader that names only what it uses does.
type streamLine struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// text is a user message's content as text: the string itself, or the text
// blocks of a content array joined in order. Those are the two forms of
// content a user message has on this wire. Anything else is refused, because
// a stand-in that guessed at it would be answering something it was not
// written to read.
func (l streamLine) text() (string, error) {
	var plain string
	if err := json.Unmarshal(l.Message.Content, &plain); err == nil {
		return plain, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(l.Message.Content, &blocks); err != nil {
		return "", errors.New("a user message whose content is neither text nor a list of blocks")
	}
	joined := ""
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("a user message carrying a %q block, which this stand-in does not read", block.Type)
		}
		joined += block.Text
	}
	return joined, nil
}

// serveStreamWorker runs this process as a worker in stream mode: it records
// itself, then reads standard input one JSON line at a time until it is
// closed, answering each user message as the script's exchanges say and
// rewriting its record as each arrives.
//
// What it does with a line is the whole of what the stand-in models of the
// CLI's streaming input, and each rule is one the conformance probe can be
// failed by:
//
//   - A line that is not JSON ends the worker with exitStreamParse and a note
//     on standard error, so a worker sent anything but a framed message stops
//     being driveable.
//   - A JSON line whose type is not "user" is passed over, with a note on
//     standard error, and recorded nowhere.
//   - A user message is recorded as its text, in Call.Lines, and answered by
//     the first exchange whose Contains the text holds, with three
//     stream-json lines on standard output: a system init, an assistant
//     message carrying the reply as a text block, and a successful result
//     carrying the reply. An exchange with Recorded lines prints those
//     instead, as they are. A message no exchange answers draws a note on
//     standard error and nothing on standard output.
//
// It exits when its input closes, or after a reply whose exchange says Ends.
// A script with a Greeting is refused, because a line printed before any
// message is a line-mode shape and not one the CLI in this mode was seen to
// produce. The refusal waits for the first input line, or for input to
// close, and prints nothing either way: exiting before the opener has written
// its first line would make the opener's write fail on a closed pipe or not,
// depending on which process the scheduler ran first, and a refusal that
// races the opener is not one a test can rely on.
func serveStreamWorker(control string, args []string) int {
	script, err := readScript(control)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoScript
	}
	worker := script.Worker
	if worker == nil {
		worker = &WorkerScript{}
	}
	if worker.Greeting != "" {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprintln(os.Stderr, "standin: a stream-mode worker prints nothing before a message, so a script's Greeting is a line-mode shape it does not produce")
		return exitNoScript
	}
	dir, _ := os.Getwd()
	call := Call{Args: args, Dir: dir, Step: -1, Worker: true, Stream: true}
	seq, err := claimCall(control, call)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoRecord
	}
	call.Seq = seq
	session := "standin-worker-" + strconv.Itoa(seq)

	scanner := bufio.NewScanner(os.Stdin)
	// A framed message is its text escaped, which can be several times the
	// text's own size, so the bound is a multiple of the largest text the
	// adapter accepts rather than that text's own limit.
	scanner.Buffer(make([]byte, 0, 64<<10), 8*agents.MaxPromptBytes)
	for scanner.Scan() {
		raw := scanner.Bytes()
		var line streamLine
		if err := json.Unmarshal(raw, &line); err != nil {
			fmt.Fprintf(os.Stderr, "standin: error parsing streaming input line (%d bytes): %v\n", len(raw), err)
			return exitStreamParse
		}
		if line.Type != "user" {
			fmt.Fprintf(os.Stderr, "standin: passing over a streaming input line of type %q\n", line.Type)
			continue
		}
		text, err := line.text()
		if err != nil {
			fmt.Fprintln(os.Stderr, "standin: "+err.Error())
			return exitNoStdin
		}
		call.Lines = append(call.Lines, text)
		if err := record(callsPath(control), call); err != nil {
			fmt.Fprintln(os.Stderr, "standin: "+err.Error())
			return exitNoRecord
		}
		answered := false
		for _, exchange := range worker.Exchanges {
			if !exchange.answers(text) {
				continue
			}
			reply, err := exchange.answer(text)
			if err != nil {
				fmt.Fprintln(os.Stderr, "standin: "+err.Error())
				return exitNoOutput
			}
			if len(exchange.Recorded) > 0 {
				_, err = fmt.Fprintln(os.Stdout, reply)
			} else {
				err = writeTurn(session, dir, reply)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "standin: writing a reply: "+err.Error())
				return exitNoOutput
			}
			answered = true
			if exchange.Ends {
				return worker.Exit
			}
			break
		}
		if !answered {
			fmt.Fprintf(os.Stderr, "standin: no exchange of the script answers this message: %s\n", shorten(text))
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "standin: reading a line: "+err.Error())
		return exitNoStdin
	}
	return worker.Exit
}

// writeTurn prints one answered turn as the three stream-json lines
// serveStreamWorker states.
func writeTurn(session, dir, reply string) error {
	type text struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	lines := []any{
		map[string]any{"type": "system", "subtype": "init", "session_id": session, "cwd": dir},
		map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"role": "assistant", "content": []text{{Type: "text", Text: reply}},
		}},
		map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": reply, "session_id": session},
	}
	encoder := json.NewEncoder(os.Stdout)
	for _, line := range lines {
		if err := encoder.Encode(line); err != nil {
			return err
		}
	}
	return nil
}
