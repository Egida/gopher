// Package paths is the single source of truth for where Gopher and the
// binaries and config it owns live on the edge.
//
// The v0.1.0 consolidation collapses the legacy scatter — Caddy as an apt
// package under /etc/caddy + /var/lib/caddy, rathole downloaded to
// /usr/local/bin with config in /etc/rathole, three separate systemd services —
// into a single self-contained layout that Gopher owns end to end:
//
//	/opt/gopher/            gopher binary + extracted child binaries
//	/opt/gopher/bin/        caddy, rathole (extracted from the embedded bundle)
//	/etc/gopher/            all generated config (caddy/, rathole/)
//	/var/lib/gopher/        all state (gopher.db, caddy/ cert+data storage)
//
// Everything lives under two trees — /etc/gopher and /var/lib/gopher — so
// backup is "tar two dirs" and uninstall is "stop one service, rm two trees".
package paths

const (
	// Root is the install dir for the gopher binary and the bin/ subdir that
	// holds the child binaries extracted from the embedded bundle at startup.
	Root   = "/opt/gopher"
	BinDir = Root + "/bin"

	// ConfigDir holds every config file Gopher generates. Children are pointed
	// here explicitly (caddy --config, rathole <path>) rather than at their
	// upstream defaults.
	ConfigDir = "/etc/gopher"

	// StateDir holds all persistent state: the sqlite DB and Caddy's
	// certificate/data storage (via XDG_DATA_HOME=CaddyData when we spawn it).
	StateDir = "/var/lib/gopher"
)

// Extracted child binaries.
const (
	CaddyBin   = BinDir + "/caddy"
	RatholeBin = BinDir + "/rathole"
)

// Generated config files and dirs.
const (
	CaddyfilePath = ConfigDir + "/caddy/Caddyfile"
	CaddyConfDir  = ConfigDir + "/caddy/conf.d"
	RatholeConfig = ConfigDir + "/rathole/server.toml"
)

// Persistent state.
const (
	DBPath = StateDir + "/gopher.db"
	// CaddyData is set as Caddy's XDG_DATA_HOME so its ACME certs and data live
	// under the gopher state tree instead of /var/lib/caddy.
	CaddyData = StateDir + "/caddy"
)

// Legacy locations from the pre-consolidation layout, kept so the migration
// step can detect and move an existing install. Nothing should write to these.
const (
	LegacyRatholeConfig = "/etc/rathole/server.toml"
	LegacyRatholeBin    = "/usr/local/bin/rathole"
	LegacyCaddyfile     = "/etc/caddy/Caddyfile"
	LegacyCaddyConfDir  = "/etc/caddy/conf.d"
	LegacyCaddyData     = "/var/lib/caddy"
)
