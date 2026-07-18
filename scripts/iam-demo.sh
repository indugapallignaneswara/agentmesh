#!/usr/bin/env bash
#
# iam-demo.sh — end-to-end proof that Agent-IAM closes the loop with an
# UNCHANGED AgentMesh resource server:
#
#   register an agent client → exchange client_id/client_secret for a
#   short-lived RS256 JWT (client_credentials, RFC 8707 resource binding) →
#   AgentMesh in AGENTMESH_AUTH=oauth mode ACCEPTS that token on an MCP tool
#   call, and REJECTS a bogus one.
#
# Requires: a Go toolchain, openssl, curl, and a reachable Postgres (client
# registration needs persistence, and AGENTMESH_AUTH=oauth requires the
# postgres store). If Postgres is not reachable the script SKIPS cleanly with
# exit 0 — it does not fail.
#
#   ./scripts/iam-demo.sh              # pauses between steps (good for recording)
#   NO_PAUSE=1 ./scripts/iam-demo.sh   # run straight through (CI/self-check)
#
# Postgres defaults to the throwaway test database; override with PG_URL:
#   PG_URL='postgres://user:pw@host:port/db?sslmode=disable' ./scripts/iam-demo.sh

set -euo pipefail
cd "$(dirname "$0")/.."

PG_HOST="${PG_HOST:-127.0.0.1}"
PG_PORT="${PG_PORT:-5600}"
PG_URL="${PG_URL:-postgres://agentmesh@${PG_HOST}:${PG_PORT}/agentmesh_test?sslmode=disable}"

WS="team"
MEMBER="deployer"
BIN_DIR="$(mktemp -d)"
IAM_LOG="$(mktemp)"
MESH_LOG="$(mktemp)"
IAM_PID=""
MESH_PID=""

cleanup() {
  [ -n "$MESH_PID" ] && kill "$MESH_PID" 2>/dev/null || true
  [ -n "$IAM_PID" ] && kill "$IAM_PID" 2>/dev/null || true
  rm -rf "$BIN_DIR" "$IAM_LOG" "$MESH_LOG"
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

# port_free <port> — true if nothing is listening on 127.0.0.1:<port>.
port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# pick_port <preferred> — echo the preferred port, or the next free one.
pick_port() {
  local p="$1"
  while ! port_free "$p"; do p=$((p + 1)); done
  echo "$p"
}

# wait_http <url> <logfile> <name> — poll until the URL answers, or dump the
# log and return non-zero.
wait_http() {
  local url="$1" log="$2" name="$3"
  for _ in $(seq 1 50); do
    if curl -fsS "$url" >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  echo "$name did not start:"; cat "$log"; return 1
}

# ---------------------------------------------------------------------------
# Preflight: Postgres. Registration needs persistence and oauth mode needs the
# postgres store, so without it there is nothing honest to prove — skip.

if ! (exec 3<>"/dev/tcp/${PG_HOST}/${PG_PORT}") 2>/dev/null; then
  bold "SKIPPED (no Postgres): nothing listening at ${PG_HOST}:${PG_PORT}."
  echo "Start the test database (e.g. 'make up', or point PG_HOST/PG_PORT/PG_URL"
  echo "at a reachable Postgres) and re-run."
  exit 0
fi

IAM_PORT="$(pick_port "${IAM_PORT:-8091}")"
MESH_PORT="$(pick_port "${MESH_PORT:-8082}")"
IAM_URL="http://127.0.0.1:${IAM_PORT}"
MESH_URL="http://127.0.0.1:${MESH_PORT}"

echo "==> Building agentiam + agentmesh + coord"
go build -o "$BIN_DIR/agentiam" ./cmd/agentiam
go build -o "$BIN_DIR/agentmesh" ./cmd/agentmesh
go build -o "$BIN_DIR/coord" ./cmd/coord

# ---------------------------------------------------------------------------

step "Generate a signing key and start Agent-IAM on :${IAM_PORT}" \
     "RSA-2048 PEM (ephemeral, temp dir); issuer is the server's own URL."
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
  -out "$BIN_DIR/signing-key.pem" 2>/dev/null

AGENTIAM_ISSUER="$IAM_URL" \
AGENTIAM_HTTP_ADDR="127.0.0.1:${IAM_PORT}" \
AGENTIAM_SIGNING_KEY="$BIN_DIR/signing-key.pem" \
AGENTIAM_TOKEN_TTL=15m \
AGENTIAM_DATABASE_URL="$PG_URL" \
  "$BIN_DIR/agentiam" serve >"$IAM_LOG" 2>&1 &
IAM_PID=$!

if ! wait_http "$IAM_URL/healthz" "$IAM_LOG" "agentiam"; then
  if grep -qiE 'connect(ion)? (refused|failed)|no such host|dial|timeout' "$IAM_LOG"; then
    bold "SKIPPED (no Postgres): agentiam could not reach ${PG_URL%%\?*}."
    exit 0
  fi
  exit 1
fi
curl -fsS "$IAM_URL/.well-known/jwks.json" | grep -q '"keys"' \
  && pass "agentiam is up; JWKS published at ${IAM_URL}/.well-known/jwks.json" \
  || fail "JWKS endpoint did not answer"

step "Register an agent client (workspace=${WS}, member=${MEMBER})" \
     "The secret is printed once; only its hash is stored."
REG_OUT="$(AGENTIAM_DATABASE_URL="$PG_URL" "$BIN_DIR/agentiam" client register \
  --workspace "$WS" --member "$MEMBER" --kind agent 2>/dev/null)"
CLIENT_ID="$(printf '%s\n' "$REG_OUT" | grep -oP 'client_id:\s*\K\S+')"
CLIENT_SECRET="$(printf '%s\n' "$REG_OUT" | grep -oP 'client_secret:\s*\K\S+')"
[ -n "$CLIENT_ID" ] && [ -n "$CLIENT_SECRET" ] \
  && pass "registered ${CLIENT_ID} for ${WS}/${MEMBER}" \
  || { fail "client registration did not print credentials"; exit 1; }

step "Exchange the client credentials for a short-lived token" \
     "POST /token, grant_type=client_credentials, resource=${MESH_URL} (RFC 8707)."
TOKEN_JSON="$(curl -fsS "$IAM_URL/token" \
  -d grant_type=client_credentials \
  -d "client_id=${CLIENT_ID}" \
  -d "client_secret=${CLIENT_SECRET}" \
  --data-urlencode "resource=${MESH_URL}")"
ACCESS_TOKEN="$(printf '%s\n' "$TOKEN_JSON" | grep -oP '"access_token"\s*:\s*"\K[^"]+')"
[ -n "$ACCESS_TOKEN" ] \
  && pass "got an access token ($(printf '%s' "$TOKEN_JSON" | grep -oP '"expires_in"\s*:\s*\K[0-9]+')s TTL)" \
  || { fail "no access_token in response: $TOKEN_JSON"; exit 1; }

step "Start AgentMesh in oauth mode, trusting only Agent-IAM" \
     "AGENTMESH_AUTH=oauth; issuer/audience/JWKS point at agentiam on :${IAM_PORT}."
AGENTMESH_STORE=postgres \
AGENTMESH_DATABASE_URL="$PG_URL" \
AGENTMESH_AUTH=oauth \
AGENTMESH_OAUTH_ISSUER="$IAM_URL" \
AGENTMESH_OAUTH_AUDIENCE="$MESH_URL" \
AGENTMESH_OAUTH_JWKS_URL="$IAM_URL/.well-known/jwks.json" \
AGENTMESH_HTTP_ADDR="127.0.0.1:${MESH_PORT}" \
AGENTMESH_RATE_LIMIT=true \
  "$BIN_DIR/agentmesh" >"$MESH_LOG" 2>&1 &
MESH_PID=$!
wait_http "$MESH_URL/healthz" "$MESH_LOG" "agentmesh" || exit 1
pass "agentmesh is up on ${MESH_URL}"

export AGENTMESH_ENDPOINT="${MESH_URL}/mcp"
export AGENTMESH_WORKSPACE="$WS"
export AGENTMESH_MEMBER="$MEMBER"

step "The agent calls MCP tools with the Agent-IAM token" \
     "join + presence via coord --token <jwt>: the RS validates sig, iss, aud, exp."
if "$BIN_DIR/coord" --token "$ACCESS_TOKEN" join --kind agent --name "$MEMBER" >/dev/null 2>&1; then
  pass "join ACCEPTED with the Agent-IAM token"
else
  fail "join with a valid Agent-IAM token was rejected"
fi
if "$BIN_DIR/coord" --token "$ACCESS_TOKEN" presence 2>/dev/null | grep -q "$MEMBER"; then
  pass "presence ACCEPTED and shows ${WS}/${MEMBER}"
else
  fail "presence did not show ${MEMBER}"
fi

step "A bogus token is rejected" \
     "Same call, garbage bearer — the RS must refuse it."
BOGUS_OUT="$("$BIN_DIR/coord" --token "bogus.$(date +%s).token" presence 2>&1 || true)"
if printf '%s\n' "$BOGUS_OUT" | grep -qiE 'unauthorized|invalid|401|denied|token'; then
  pass "bogus token REJECTED"
else
  fail "bogus token was not rejected: $BOGUS_OUT"
fi

echo
if [ "$FAILED" -eq 0 ]; then
  bold "Agent-IAM end-to-end proof PASSED."
  echo "Registered client → client_credentials → RS256 JWT → AgentMesh accepted it"
  echo "audience-bound, and refused garbage. Zero changes to the resource server."
else
  bold "Some steps FAILED — see above."
  echo "--- agentiam log ---"; tail -20 "$IAM_LOG"
  echo "--- agentmesh log ---"; tail -20 "$MESH_LOG"
  exit 1
fi
