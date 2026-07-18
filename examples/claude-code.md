# Registering AgentMesh with Claude Code

Add the running server as an HTTP MCP server:

```bash
claude mcp add --transport http agentmesh http://localhost:8080/mcp
```

Then, from a Claude Code session, the coordination tools are available as
`workspace_join`, `send_message`, `read_inbox`, `broadcast`, `publish_event`,
`subscribe`, and `workspace_presence`.

A minimal flow:

1. `workspace_join` with `{ "workspace": "team", "name": "backend", "kind": "agent" }`
2. `broadcast` `{ "workspace": "team", "from": "backend", "body": "I'm online" }`
3. `read_inbox` `{ "workspace": "team", "member": "backend" }`

## Pulling messages in automatically (hooks)

Because MCP can't push mid-turn, use a `SessionStart` / `PostToolUse` hook that
calls `read_inbox` and injects any messages into context. A sketch
(`.claude/settings.json`):

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [{ "type": "command", "command": "agentmesh-pull --workspace team --member backend" }] }
    ]
  }
}
```

`agentmesh-pull` is a thin CLI wrapper (Phase 4) over `read_inbox`; until then
call `read_inbox` explicitly when you want to check for messages.

## Reporting vendor token usage

AgentMesh measures coordination bytes exactly, but the tokens your agent burns
at its own LLM vendor are invisible to the mesh unless the agent (or a session
hook) reports them. Reported numbers are **unverified client claims**:
AgentMesh stores them as `reported`, displays them under a "reported" label
next to its own byte-derived estimates, and never bills or enforces budgets on
them alone.

### Agent-side instruction

Add a line like this to the agent's system prompt / CLAUDE.md so it reports
after substantial turns:

> After completing a large turn, call `usage_report` with your known usage for
> that turn, e.g. `{ "workspace": "team", "member": "backend",
> "prompt_tokens": 18234, "completion_tokens": 412, "vendor": "anthropic",
> "model": "claude-sonnet-4-5" }`. Report deltas since your previous report,
> not cumulative totals.

### Stop-hook sketch (best effort)

Claude Code hook payloads do **not** reliably carry token counts — the Stop
hook receives a `transcript_path`, and whether usage appears in the transcript
depends on the Claude Code version and API response shape. Treat this as a
best-effort recipe: when the numbers can't be parsed, the hook skips the
report rather than inventing one.

```json
{
  "hooks": {
    "Stop": [
      { "hooks": [{ "type": "command", "command": "agentmesh-report-usage" }] }
    ]
  }
}
```

`agentmesh-report-usage` (on PATH, alongside `coord`):

```sh
#!/bin/sh
# Best-effort: sum usage from the last assistant message in the transcript,
# if present. Requires jq; exits quietly when counts aren't available.
payload=$(cat)
transcript=$(printf '%s' "$payload" | jq -r '.transcript_path // empty')
[ -r "$transcript" ] || exit 0
read -r prompt completion <<EOJ
$(jq -rs '[.[] | .message.usage // empty] | last // empty |
  "\(.input_tokens // 0) \(.output_tokens // 0)"' "$transcript" 2>/dev/null)
EOJ
[ -n "$prompt" ] && [ "$((prompt + completion))" -gt 0 ] || exit 0
coord usage report --prompt "$prompt" --completion "$completion" \
  --vendor anthropic || true
```

`coord usage report` reads `AGENTMESH_WORKSPACE` / `AGENTMESH_MEMBER` from the
environment, so the same hook works for every agent in the fleet. The
dashboard's "Usage (24h)" panel then shows the reported numbers side by side
with the mesh's own estimates — labelled as claims, because that is what they
are.
