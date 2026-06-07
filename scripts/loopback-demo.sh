#!/usr/bin/env bash
#
# loopback-demo.sh — a single-host simulation of the AgentMesh Phase 0 success
# metric: two members address each other by name through one workspace, and a
# broadcast fans out to all present members.
#
# This is a SIMULATION, not the cross-machine acceptance test. It runs one
# server and two CLI "members" on the same host to prove the coordination
# semantics end to end. For the real cross-machine / cross-vendor test, see
# docs/validation.md.
#
# Requires: a Go toolchain. Uses AGENTMESH_STORE=memory so no Postgres/NATS are
# needed. Picks an ephemeral port and cleans up the server on exit.

set -euo pipefail

cd "$(dirname "$0")/.."

PORT="${PORT:-8087}"
ENDPOINT="http://127.0.0.1:${PORT}/mcp"
WS="demo"
BIN_DIR="$(mktemp -d)"
SERVER_LOG="$(mktemp)"
SERVER_PID=""

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$BIN_DIR" "$SERVER_LOG"
}
trap cleanup EXIT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILED=1; }
FAILED=0

echo "==> Building agentmesh + coord"
go build -o "$BIN_DIR/agentmesh" ./cmd/agentmesh
go build -o "$BIN_DIR/coord" ./cmd/coord

echo "==> Starting server (in-memory store) on port ${PORT}"
AGENTMESH_STORE=memory AGENTMESH_HTTP_ADDR="127.0.0.1:${PORT}" AGENTMESH_LOG_LEVEL=error \
  "$BIN_DIR/agentmesh" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

# Wait for health.
for _ in $(seq 1 30); do
  if curl -sf -m 2 "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.2
done
if ! curl -sf -m 2 "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
  echo "server failed to start; log:"; cat "$SERVER_LOG"; exit 1
fi

export AGENTMESH_ENDPOINT="$ENDPOINT" AGENTMESH_WORKSPACE="$WS"
COORD="$BIN_DIR/coord"

# Two members standing in for two different agent vendors/sessions:
#   "claude-backend"  — as if a Claude Code session on machine A
#   "codex-frontend"  — as if a Codex session on machine B
A="claude-backend"
B="codex-frontend"
HUMAN="alice"

echo "==> Members join the shared workspace"
AGENTMESH_MEMBER="$HUMAN" "$COORD" join --kind human  >/dev/null
AGENTMESH_MEMBER="$A"     "$COORD" join --kind agent  >/dev/null
AGENTMESH_MEMBER="$B"     "$COORD" join --kind agent  >/dev/null

echo "==> Test 1: presence lists all three"
present=$(AGENTMESH_MEMBER="$HUMAN" "$COORD" --json presence | grep -c '"name"' || true)
[ "$present" -eq 3 ] && pass "3 members present" || fail "expected 3 present, got $present"

echo "==> Test 2: any-to-any — $HUMAN addresses $A by name"
AGENTMESH_MEMBER="$HUMAN" "$COORD" send --to "$A" "deploy build 42" >/dev/null
got=$(AGENTMESH_MEMBER="$A" "$COORD" inbox)
echo "$got" | grep -q "deploy build 42" && pass "$A received the direct message" || fail "$A inbox: $got"

echo "==> Test 3: the message was addressed only to $A (not $B)"
otherbox=$(AGENTMESH_MEMBER="$B" "$COORD" inbox)
echo "$otherbox" | grep -q "no new messages" && pass "$B correctly received nothing" || fail "$B unexpectedly got: $otherbox"

echo "==> Test 4: consume-on-read — $A's second read is empty"
second=$(AGENTMESH_MEMBER="$A" "$COORD" inbox)
echo "$second" | grep -q "no new messages" && pass "message consumed on first read" || fail "re-read returned: $second"

echo "==> Test 5: many-to-many — $A broadcasts to everyone else"
recips=$(AGENTMESH_MEMBER="$A" "$COORD" --json broadcast "standup now" | grep -o '"recipients": *[0-9]*' | grep -o '[0-9]*')
[ "$recips" = "2" ] && pass "broadcast reached 2 recipients" || fail "broadcast recipients=$recips, want 2"
b1=$(AGENTMESH_MEMBER="$HUMAN" "$COORD" inbox)
b2=$(AGENTMESH_MEMBER="$B" "$COORD" inbox)
echo "$b1" | grep -q "standup now" && echo "$b2" | grep -q "standup now" \
  && pass "both other members received the broadcast" || fail "broadcast not delivered to all"

echo "==> Test 6: observation log reflects the activity"
events=$(AGENTMESH_MEMBER="$HUMAN" "$COORD" --json subscribe --since 0 | grep -c '"type"' || true)
[ "$events" -ge 5 ] && pass "event log has $events entries" || fail "expected >=5 events, got $events"

# --- Phase 1: shared task board ---

echo "==> Test 7: shared task board — dependency-gated claiming, no double-claim"
# Create "build", then "deploy" depending on it.
t1=$(AGENTMESH_MEMBER="$HUMAN" "$COORD" --json task create --title "build" \
  | grep -o '"id": *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
AGENTMESH_MEMBER="$HUMAN" "$COORD" task create --title "deploy" --depends-on "$t1" >/dev/null

# $A claims: must get "build" (deploy is blocked).
claimA=$(AGENTMESH_MEMBER="$A" "$COORD" task claim)
echo "$claimA" | grep -q "build" && pass "$A claimed the unblocked task (build)" || fail "$A claim: $claimA"

# $B claims: nothing, because deploy is still blocked by build.
claimB=$(AGENTMESH_MEMBER="$B" "$COORD" task claim)
echo "$claimB" | grep -q "no claimable task" && pass "$B correctly got nothing (deploy blocked)" || fail "$B claim: $claimB"

# $A completes build -> deploy becomes claimable.
AGENTMESH_MEMBER="$A" "$COORD" task complete --id "$t1" --result "built" >/dev/null
claimB2=$(AGENTMESH_MEMBER="$B" "$COORD" task claim)
echo "$claimB2" | grep -q "deploy" && pass "$B claimed deploy after its dependency completed" || fail "$B second claim: $claimB2"

echo
if [ "$FAILED" -eq 0 ]; then
  printf '\033[32mAll loopback checks passed.\033[0m This simulates the Phase 0 + Phase 1 metrics on one host.\n'
  printf 'For the real cross-machine / cross-vendor test, see docs/validation.md.\n'
else
  printf '\033[31mSome checks failed.\033[0m See output above.\n'
  exit 1
fi
