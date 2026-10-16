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

The first runnable agent in the list is used. Naming a named-only adapter is
how you select it.

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
| Terminal workers | ❌ | ✅ | ❌ | ❌ | ❌ |

## How It Works

1. Read `agent` config key (defaults to `["auto"]`)
2. If `"auto"`, expand to auto-eligible adapters (Claude)
3. For each entry, resolve the binary on PATH and use the first that can run
4. If none are available, refuse before the first stage

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
