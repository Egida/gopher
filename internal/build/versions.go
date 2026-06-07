package build

import "strings"

// Pinned versions of the external binaries Gopher installs. These are the
// single source of truth — install code (Go) and rendered install scripts both
// read from here. Bump them ONLY in a release, after testing the newer version,
// so fresh installs are reproducible and never surprised by an upstream breaking
// release.
const (
	// CaddyVersion is the apt/yum package version installed on the edge
	// (e.g. "apt-get install caddy=<CaddyVersion>"). Must exist in the Caddy
	// cloudsmith repo.
	CaddyVersion = "2.10.2"

	// RatholeVersion is the rathole release tag downloaded for the edge and
	// origins. The deployed build must carry the noise + notify features Gopher
	// relies on; do not move off a tested tag casually.
	RatholeVersion = "v0.5.0"

	// RatholeRepo is the GitHub repo rathole releases are fetched from.
	RatholeRepo = "rathole-org/rathole"
)

// InjectVersions substitutes the pinned-version placeholder tokens in an install
// script with their concrete values. Keeps shell scripts free of hardcoded
// versions so the constants above remain the single source of truth.
func InjectVersions(script string) string {
	return placeholderReplacer.Replace(script)
}

var placeholderReplacer = strings.NewReplacer(
	"__RATHOLE_VERSION__", RatholeVersion,
	"__RATHOLE_REPO__", RatholeRepo,
	"__CADDY_VERSION__", CaddyVersion,
)
