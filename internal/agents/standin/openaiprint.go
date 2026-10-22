package standin

import (
	"encoding/json"
	"slices"
)

// openaiArgs reports whether args are the OpenAI adapter's one-shot command
// line: --output-format with neither --print nor --prompt-file. The OpenAI
// adapter writes --output-format and nothing else of its own, and takes the
// prompt on standard input. Claude Code and Cursor write --print with it and
// Grok Build writes --prompt-file, so without this an OpenAI one-shot would
// be answered as a line-mode worker, which prints no envelope at all.
func openaiArgs(args []string) bool {
	return slices.Contains(args, "--output-format") && !oneShotArgs(args)
}

// openaiWire encodes the envelope as the OpenAI adapter's reader decodes it:
// the result, the model, and two token counts under the chat completion
// names, prompt_tokens and completion_tokens. It carries no cache count and
// no turn count, because the reader names none.
//
// No CLI printing this was recorded. The adapter names a binary called
// "openai" taking --output-format json, and nothing in this repository
// shows one, so this is that reader's model written from the other side and
// not an observed output: what a probe over it establishes is that the
// adapter reads these two keys and declares what it reads, not that a real
// CLI prints them.
func (e Envelope) openaiWire(c Call) ([]byte, error) {
	model := e.Model
	if model == "" {
		model = c.Model()
	}
	if model == "" {
		model = DefaultModel
	}
	return json.Marshal(map[string]any{
		"is_error": e.IsError,
		"result":   e.Result,
		"model":    model,
		"usage": map[string]any{
			"prompt_tokens":     e.Usage.InputTokens,
			"completion_tokens": e.Usage.OutputTokens,
		},
	})
}
