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
