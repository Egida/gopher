package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/embedbin"
	"github.com/smalex-z/gopher/internal/paths"
	"github.com/smalex-z/gopher/internal/service"
	"github.com/smalex-z/gopher/internal/supervisor"
)

// startBundledChildren extracts the embedded caddy/rathole and brings them up
// under a supervisor that gopher owns. It returns (nil, nil) — "not
// supervising" — unless ALL of the following hold, so it's safe to compile in
// and to run tests against a live edge:
//
//  1. GOPHER_MANAGED=1 — set only by the systemd unit (see install.go). `go
//     test`, `go run`, and stray executions don't set it, so they never run the
//     DESTRUCTIVE legacy migration or fight a live edge's services.
//  2. Embedded() — real binaries were compiled in (not a dev/IDE build).
//  3. The consolidated /etc/gopher config exists — created by the migration
//     below or a fresh install; until then there's nothing valid to run, so we
//     leave caddy/rathole to their current manager.
//
// caddy must bind 80/443; gopher runs unprivileged and spawns it as a child, so
// the cap is granted on the extracted binary via setcap — no unit change or
// self-restart needed (requires NoNewPrivileges unset on gopher.service, which
// it is).
func startBundledChildren() (*supervisor.Supervisor, error) {
	if os.Getenv("GOPHER_MANAGED") != "1" || !embedbin.Embedded() {
		return nil, nil
	}

	// Move a legacy install onto the /etc/gopher layout. Idempotent; no-op on a
	// fresh or already-migrated edge.
	if migrated, err := service.MigrateEdgeLayout(os.Stdout); err != nil {
		return nil, fmt.Errorf("edge layout migration: %w", err)
	} else if migrated {
		log.Printf("supervisor: migrated legacy edge layout to %s", paths.ConfigDir)
	}

	if _, err := os.Stat(paths.RatholeConfig); err != nil {
		log.Printf("supervisor: %s not present yet — leaving caddy/rathole to their current manager", paths.RatholeConfig)
		return nil, nil
	}

	if err := embedbin.ExtractAll(paths.BinDir, embedbin.RunBundle()); err != nil {
		return nil, err
	}
	// File capability so the unprivileged, gopher-spawned caddy can bind 80/443.
	// Re-applied each start because extraction (temp + rename) drops file caps.
	if out, err := exec.Command("sudo", "-n", "setcap", "cap_net_bind_service=+ep", paths.CaddyBin).CombinedOutput(); err != nil { // #nosec G204 — fixed args
		log.Printf("supervisor: setcap on %s failed (caddy may not bind 80/443): %v: %s", paths.CaddyBin, err, out)
	}

	sup := supervisor.New(os.Stdout,
		supervisor.Spec{
			Name: "caddy",
			Path: paths.CaddyBin,
			Args: []string{"run", "--config", paths.CaddyfilePath, "--adapter", "caddyfile"},
			// Caddy stores ACME certs/data under $XDG_DATA_HOME/caddy, keeping
			// them in /var/lib/gopher/caddy (= paths.CaddyData).
			Env: []string{"XDG_DATA_HOME=" + paths.StateDir},
		},
		supervisor.Spec{
			Name: "rathole",
			Path: paths.RatholeBin,
			Args: []string{paths.RatholeConfig}, // server mode auto-detected from [server] config
		},
	)
	log.Printf("supervisor: starting bundled caddy %s + rathole %s as children", build.CaddyVersion, build.RatholeVersion)
	sup.Start(context.Background())
	return sup, nil
}
