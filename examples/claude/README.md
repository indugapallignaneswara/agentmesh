# AgentMesh × Claude Code integration

These are **tracked templates** you copy into a project's `.claude/` directory
(which is git-ignored in this repo, so they live here instead). They give a
Claude Code session two things:

1. **Inbox pull** — a hook that injects waiting AgentMesh messages into context
   at turn boundaries (MCP can't push mid-turn, so the agent *pulls*).
2. **`/ask`** — a slash command to address a teammate by name.

Both are built on the `coord` CLI, so first build and install it:

```bash
go build -o ~/.local/bin/coord ./cmd/coord   # ensure ~/.local/bin is on PATH
```

## 1. Configure the workspace identity

Set these in your shell profile (or in the hook command itself). Every session
on this machine that should participate uses them:

```bash
export AGENTMESH_ENDPOINT=http://localhost:8080/mcp   # your AgentMesh server
export AGENTMESH_WORKSPACE=team                        # the shared workspace
export AGENTMESH_MEMBER=backend                        # THIS agent's name
```

Join once (or let the agent call `workspace_join` itself):

```bash
coord join --kind agent
```

## 2. Install the inbox-pull hook

Copy the hook script somewhere stable and make it executable:

```bash
cp examples/claude/hooks/agentmesh-pull.sh ~/.local/bin/agentmesh-pull.sh
chmod +x ~/.local/bin/agentmesh-pull.sh
```

Then merge the `hooks` block from [`settings.json`](settings.json) into your
project's `.claude/settings.json`, fixing the absolute path to the script.

- **SessionStart** (`startup`, `resume`) pulls messages when a session begins.
- **PostToolUse** (`*`) pulls after every tool call, so a teammate's reply
  surfaces at the next turn boundary.

The script is non-blocking: if the server is down or config is missing, it adds
no context and never wedges the session. With `jq` installed the injected block
is nicely formatted; without it, raw JSON is injected.

## 3. Install the `/ask` command

```bash
mkdir -p .claude/commands
cp examples/claude/commands/ask.md .claude/commands/ask.md
```

Usage inside a session:

```
/ask frontend can you publish the new API types?
```

This delivers a direct message to `frontend`'s inbox via
`coord send --to frontend ...`. They receive it at their next turn boundary.

## Codex / other agents

Codex registers the MCP server directly (see [`../codex.toml`](../codex.toml)).
For agents without hook support, run `coord inbox` manually or on a timer to
pull messages, and `coord send` / `coord broadcast` to address others — the CLI
is the universal fallback path.
