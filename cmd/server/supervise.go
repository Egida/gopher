package main

import (
	"context"
	"log"
	"os"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/embedbin"
	"github.com/smalex-z/gopher/internal/paths"
	"github.com/smalex-z/gopher/internal/supervisor"
)

// startBundledChildren extracts the embedded caddy/rathole to paths.BinDir and
// brings them up under a supervisor that gopher owns. It returns (nil, nil) —
// meaning "not supervising" — in the cases where caddy/rathole are still managed
// externally (systemd), so the caller treats a nil supervisor as a no-op.
//
// Two interlocks keep this safe to deploy before the rest of the migration
// lands:
//
//  1. !Embedded(): a dev/legacy build (no fetched binaries) never supervises —
//     behaviour is identical to today, caddy/rathole stay under systemd.
//  2. No consolidated config yet: even an embedded build won't start its own
//     caddy/rathole until /etc/gopher has been populated by migration. Until
//     then the legacy systemd-managed caddy.service/rathole-server.service are
//     serving, and starting our own children would fight them for ports 80/443
//     and the rathole control port.
//
// PREREQUISITE for the switch (leg 4): gopher.service must run with
// AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN — gopher runs as the
// unprivileged `gopher` user, and the supervised caddy inherits gopher's ambient
// capability set; without it caddy cannot bind 80/443. rathole binds only high
// ports and needs no caps. Migration must also stop+disable the legacy
// caddy.service/rathole-server.service and preserve data (the DB stays in
// /var/lib/gopher; Caddy's certs move from /var/lib/caddy to /var/lib/gopher/caddy
// so they are NOT re-issued).
func startBundledChildren() (*supervisor.Supervisor, error) {
	if !embedbin.Embedded() {
		return nil, nil
	}
	if _, err := os.Stat(paths.RatholeConfig); err != nil {
		log.Printf("supervisor: %s not present yet — leaving caddy/rathole to their current manager", paths.RatholeConfig)
		return nil, nil
	}

	if err := embedbin.ExtractAll(paths.BinDir, embedbin.RunBundle()); err != nil {
		return nil, err
	}

	sup := supervisor.New(os.Stdout,
		supervisor.Spec{
			Name: "caddy",
			Path: paths.CaddyBin,
			Args: []string{"run", "--config", paths.CaddyfilePath, "--adapter", "caddyfile"},
			// Caddy stores its ACME certs/data under $XDG_DATA_HOME/caddy, so this
			// keeps them in /var/lib/gopher/caddy (= paths.CaddyData).
			Env: []string{"XDG_DATA_HOME=" + paths.StateDir},
		},
		supervisor.Spec{
			Name: "rathole",
			Path: paths.RatholeBin,
			// rathole auto-detects server mode from the [server] config block.
			Args: []string{paths.RatholeConfig},
		},
	)
	log.Printf("supervisor: starting bundled caddy %s + rathole %s as children", build.CaddyVersion, build.RatholeVersion)
	sup.Start(context.Background())
	return sup, nil
}
