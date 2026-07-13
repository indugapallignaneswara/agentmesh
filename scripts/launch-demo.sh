#!/usr/bin/env bash
#
# launch-demo.sh — the AgentMesh product story on a single host, scripted end
# to end. It stages the v1.0 acceptance flow: a human owner creates an
# invite-only room and controls it; two agents join by invite and coordinate
# through the shared board; a human gates shared memory and, finally, ejects a
# misbehaving agent and reads the whole conversation back.
#
# This is a single-host SIMULATION of the coordination semantics, not the
# cross-machine security proof — it runs the in-memory store with auth off, so
# member identities are asserted, not authenticated (the coord CLI passes each
# actor's name and the server trusts it in auth=off mode). It demonstrates what
# the system DOES; docs/launch-demo.md's two-machine path is the real proof,
# and docs/operations.md covers running it authenticated.
#
# Requires: a Go toolchain. No Postgres/NATS. Picks an ephemeral-ish port and
# cleans the server up on exit.
#
#   ./scripts/launch-demo.sh          # pauses between steps (good for recording)
#   NO_PAUSE=1 ./scripts/launch-demo.sh   # run straight through (CI/self-check)

set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${PORT:-8090}"
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

bold() { printf '\033[1m%s\033[0m\n' "$1"; }
pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$1"; FAILED=1; }
FAILED=0

# step prints a headline and (unless NO_PAUSE) waits for Enter so a viewer can
# read the screen before the next action runs.
step() {
  echo
  bold "▶ $1"
  [ "${2:-}" ] && echo "  $2"
  if [ -z "${NO_PAUSE:-}" ]; then
    printf '  \033[2m(press Enter)\033[0m'; read -r _ || true
  fi
}

export AGENTMESH_ENDPOINT="$ENDPOINT"
export AGENTMESH_WORKSPACE="$WS"

# as <member> <coord args...> — run a coord command acting as <member>.
as() { local who="$1"; shift; AGENTMESH_MEMBER="$who" "$BIN_DIR/coord" "$@"; }

# denies <regex> <command...> — run a command we EXPECT to be refused, and
# succeed only if its (stderr+stdout) output matches <regex>. We must capture
# the output rather than pipe straight into grep: under `set -o pipefail` a
# refused coord call exits non-zero, which would poison an `if cmd | grep`
# pipeline even when grep matched. Capturing first isolates the assertion to
# the message, which is exactly what we're testing.
denies() {
  local re="$1"; shift
  local out; out="$("$@" 2>&1 || true)"
  printf '%s\n' "$out" | grep -qiE "$re"
}

echo "==> Building agentmesh + coord"
go build -o "$BIN_DIR/agentmesh" ./cmd/agentmesh
go build -o "$BIN_DIR/coord" ./cmd/coord

echo "==> Starting server (in-memory, auth off, explicit rooms) on port ${PORT}"
AGENTMESH_STORE=memory \
AGENTMESH_AUTH=off \
AGENTMESH_HTTP_ADDR="127.0.0.1:${PORT}" \
AGENTMESH_IMPLICIT_WORKSPACES=false \
AGENTMESH_RATE_LIMIT=true \
  "$BIN_DIR/agentmesh" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null || { echo "server did not start:"; cat "$SERVER_LOG"; exit 1; }
echo "    up. Watch it live at http://127.0.0.1:${PORT}/ui  (room: ${WS})"

# ---------------------------------------------------------------------------

step "The human owner creates the room and takes control" \
     "lead creates '${WS}', joins as human (becoming owner), and locks it down."
as lead room create --name "$WS" --creator lead
as lead join --kind human --name lead
as lead room policy --join invite --broadcast moderators
pass "room '${WS}' is invite-only; only moderators may broadcast"

step "The owner mints one single-use invite per agent"
CODE_A=$(as lead --json invite create --kind agent --max-uses 1 | grep -oP '"code"\s*:\s*"\Kami_[^"]+')
CODE_B=$(as lead --json invite create --kind agent --max-uses 1 | grep -oP '"code"\s*:\s*"\Kami_[^"]+')
[ -n "$CODE_A" ] && [ -n "$CODE_B" ] && pass "issued codes for alice and bob" || fail "invite codes not issued"

step "Two agents join by invite" \
     "A bare join is refused; the code is what admits them."
if denies 'invite' as mallory join --kind agent --name mallory; then
  pass "a codeless join into the invite-only room is rejected"
else
  fail "codeless join should have been rejected"
fi
as alice join --kind agent --name alice --invite "$CODE_A"
as bob   join --kind agent --name bob   --invite "$CODE_B"
as lead presence | grep -q alice && as lead presence | grep -q bob \
  && pass "alice and bob are in the room" || fail "agents not present"

step "The agents address each other by name" \
     "Direct messages are point-to-point: only the named recipient sees them."
as alice send --to bob "bob, what are you working on?"
if as bob inbox | grep -q "what are you working on"; then
  pass "bob received alice's direct message"
else fail "bob did not receive the message"; fi
as bob send --to alice "wiring up the parser; you take the lexer?"
as alice inbox | grep -q "take the lexer" && pass "alice received bob's reply" \
  || fail "alice did not receive the reply"

step "Only the human may broadcast" \
     "who_may_broadcast=moderators — an agent's broadcast is refused; the owner's fans out."
if denies 'moderator|not allowed|denied|permission' as alice broadcast --body "everyone stop and read this"; then
  pass "alice (agent) is blocked from broadcasting"
else fail "agent broadcast should have been blocked"; fi
as lead broadcast --body "stand-up in 5, wrap your current step"
{ as alice inbox | grep -q "stand-up in 5"; } && { as bob inbox | grep -q "stand-up in 5"; } \
  && pass "the owner's broadcast reached both agents" || fail "broadcast did not fan out"

step "The shared task board sequences work with dependencies" \
     "task 'test' depends on 'build' — it stays unclaimable until 'build' completes."
T_BUILD=$(as lead --json task create --title "build the binary" | grep -oP '"id"\s*:\s*"\K[^"]+' | head -1)
T_TEST=$(as lead --json task create --title "run the tests" --depends-on "$T_BUILD" | grep -oP '"id"\s*:\s*"\K[^"]+' | head -1)
CLAIM1=$(as alice task claim)
echo "    alice: $CLAIM1"
echo "$CLAIM1" | grep -q "$T_BUILD" && pass "alice claimed 'build' (the only claimable task)" \
  || fail "alice should have claimed the build task"
as alice task complete --id "$T_BUILD" --result "binary at ./agentmesh"
CLAIM2=$(as bob task claim)
echo "    bob:   $CLAIM2"
echo "$CLAIM2" | grep -q "$T_TEST" && pass "completing 'build' unblocked 'test'; bob claimed it" \
  || fail "the dependent task did not become claimable"

step "Shared memory is quarantined until a human approves it" \
     "alice proposes a shared fact; it is invisible to bob until lead approves."
as alice memory write --scope shared --content "the parser lives in internal/lang/parser.go"
if as bob memory search --query parser | grep -q "parser.go"; then
  fail "pending shared memory should NOT be searchable yet"
else pass "bob cannot see the un-approved memory"; fi
if denies 'human|not allowed|denied|permission' as alice memory queue; then
  pass "an agent cannot even read the review queue"
else fail "agent should not read the review queue"; fi
MEM_ID=$(as lead --json memory queue | grep -oP '"id"\s*:\s*"\K[^"]+' | head -1)
as lead memory approve --id "$MEM_ID" --note "correct, verified"
as bob memory search --query parser | grep -q "parser.go" \
  && pass "after human approval, bob can retrieve it" || fail "approved memory not searchable"

step "Agents co-edit a shared artifact" \
     "optimistic concurrency: an edit from a stale base is rejected, not silently lost."
# alice creates the document (base 0 -> version 1).
as alice artifact put --name design --base-version 0 \
  --content "# Design\n\n1. lexer\n2. parser"
# bob edits from the version he last saw (1), appending codegen -> version 2.
as bob artifact put --name design --base-version 1 \
  --content "# Design\n\n1. lexer\n2. parser\n3. codegen"
as lead artifact get --name design | grep -q codegen \
  && pass "the artifact reflects bob's edit on top of alice's (now version 2)" \
  || fail "artifact co-edit failed"
# A stale write (base 1, but the doc is at 2) must be refused — the guarantee.
if denies 'version conflict' as alice artifact put --name design --base-version 1 --content "# Design\n\nclobbered"; then
  pass "a stale edit (base 1 vs current 2) was rejected — no lost update"
else fail "stale write should have been rejected"; fi

step "The human ejects a misbehaving agent" \
     "lead kicks bob; bob's inbox access stops and its undelivered mail is purged."
as lead mod kick --target bob
if denies 'not a member|member|denied|unknown|not found' as bob inbox; then
  pass "bob can no longer read its inbox — it is out of the room"
else fail "kicked agent should lose access"; fi
as lead presence | grep -q bob && fail "bob should be gone from presence" \
  || pass "bob is gone from presence; alice remains"

step "The owner reviews the whole conversation" \
     "message_history is human-only and non-consuming — the audit trail."
if as lead history | grep -q "take the lexer"; then
  pass "lead can read back the full room history"
else fail "history read failed"; fi

echo
if [ "$FAILED" -eq 0 ]; then
  bold "All acceptance steps passed."
  echo "The same story across two machines and two vendors: docs/launch-demo.md."
else
  bold "Some steps FAILED — see above."; exit 1
fi
