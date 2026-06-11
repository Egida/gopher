package service

import (
	"fmt"
	"io"
	"os"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/paths"
)

// MigrateEdgeLayout moves a legacy edge install (apt Caddy under /etc/caddy +
// /var/lib/caddy, rathole under /etc/rathole with its own systemd unit) onto the
// consolidated /etc/gopher + /var/lib/gopher layout the embedded, supervised
// gopher expects. It is idempotent and deliberately data-preserving:
//
//   - The DB stays in /var/lib/gopher and is never touched here.
//   - Caddy's ACME certs are copied /var/lib/caddy -> /var/lib/gopher/caddy so
//     they are NOT re-issued (Let's Encrypt rate limits).
//   - The legacy server.toml + Caddyfile are copied over so their user-owned
//     custom blocks survive; gopher regenerates the managed parts (with the new
//     /etc/gopher import paths) from the DB on the reconcile that follows.
//   - Legacy caddy.service + rathole-server.service are stopped + disabled so
//     they release 80/443 + the rathole control port for the supervised children
//     and don't auto-start on boot.
//
// It COPIES rather than moves config/certs and never deletes the legacy trees,
// so a failed cutover can be rolled back by reverting to the old binary. Returns
// migrated=false when there is nothing to do (new layout already present, or a
// fresh install with no legacy files — the install flow creates /etc/gopher
// directly in that case).
//
// NOT YET VERIFIED END-TO-END. Must be exercised against a real legacy install
// on a throwaway VPS — confirming certs survive (no re-issue) and existing
// clients reconnect — before this path runs on a production edge.
func MigrateEdgeLayout(w io.Writer) (migrated bool, err error) {
	// Gate on a dedicated marker, NOT on server.toml — the boot reconcile creates
	// server.toml, so keying off it would make us skip the cert-move + service
	// stop (the bug the first cutover hit).
	marker := paths.StateDir + "/.edge-migrated"
	if _, statErr := os.Stat(marker); statErr == nil {
		return false, nil
	}
	// Nothing legacy to migrate => fresh install; the install flow builds
	// /etc/gopher directly. Mark it handled so we don't re-check every boot.
	_, ratholeErr := os.Stat(paths.LegacyRatholeConfig)
	_, caddyErr := os.Stat(paths.LegacyCaddyfile)
	if ratholeErr != nil && caddyErr != nil {
		_ = os.WriteFile(marker, []byte("fresh\n"), 0o644)
		return false, nil
	}

	fmt.Fprintf(w, "Migrating edge to the consolidated %s layout...\n", paths.ConfigDir)

	// 1. Stop + disable the legacy services so they release their ports and
	//    don't auto-start. Best-effort — a unit that isn't installed is fine.
	for _, unit := range []string{"caddy", "rathole-server"} {
		_ = runLocalCmd(w, "sudo", "-n", "systemctl", "disable", "--now", unit)
	}

	// 2. Create the new trees.
	for _, dir := range []string{paths.ConfigDir + "/rathole", paths.CaddyConfDir, paths.CaddyData} {
		if mkErr := sudoMkdir(dir); mkErr != nil {
			return false, fmt.Errorf("migrate: mkdir %s: %w", dir, mkErr)
		}
	}

	// 3. Copy Caddy's certs/data — the irreplaceable part. `cp -a` preserves
	//    perms/symlinks; the trailing /. copies contents into the existing dir.
	if _, e := os.Stat(paths.LegacyCaddyData); e == nil {
		if cpErr := runLocalCmd(w, "sudo", "-n", "cp", "-a", paths.LegacyCaddyData+"/.", paths.CaddyData+"/"); cpErr != nil {
			return false, fmt.Errorf("migrate: copy caddy data: %w", cpErr)
		}
		fmt.Fprintf(w, "  copied Caddy certs %s -> %s\n", paths.LegacyCaddyData, paths.CaddyData)
	}

	// 4. Copy legacy config so user custom blocks survive. The managed parts get
	//    regenerated (with /etc/gopher import paths) by the reconcile that runs
	//    after migration.
	if _, e := os.Stat(paths.LegacyRatholeConfig); e == nil {
		_ = runLocalCmd(w, "sudo", "-n", "cp", paths.LegacyRatholeConfig, paths.RatholeConfig)
	}
	if _, e := os.Stat(paths.LegacyCaddyfile); e == nil {
		_ = runLocalCmd(w, "sudo", "-n", "cp", paths.LegacyCaddyfile, paths.CaddyfilePath)
	}
	if _, e := os.Stat(paths.LegacyCaddyConfDir); e == nil {
		_ = runLocalCmd(w, "sudo", "-n", "sh", "-c",
			fmt.Sprintf("cp %s/*.caddy %s/ 2>/dev/null || true", paths.LegacyCaddyConfDir, paths.CaddyConfDir))
	}

	// 5. Hand the new trees to the gopher user (the supervised caddy/rathole run
	//    as gopher and must read/write them; legacy caddy data was caddy:caddy).
	_ = runLocalCmd(w, "sudo", "-n", "chown", "-R", "gopher:gopher", paths.ConfigDir)
	_ = runLocalCmd(w, "sudo", "-n", "chown", "-R", "gopher:gopher", paths.CaddyData)

	// 6. The copied Caddyfile still imports /etc/caddy/conf.d; rebuild it so the
	//    import points at the new conf.d (paths.CaddyConfDir) while preserving the
	//    user's custom block. The supervised caddy reads this file.
	if existing, e := os.ReadFile(paths.CaddyfilePath); e == nil {
		bindIP := ""
		if s, _ := db.GetSettings(); s != nil {
			bindIP = s.BindIP
		}
		if wErr := writeLocalFile(paths.CaddyfilePath, buildManagedCaddyfile(string(existing), bindIP)); wErr != nil {
			return false, fmt.Errorf("migrate: rewrite Caddyfile import path: %w", wErr)
		}
	}

	// Mark migration done so subsequent boots skip it (idempotent).
	if wErr := os.WriteFile(marker, []byte("migrated\n"), 0o644); wErr != nil {
		fmt.Fprintf(w, "  warning: could not write migration marker %s: %v\n", marker, wErr)
	}

	fmt.Fprintf(w, "Edge layout migration complete (legacy trees left in place for rollback).\n")
	return true, nil
}
