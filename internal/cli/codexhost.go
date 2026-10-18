package cli

// Codex Stop hook observation, recorded from the installed CLI and its
// published hook documentation before this adapter was written. Re-run
// `codex --version` and read https://developers.openai.com/codex/hooks on
// the machine that installs the hook; this comment claims only what those
// two sources say and what the tests below exercise through the hook command
// Codex would invoke.
//
// Verified protocol (Stop event):
//   - Config file: $CODEX_HOME/hooks.json when CODEX_HOME is set, else
//     ~/.codex/hooks.json. Shape matches Claude's hooks table: a "hooks"
//     object whose "Stop" key holds a list of entries, each with a "hooks"
//     list of {"type":"command","command":...} objects.
//   - Turn-end input on the hook's standard input is one JSON object. The
//     re-entry signal is the boolean member stop_hook_active, spelled exactly
//     as Claude Code's Stop hook uses the same name for the same meaning.
//     Codex also sends turn_id, session_id, hook_event_name, and
//     last_assistant_message; host stop reads only stop_hook_active.
//   - Blocking a turn from ending: exit status 2 with the reason on standard
//     error, or exit 0 with {"decision":"block","reason":"..."} on standard
//     output. This adapter uses exit 2 and standard error, which the
//     documentation states the Stop hook treats as a continuation prompt and
//     not as ending the turn. Plain text on standard output with exit 0 is
//     invalid for Stop; exit 0 with no output allows the turn to end.
//   - Notification-only hooks (SessionStart, PostToolUse, and the rest) do
//     not decide whether a turn may end; Stop is the event that can.
//
// Gaps this adapter does not close:
//   - Hook trust: Codex skips non-managed hooks until a person trusts them in
//     /hooks; this installer does not trust or bypass that review.
//   - Multiple matching Stop hooks run concurrently; another hook cannot be
//     prevented from also running beside this one.
//   - The observation above was checked against published documentation and
//     hook-shaped stdin in tests; a full turn driven through `codex exec` is
//     gated on ASSISTANT_CODEX_HOST_LIVE in host_codex_test.go and is not
//     required for merge or stop-guard unit tests.
