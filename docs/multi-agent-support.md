# Multi-Agent Support in Assistant

## Overview

`assistant` drives coding agents through their CLIs. It does not call provider
HTTP APIs directly. Supported adapters are Cursor (Agent CLI), Claude Code,
OpenAI, and Grok Build.

## Supported Agents

### 1. Cursor (Agent CLI)

- **Config name**: `cursor`
- **Binaries on PATH** (first match wins): `cursor-agent`, `cursor`, `agent`
- **Install**: `curl https://cursor.com/install -fsS | bash`
- **Auth (CLI login only)**: `agent login` (or `cursor-agent login`). The adapter
  strips `CURSOR_API_KEY` from the process environment and never passes
  `--api-key`, so a key in the service environment cannot redirect runs onto
  the API-key route.
- **Sessions**: resumable fixer sessions (`--resume`)
- **Note**: Grok’s installer also links `agent`. Prefer `cursor-agent` on PATH
  when both are installed.

### 2. Claude (Claude Code)

- **Binary**: `claude`
- **Auth**: `claude` login / Pro·Max subscription, or `ANTHROPIC_API_KEY`
- **Sessions**: resumable fixer sessions
- **Terminal workers**: still require `ANTHROPIC_API_KEY` in the service
  environment (see `docs/operators.md`); each worker gets an isolated config
  directory that does not carry your login.

### 3. OpenAI

- **Binary**: `openai`
- **Sessions**: not supported
- Named-only; authenticate however that CLI documents.

### 4. Grok (Grok Build)

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
`auto` expands to Claude alone. Cursor, OpenAI, and Grok run only when named in
`agent`. `assistant doctor` lists any that resolve on PATH under "named-only
agents".

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
| Cursor | `cursor-agent --version` or `agent --version` |
| Claude | `claude --version` |
| OpenAI | `openai --version` |
| Grok | `grok version` |

## Agent Capabilities

| Feature | Cursor | Claude | OpenAI | Grok |
|---------|--------|--------|--------|------|
| Basic execution | ✅ | ✅ | ✅ | ✅ |
| Structured output | ✅ | ✅ | ✅ | ✅ |
| Resumable sessions | ✅ | ✅ | ❌ | ✅ |
| CLI login (no API key) | ✅ | ✅ (print mode) | TBD | ✅ |
| Terminal workers | ❌ | ✅ | ❌ | ❌ |

## How It Works

1. Read `agent` config key (defaults to `["auto"]`)
2. If `"auto"`, expand to auto-eligible adapters (Claude)
3. For each entry, resolve the binary on PATH and use the first that can run
4. If none are available, refuse before the first stage

Each adapter executes the agent CLI, passes the prompt (stdin or
`--prompt-file`), reads a JSON result envelope from stdout, and returns a
structured `Result`. Cursor and Grok strip their provider API-key environment
variables so authentication is the CLI login.

## Troubleshooting

### "No runnable agent"

Install at least one agent CLI and ensure it is on the service’s PATH.

### Cursor resolves as Grok’s `agent`

Install Cursor’s CLI and ensure `cursor-agent` appears on PATH ahead of a
Grok-owned `agent` symlink, or pin the binary:

```yaml
# Not supported as a path in agent: — put cursor-agent on PATH instead.
agent: cursor
```

### Cursor / Grok: not authenticated

```bash
agent login          # Cursor
grok login           # Grok (browser)
grok login --device-auth   # Grok headless
```

Do not set `CURSOR_API_KEY` or `XAI_API_KEY` for `assistant` runs; the adapters
strip those variables on purpose.

### Wrong agent selected

`auto` only picks Claude. Name the agent you want:

```yaml
agent: cursor
```
