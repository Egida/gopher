#!/usr/bin/env bash
# push.sh — build the gopher binary and push it to a remote box over SSH,
# optionally installing it (fresh) or hot-swapping it (upgrade/cutover).
#
# Usage:
#   scripts/push.sh --host user@host [--key ~/.ssh/id_xxx] [--arch amd64|arm64] \
#                   [--binary ./gopher] [--install | --upgrade]
#
# Modes:
#   (default)   copy the binary to remote:/tmp/gopher and stop
#   --install   fresh install on a clean box:  sudo /tmp/gopher install
#   --upgrade   existing box / cutover: ensure /opt/gopher/bin + GOPHER_MANAGED,
#               hot-swap /opt/gopher/gopher, restart (triggers the legacy->/etc/gopher
#               migration on first managed boot)
#
# Assumes the SSH user is root or has passwordless sudo (the gopher norm).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

HOST="" KEY="" ARCH="" BINARY="" MODE="copy"
while [ $# -gt 0 ]; do
  case "$1" in
    --host)    HOST="$2"; shift 2 ;;
    --key)     KEY="$2"; shift 2 ;;
    --arch)    ARCH="$2"; shift 2 ;;
    --binary)  BINARY="$2"; shift 2 ;;
    --install) MODE="install"; shift ;;
    --upgrade) MODE="upgrade"; shift ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

[ -n "$HOST" ] || { echo "ERROR: --host user@host is required" >&2; exit 1; }

SSH_OPTS=(-o StrictHostKeyChecking=accept-new)
[ -n "$KEY" ] && SSH_OPTS+=(-i "$KEY")

# ── Build (unless a prebuilt binary was supplied) ───────────────────────────
if [ -n "$BINARY" ]; then
  [ -f "$BINARY" ] || { echo "ERROR: --binary $BINARY not found" >&2; exit 1; }
else
  HOST_ARCH="$(go env GOARCH)"
  TARGET_ARCH="${ARCH:-$HOST_ARCH}"
  case "$TARGET_ARCH" in
    amd64|arm64) ;;
    *) echo "ERROR: --arch must be amd64 or arm64 (edges are 64-bit)" >&2; exit 1 ;;
  esac

  echo "→ Building (frontend + agents + host deps)..."
  bash "$ROOT/scripts/build.sh"
  if [ "$TARGET_ARCH" != "$HOST_ARCH" ]; then
    echo "→ Cross-building gopher for linux/$TARGET_ARCH..."
    GOARCH="$TARGET_ARCH" bash "$ROOT/scripts/fetch-deps.sh"
    VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
    ( cd "$ROOT" && GOOS=linux GOARCH="$TARGET_ARCH" go build \
        -ldflags "-X github.com/smalex-z/gopher/internal/build.Version=${VERSION}" \
        -o gopher ./cmd/server/... )
  fi
  BINARY="$ROOT/gopher"
fi

echo "→ Pushing $BINARY ($(du -h "$BINARY" | cut -f1)) to $HOST:/tmp/gopher ..."
scp "${SSH_OPTS[@]}" "$BINARY" "$HOST:/tmp/gopher"

case "$MODE" in
  copy)
    echo "✓ Copied to $HOST:/tmp/gopher"
    echo "  Fresh install:  ssh $HOST 'sudo /tmp/gopher install'"
    echo "  Or re-run with --install / --upgrade."
    ;;
  install)
    echo "→ Fresh install on $HOST..."
    ssh "${SSH_OPTS[@]}" "$HOST" "sudo /tmp/gopher install"
    echo "✓ Installed. Dashboard: http://<host-ip>:4321 — finish the setup wizard."
    ;;
  upgrade)
    echo "→ Hot-swapping + ensuring embed bits on $HOST..."
    ssh "${SSH_OPTS[@]}" "$HOST" 'bash -s' <<'REMOTE'
set -e
sudo install -d -o gopher -g gopher -m 0755 /opt/gopher/bin
UNIT=/etc/systemd/system/gopher.service
if [ -f "$UNIT" ] && ! sudo grep -q GOPHER_MANAGED=1 "$UNIT"; then
  sudo sed -i '/^\[Service\]/a Environment=GOPHER_MANAGED=1' "$UNIT"
  echo "  added GOPHER_MANAGED=1 to the unit"
fi
sudo systemctl daemon-reload
sudo systemctl stop gopher
sudo cp /tmp/gopher /opt/gopher/gopher
sudo systemctl start gopher
sleep 2
sudo systemctl --no-pager status gopher | head -4
REMOTE
    echo "✓ Upgraded + restarted. On a legacy edge this triggers the migration —"
    echo "  watch it:  ssh $HOST 'journalctl -u gopher -f'"
    ;;
esac
