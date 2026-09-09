# Multi-Agent Support in Assistant

## Overview

As of this update, `assistant` supports multiple LLM providers through a pluggable agent architecture. You can use Cursor, Claude, OpenAI, or Grok agents to power the validation pipeline.

## Supported Agents

### 1. Cursor
- **Binary**: `cursor` or `cursor-cli`
- **Features**: Full agent support, optimized for Cursor environment
- **Sessions**: TBD based on Cursor CLI capabilities

### 2. Claude (Claude Code)
- **Binary**: `claude`
- **Features**: Full-featured with resumable sessions
- **Sessions**: ✅ Supported (fixer sessions persist across rounds)

### 3. OpenAI
- **Binary**: `openai` or `openai-cli`
- **Features**: Core agent functionality
- **Sessions**: ❌ Not supported

### 4. Grok
- **Binary**: `grok` or `grok-cli`
- **Features**: Core agent functionality
- **Sessions**: ❌ Not supported

## Configuration

### Auto-Detection (Default)

By default, `assistant` uses `"auto"` which tries agents in this order:
1. Cursor (most common in Cursor environments)
2. Claude (full-featured)
3. OpenAI
4. Grok

```yaml
# .assistant.yaml
# No agent configuration needed - uses auto-detection
```

### Explicit Agent Selection

Specify a single agent:

```yaml
# .assistant.yaml
agent: cursor
```

### Fallback List

Provide an ordered fallback list:

```yaml
# .assistant.yaml
agent:
  - cursor
  - claude
  - openai
```

The first runnable agent in the list will be used.

## CLI Binary Requirements

Each agent requires its CLI tool to be installed and on PATH:

- **Cursor**: Install Cursor CLI
- **Claude**: Install Claude Code CLI
- **OpenAI**: Install OpenAI CLI
- **Grok**: Install Grok CLI

Check availability with:

```bash
assistant doctor
```

This will report which agents are available in your environment.

## Agent Capabilities

| Feature | Cursor | Claude | OpenAI | Grok |
|---------|--------|--------|--------|------|
| Basic execution | ✅ | ✅ | ✅ | ✅ |
| Structured output | ✅ | ✅ | ✅ | ✅ |
| Resumable sessions | TBD | ✅ | ❌ | ❌ |
| Project instructions | ✅ | ✅ | TBD | TBD |

## How It Works

### Agent Resolution

1. Read `agent` config key (defaults to `["auto"]`)
2. If `"auto"`, expand to full catalog in priority order
3. For each agent name:
   - Check if the binary exists on PATH
   - If yes, use that agent
   - If no, try the next one
4. If none are available, error

### Agent Invocation

All agents follow the same interface:

```go
type Runner interface {
    Name() string
    Capabilities() Capabilities
    Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error)
}
```

Each adapter:
1. Executes the agent CLI with appropriate flags
2. Passes the prompt via stdin
3. Reads JSON result envelope from stdout
4. Parses findings/reports from the result
5. Returns structured Result

### Result Format

All agents must output a JSON envelope:

```json
{
  "result": "<stage report or review report>",
  "model": "<model used>",
  "usage": {
    "input_tokens": 1000,
    "output_tokens": 500
  }
}
```

## Adding a New Agent

To add support for another LLM provider:

1. Create `internal/agents/newagent.go`
2. Implement the Factory interface
3. Implement the Runner interface
4. Add factory to DefaultCatalog in `resolve.go`
5. Document capabilities and requirements

See `claude.go` as the reference implementation.

## Troubleshooting

### "No runnable agent"

None of the configured agents are available on PATH.

**Solution**: Install at least one agent CLI:
- `cursor --version`
- `claude --version`
- `openai --version`
- `grok --version`

### Agent fails to run

The binary exists but fails when invoked.

**Solution**: 
- Check authentication (most CLIs need API keys)
- Verify CLI version compatibility
- Run `<agent> --help` to test manually

### Wrong agent selected

Auto-detection picked an agent you don't want.

**Solution**: Explicitly configure the agent:
```yaml
agent: cursor  # Force Cursor
```

## Migration from Claude-only

No changes needed! If you have Claude installed, it will continue to work. The new agents are additive.

To switch to a different agent:

```yaml
# Old (still works)
agent: claude

# New (try Cursor first, fallback to Claude)
agent:
  - cursor
  - claude
```

## Future Enhancements

Planned additions:
- Anthropic API (direct API calls, no CLI)
- OpenAI API (direct API calls, no CLI)
- Gemini support
- Local model support (Ollama, etc.)
