#!/usr/bin/env bash
# Fetches the external binaries Gopher embeds, into internal/embedbin/bin/
# (gitignored). MUST run before `go build -tags embedbins`. Versions are read
# from internal/build/versions.go so this stays in sync with the Go source of
# truth.
#
#   Caddy   — matching edge arch only (amd64 OR arm64), since only the edge runs it.
#   rathole — every origin arch (x86_64, aarch64, armv7): the edge runs its own
#             and serves the rest to origins.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$REPO_ROOT/internal/embedbin/bin"
VERS="$REPO_ROOT/internal/build/versions.go"

ver() { grep -E "$1 *=" "$VERS" | grep -oE '"[^"]+"' | head -1 | tr -d '"'; }
CADDY_VERSION="$(ver CaddyVersion)"
RATHOLE_VERSION="$(ver RatholeVersion)"
RATHOLE_REPO="$(ver RatholeRepo)"
GOARCH="${GOARCH:-$(go env GOARCH)}"

mkdir -p "$BIN"

case "$GOARCH" in
  amd64) CADDY_ARCH=amd64 ;;
  arm64) CADDY_ARCH=arm64 ;;
  *) echo "fetch-deps: unsupported edge GOARCH=$GOARCH (edges are amd64 or arm64)" >&2; exit 1 ;;
esac

echo "Fetching caddy ${CADDY_VERSION} (linux/${CADDY_ARCH})..."
curl -fsSL "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${CADDY_ARCH}.tar.gz" \
  | tar -xz -C "$BIN" caddy
chmod +x "$BIN/caddy"

fetch_rathole() {
  local tag="$1" out="$2" tmp
  echo "Fetching rathole ${RATHOLE_VERSION} (${tag})..."
  tmp="$(mktemp -d)"
  curl -fsSL "https://github.com/${RATHOLE_REPO}/releases/download/${RATHOLE_VERSION}/rathole-${tag}.zip" -o "$tmp/r.zip"
  unzip -oq "$tmp/r.zip" -d "$tmp"
  mv "$tmp/rathole" "$BIN/$out"
  chmod +x "$BIN/$out"
  rm -rf "$tmp"
}
fetch_rathole "x86_64-unknown-linux-gnu"       "rathole-x86_64"
fetch_rathole "aarch64-unknown-linux-musl"     "rathole-aarch64"
fetch_rathole "armv7-unknown-linux-musleabihf" "rathole-armv7"

echo "Done. Embedded binaries staged in $BIN"
