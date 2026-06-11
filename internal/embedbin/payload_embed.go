//go:build embedbins

package embedbin

import (
	_ "embed"

	"github.com/smalex-z/gopher/internal/build"
)

// Real bundled binaries, compiled in only under the `embedbins` tag.
// scripts/fetch-deps.sh populates internal/embedbin/bin/ before the build:
// caddy is the edge's own arch (fetch-deps picks it by GOARCH); rathole is
// fetched for every origin arch because the edge serves them all.

//go:embed bin/caddy
var caddyBin []byte

//go:embed bin/rathole-x86_64
var ratholeX86 []byte

//go:embed bin/rathole-aarch64
var ratholeAarch64 []byte

//go:embed bin/rathole-armv7
var ratholeArmv7 []byte

// Embedded reports whether real binaries were compiled in.
func Embedded() bool { return true }

// RunBundle returns the binaries the edge extracts and runs: caddy (this build's
// arch) and the edge-arch rathole used for rathole-server.
func RunBundle() []Binary {
	return []Binary{
		{Name: "caddy", Version: build.CaddyVersion, Data: caddyBin},
		{Name: "rathole", Version: build.RatholeVersion, Data: edgeRathole()},
	}
}

// edgeRathole returns the rathole binary matching the edge's own architecture.
func edgeRathole() []byte {
	if build.EdgeRatholeTarget().Name == "aarch64" {
		return ratholeAarch64
	}
	return ratholeX86
}

// RatholeForOrigin returns the rathole binary to serve to an origin reporting
// the given `uname -m`, replacing the per-origin download from GitHub.
func RatholeForOrigin(uname string) ([]byte, bool) {
	switch uname {
	case "x86_64":
		return ratholeX86, true
	case "aarch64":
		return ratholeAarch64, true
	case "armv7l":
		return ratholeArmv7, true
	}
	return nil, false
}
