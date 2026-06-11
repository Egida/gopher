package embedbin

import (
	"embed"

	"github.com/smalex-z/gopher/internal/build"
)

// Bundled binaries are embedded from internal/embedbin/bin/, which
// scripts/fetch-deps.sh populates before a release/deploy `go build` — the same
// shape as the frontend (vite builds frontend/dist, then go build embeds it) and
// the agents (cmd/server/agents.go). A committed bin/.gitkeep keeps the
// `//go:embed all:bin` directive from failing on a fresh checkout that hasn't
// fetched the binaries yet. When only .gitkeep is present (ordinary dev builds),
// Embedded() reports false and callers fall back to running caddy/rathole from
// PATH. No build tag — presence of the binaries, not a tag, decides.
//
//go:embed all:bin
var binFS embed.FS

func read(name string) ([]byte, bool) {
	data, err := binFS.ReadFile("bin/" + name)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Embedded reports whether real binaries were compiled in (vs. just .gitkeep).
func Embedded() bool {
	_, ok := read("caddy")
	return ok
}

// RunBundle returns the binaries the edge extracts and runs: caddy (this build's
// arch) and the edge-arch rathole used for rathole-server. Empty when the
// binaries weren't fetched at build time.
func RunBundle() []Binary {
	var bins []Binary
	if d, ok := read("caddy"); ok {
		bins = append(bins, Binary{Name: "caddy", Version: build.CaddyVersion, Data: d})
	}
	if d, ok := read("rathole-" + build.EdgeRatholeTarget().Name); ok {
		bins = append(bins, Binary{Name: "rathole", Version: build.RatholeVersion, Data: d})
	}
	return bins
}

// RatholeForOrigin returns the rathole binary to serve to an origin reporting
// the given `uname -m`, replacing the per-origin download from GitHub.
func RatholeForOrigin(uname string) ([]byte, bool) {
	t, ok := build.RatholeTargetForUname(uname)
	if !ok {
		return nil, false
	}
	return read("rathole-" + t.Name)
}
