#!/bin/sh
# AgentMesh installer. Downloads the latest release's binaries for your OS/arch
# and installs them to a directory on your PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/indugapallignaneswara/agentmesh/main/install.sh | sh
#
# Overrides:
#   AGENTMESH_VERSION   pin a version (default: latest)
#   AGENTMESH_BINDIR    install location (default: /usr/local/bin, or ~/.local/bin if not writable)
set -eu

REPO="indugapallignaneswara/agentmesh"

err() { echo "install: $*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) err "unsupported architecture: $arch" ;;
esac
case "$os" in
  linux|darwin) ;;
  *) err "unsupported OS: $os (use the release archive directly on Windows)" ;;
esac

version="${AGENTMESH_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name":[[:space:]]*"[^"]*"' | head -1 | cut -d'"' -f4)
  [ -n "$version" ] || err "could not determine the latest version (is there a release yet?)"
fi

bindir="${AGENTMESH_BINDIR:-/usr/local/bin}"
if [ ! -w "$bindir" ] 2>/dev/null; then
  bindir="$HOME/.local/bin"
  mkdir -p "$bindir"
  case ":$PATH:" in
    *":$bindir:"*) ;;
    *) echo "install: note: add $bindir to your PATH" >&2 ;;
  esac
fi

num="${version#v}"
url="https://github.com/${REPO}/releases/download/${version}/agentmesh_${num}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "install: downloading agentmesh ${version} (${os}/${arch})"
curl -fsSL "$url" -o "$tmp/am.tar.gz" || err "download failed: $url"
tar -xzf "$tmp/am.tar.gz" -C "$tmp"

for bin in agentmesh coord; do
  [ -f "$tmp/$bin" ] || err "archive missing $bin"
  install -m 0755 "$tmp/$bin" "$bindir/$bin"
done

echo "install: installed agentmesh and coord to $bindir"
echo
echo "  Try it (zero setup, in-memory, no auth — demo only):"
echo "    AGENTMESH_STORE=memory agentmesh"
echo
echo "  Before exposing a node, read the production checklist:"
echo "    https://github.com/${REPO}/blob/main/docs/operations.md"
