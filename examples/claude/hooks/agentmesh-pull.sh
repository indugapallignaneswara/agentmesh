#!/usr/bin/env bash
#
# agentmesh-pull.sh — a Claude Code hook that pulls AgentMesh inbox messages
# into the model's context at a turn boundary.
#
# MCP can't push to an agent mid-turn, so this is the "pull" side of the Phase 0
# design: wire it to SessionStart and/or PostToolUse and it injects any waiting
# messages as additionalContext. It reads the hook JSON payload on stdin (only
# to stay well-behaved) and writes a hook JSON result on stdout.
#
# Requires: the `coord` binary on PATH and these env vars (set them in the hook
# command or your shell profile):
#   AGENTMESH_ENDPOINT   e.g. http://localhost:8080/mcp
#   AGENTMESH_WORKSPACE  e.g. team
#   AGENTMESH_MEMBER     this agent's name, e.g. backend
# Optional:
#   AGENTMESH_COORD      path to the coord binary (default: "coord" on PATH)
#
# No jq dependency: it uses coord's human-readable output, which already formats
# messages and prints "(no new messages)" when the inbox is empty.
#
# Exit code: always 0 (non-blocking). Any failure degrades to "no context
# added" so a coordination outage never wedges the agent's session.

set -uo pipefail

# Drain stdin (the hook payload) to avoid a broken pipe on the caller's side.
cat >/dev/null 2>&1 || true

# inject prints a hook result. With a non-empty argument it injects that text as
# additionalContext; with none it injects nothing (and suppresses output).
inject() {
  local ctx="${1:-}"
  if [ -z "$ctx" ]; then
    printf '{"continue": true, "suppressOutput": true}\n'
    return
  fi
  # JSON-escape the context string with a pure-bash escaper (backslash, quote,
  # then newlines -> \n via awk).
  local esc
  esc=${ctx//\\/\\\\}
  esc=${esc//\"/\\\"}
  esc=$(printf '%s' "$esc" | awk 'BEGIN{ORS=""} {if(NR>1) printf "\\n"; printf "%s",$0}')
  printf '{"continue": true, "hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": "%s"}}\n' "$esc"
}

# Guard: required config. If unset, do nothing rather than error.
if [ -z "${AGENTMESH_WORKSPACE:-}" ] || [ -z "${AGENTMESH_MEMBER:-}" ]; then
  inject ""
  exit 0
fi

coord_bin="${AGENTMESH_COORD:-coord}"
if ! command -v "$coord_bin" >/dev/null 2>&1; then
  inject ""
  exit 0
fi

# Pull undelivered messages (consume-on-read), human-readable form.
inbox=$("$coord_bin" inbox 2>/dev/null) || inbox=""

# Empty or the explicit "no messages" marker -> inject nothing.
if [ -z "$inbox" ] || printf '%s' "$inbox" | grep -q '^(no new messages)$'; then
  inject ""
  exit 0
fi

inject "AgentMesh — new messages for ${AGENTMESH_MEMBER} in workspace ${AGENTMESH_WORKSPACE}:
${inbox}"
exit 0
