# Multi-Agent Support in Assistant

## Overview

`assistant` drives coding agents through their CLIs. It does not call provider
HTTP APIs directly. Supported adapters are Cursor (Agent CLI), Claude Code,
OpenAI, and Grok Build.

## Supported Agents

### 1. Cursor (Agent CLI)

- **Config name**: `cursor`
- **Binaries on PATH** (first match wins): `cursor-agent`, then `cursor`
- **Install**: `curl https://cursor.com/install -fsS | bash`
- **Auth (CLI login only)**: `cursor-agent login`. The adapter strips
  `CURSOR_API_KEY` from the process environment and never passes `--api-key`,
  so a key in the service environment cannot redirect runs onto the API-key
  route.
- **Prompt delivery**: trailing argv after `--` (the CLI’s documented
  `prompt...` argument; not stdin). Large prompts are still bounded by
  `MaxPromptBytes`; there is no Cursor `--prompt-file`.
- **Sessions**: resumable fixer sessions (`--resume`)
- **Workers**: persistent ACP pipe sessions; attachable terminal workers are not declared.
- **Note**: Grok’s installer also links `agent`. That name is **not** a Cursor
  candidate here, so a machine with only Grok’s `agent` will not resolve as
  `cursor`. Put `cursor-agent` on PATH.

### 2. Claude (Claude Code)

- **Binary**: `claude`
- **Auth**: `claude` login / Pro·Max subscription, or `ANTHROPIC_API_KEY`
- **Sessions**: resumable fixer sessions
- **Terminal workers**: still require `ANTHROPIC_API_KEY` in the service
  environment (see `docs/operators.md`); each worker gets an isolated config
  directory that does not carry your login.

### 3. Codex (OpenAI Codex CLI)

- **Config name**: `codex`
- **Binary**: `codex`
- **Install**: `npm install -g @openai/codex` (or the upstream install path)
- **Auth (CLI login only)**: `codex login`. The adapter strips `OPENAI_API_KEY`
  from the process environment so a key in the service environment cannot
  redirect runs onto a different path than the logged-in credentials.
- **Invocation**: `codex exec --json` (JSONL events); resume via
  `codex exec resume <thread_id>`
- **Prompt delivery**: trailing argv after `--` (stdin closed empty; Codex
  would otherwise treat a live stdin as more prompt input)
- **Sessions**: resumable fixer sessions (`thread_id` from `thread.started`)
- **Grants**: `--sandbox read-only` (review); `--approve-for-me` alone (fix;
  that flag already sets workspace-write and conflicts with `--sandbox` on
  the Codex CLI). Residual: fix grant still permits shell through Codex
  automatic review, not a hard Bash deny.

### 4. OpenAI (legacy stub)

- **Binary**: `openai`
- **Sessions**: not supported
- Named-only Claude-shaped stub; prefer `codex` for the Codex CLI.

### 5. Grok (Grok Build)

- **Binary**: `grok`
- **Install**: `curl -fsSL https://x.ai/cli/install.sh | bash`
- **Auth (CLI login only)**: `grok login` or `grok login --device-auth` for
  headless hosts. The adapter strips `XAI_API_KEY` and never passes an API key
  flag, so runs stay on the login / inference-proxy path.
- **Prompt delivery**: `--prompt-file` (not stdin / `--print`)
- **Sessions**: resumable fixer sessions (`--resume` with `sessionId`)

## Configuration

### Auto-Detection (Default)

By default, `assistant` uses `"auto"`, which tries only adapters that declare
`AutoEligible`. PRD section 12 makes Claude Code the only phase-one adapter, so
`auto` expands to Claude alone. Cursor, Codex, OpenAI, and Grok run only when
named in `agent`. `assistant doctor` lists any that resolve on PATH under
"named-only agents".

```yaml
# .assistant.yaml
# No agent configuration needed - uses auto → Claude when present
```

### Explicit Agent Selection

```yaml
agent: cursor
```

```yaml
agent: grok
```

### Model pin and fallback list

```yaml
agent: cursor --model gpt-5
```

```yaml
agent:
  - cursor
  - claude
  - grok
```

The first runnable agent in the list is used for validation runs. Naming a
named-only adapter is how you select it.

### GPT review roles

Codex translates the configured `openai` / `gpt` versions `6-astra`,
`6.1-sol`, and `6-luna` into `gpt-<version>`. A named effort (`low`,
`medium`, or `high`) is passed as a `model_reasoning_effort` configuration
override on both fresh and resumed invocations. Omitting it leaves the CLI's
configured effort in effect. Translation does not establish model access in
an installed CLI or account.

For example, in the global `config.yaml`:

```yaml
roles:
  review:
    agent: codex
    provider: openai
    type: gpt
    version: "6.1-sol"
    effort: high
```

The review uses a fresh context, separate from the fix role's session.
Codex still declares no worker capability; this setting enables review,
not task workers.

### Worker routing (GAP-02)

Task workers are routed separately from the run agent. `agents.Route` picks
the first runnable entry whose declared capabilities cover the worker need
(`worker_sessions`, and `terminal_workers` when `session_provider: tmux`):

- A **single named** `agent` entry (not `auto`) is an operator pin: if that
  adapter lacks the need, launch is refused with no fallthrough.
- A **list** is ordered preference with capability filtering: a runnable
  adapter that cannot host a worker is skipped; the next covering candidate
  wins.
- A task may name a `roles` entry. That role's agent is the pin for that
  task's launch. A role that names a provider, type, and version is
  translated by the adapter into the model identifier it starts, and an
  adapter that has not declared that translation is refused with nothing
  else started. An empty `roles` leaves every task on the `agent` list. The
  default `fix` role is `cursor`; a table that omits `fix` keeps that fixer,
  and a `fix` entry with no agent uses the `agent` list. A task that names
  `review` or `fix`, or a role that is not configured, is refused at intake
  and not created.
- Every successful selection records `adapter` and `route_reason`
  (`override` or `ordered`) on the task attempt before the process starts,
  and the role, model identifier, and effort when the launch named them.
- A routing refusal records a `route-refused` event; the task resolves
  Failed rather than staying Dispatched. Grants stay `GrantWorker` on the
  ordinary Create path — a miss does not widen permissions.

`agent: cursor` supports reviews and ACP pipe workers. Attachable tmux
workers require an adapter declaring terminal workers, currently Claude.
A Cursor pin with `session_provider: tmux` refuses instead of changing the
session provider. An explicit role backup is described below.

## CLI Binary Requirements

Check availability with:

```bash
assistant doctor
```

| Adapter | Check |
|---------|--------|
| Cursor | `cursor-agent --version` |
| Claude | `claude --version` |
| Codex | `codex --version` |
| OpenAI | `openai --version` (legacy stub) |
| Grok | `grok version` |

## Agent Capabilities

| Feature | Cursor | Claude | Codex | OpenAI | Grok |
|---------|--------|--------|-------|--------|------|
| Basic execution | ✅ | ✅ | ✅ | ✅ | ✅ |
| Structured output | ✅ | ✅ | ✅ | ✅ | ✅ |
| Resumable sessions | ✅ | ✅ | ✅ | ❌ | ✅ |
| CLI login (no API key) | ✅ | ✅ (print mode) | ✅ | TBD | ✅ |
| Pipe workers | ✅ | ✅ | ❌ | ❌ | ❌ |
| Terminal workers | ❌ | ✅ | ❌ | ❌ | ❌ |

## How It Works

1. Read `agent` config key (defaults to `["auto"]`)
2. If `"auto"`, expand to auto-eligible adapters (Claude)
3. For each entry, resolve the binary on PATH and use the first that can run
4. If none are available, refuse before the first stage
5. For task workers, run the same list through capability-aware routing
   (see above); record the winner or fail the task with `route-refused`

Each adapter executes the agent CLI, passes the prompt (Cursor/Codex: trailing
argv; Claude: stdin; Grok: `--prompt-file`), reads a structured result from
stdout (Claude-shaped JSON envelope, or Codex JSONL events), and returns a
structured `Result`. Cursor, Codex, and Grok strip their provider API-key
environment variables so authentication is the CLI login.

## Troubleshooting

### "No runnable agent"

Install at least one agent CLI and ensure it is on the service’s PATH.

### `agent: cursor` will not resolve

Put `cursor-agent` (or `cursor`) on PATH. The bare name `agent` is not a
Cursor candidate — Grok’s installer owns that symlink. Pinning a path is not
supported in the `agent:` key; fix PATH instead.

```yaml
agent: cursor
```

### Cursor / Grok: not authenticated

```bash
cursor-agent login   # Cursor
codex login          # Codex
grok login           # Grok (browser)
grok login --device-auth   # Grok headless
```

Do not set `CURSOR_API_KEY`, `OPENAI_API_KEY`, or `XAI_API_KEY` for
`assistant` runs; the adapters strip those variables on purpose.

### Wrong agent selected

`auto` only picks Claude. Name the agent you want:

```yaml
agent: cursor
```

### Task stays Failed with `route-refused`

The configured agent list has no adapter that declares the worker
capabilities the session provider needs. Pin Claude, or list a covering
adapter after your preferred run agent. Cursor supports pipe workers, but a
Cursor-only pin with tmux is refused. A backup never changes the chosen
session provider or widens grants.

## Explicit availability fallback

Each role may name one `fallback` with its own agent and optional model and
effort. A missing executable or a recognized quota/authentication/service
failure reaches that backup. Ordinary errors, invalid reports, cancellation,
deadlines, context exhaustion, invalid settings, and missing capabilities
refuse rather than changing agents. Unknown quota wording also refuses.
Claude's monthly spend-limit sentence is a quota line. A line is recognized
when it is a known line, or begins with one and then a space, a period, or a
colon. The shorter limit sentence does not cover the monthly spend-limit
sentence, and a mention later in a line stays unrecognized.
There is no dependency on a quota-reporting CLI or live quota polling.

```yaml
agent: cursor
roles:
  worker:
    agent: claude
    fallback:
      agent: cursor
  fix:
    agent: claude
    fallback:
      agent: cursor
  review:
    agent: codex
    provider: openai
    type: gpt
    version: "6.1-sol"
    effort: high
    fallback:
      agent: cursor
```

`agent: cursor` handles general stages while the named worker and fixer
roles still try Claude first. Backups do not inherit the primary's model or
effort. Each review is fresh. A fixer handoff starts a new conversation on
the backup and keeps its agent-qualified reference across resume.

A quota-limited pipe worker is stopped before its backup starts. The existing
 bounded relaunch sweeps and reasserts its same copy, preserving committed and
 uncommitted work. Its written-once handoff selection survives service restart;
it does not switch back within that task. A new task tries its primary again.
Fresh reviews also re-resolve a primary that was missing at service startup.
A failed safety proof or an exhausted relaunch bound follows the existing
 refusal/hold path. A backup outage raises a retry/stop decision; it does not start a third agent.
A retry that failed before a session was recorded reuses dispatch's existing
standing-copy sweep and the same relaunch bound.

Cursor workers require `session_provider: pipe` (the default). Worker backup capabilities are checked before any worker starts. A tmux provider
is refused for Cursor rather than replaced, because Cursor terminal support
 is not declared. Its ACP client grants only read/edit/search requests and
literal git or worker-route commands without shell operators; other permission
requests are cancelled. The CLI's own preapproved tools remain outside that
client policy. No API key is added; Cursor uses its CLI login.

`ASSISTANT_CURSOR_WORKER_LIVE=1 go test ./internal/agents -run TestInstalledCursorACPWorker`
checks an installed Cursor CLI with a real model call. Without the explicit
opt-in it skips, so ordinary CI proves the scripted wire, not model access.
