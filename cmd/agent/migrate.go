package main

import (
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/smalex-z/gopher/internal/paths"
)

// Origin systemd units the migration rewrites in place.
const (
	ratholeClientUnit = "/etc/systemd/system/rathole-client.service"
	gopherAgentUnit   = "/etc/systemd/system/gopher-agent.service"
)

// migrateOriginLayout moves an origin off the legacy pre-consolidation layout
// (/etc/rathole/client.toml, /etc/gopher-agent/config.env) onto the /etc/gopher
// tree, matching the edge. It runs once on first boot after the 0.2.1 upgrade
// and is a no-op on already-migrated origins (and on fresh 0.2.1 bootstraps,
// which write the new paths directly).
//
// The agent runs as the gopher user with NOPASSWD: ALL (see bootstrap.sh), so
// every privileged step shells out through `sudo -n`. The whole routine is
// best-effort: a failure logs and returns rather than crashing the agent, so a
// half-migrated box still serves on whatever paths currently work and the next
// boot retries. Steps are ordered copy → repoint-unit → reload → restart →
// remove-legacy so there is never a window where a unit points at a file that
// does not exist.
func migrateOriginLayout() {
	legacyClient := fileExists(paths.LegacyRatholeClientConfig)
	legacyEnv := fileExists(paths.LegacyAgentConfigEnv)
	unitsReferenceLegacy := unitReferences(ratholeClientUnit, paths.LegacyRatholeClientConfig) ||
		unitReferences(gopherAgentUnit, paths.LegacyAgentConfigEnv)

	if !legacyClient && !legacyEnv && !unitsReferenceLegacy {
		return // already on the consolidated layout (or a fresh 0.2.1 bootstrap)
	}

	// Before touching anything, make sure the rathole config we'd relocate is
	// actually loadable. Relocating means restarting rathole-client, and a
	// restart re-reads the file from disk — so a latent on-disk error that the
	// running rathole survived (it rejected the inotify reload and kept its good
	// in-memory config) would turn fatal on restart and drop every tunnel. If
	// the config is broken, refuse to migrate and leave the machine exactly as
	// it is (up, on its current config); the operator fixes the config and the
	// next boot retries. This is the one failure mode that can take a healthy
	// origin offline, so it gates the whole routine.
	srcConfig := paths.LegacyRatholeClientConfig
	if !legacyClient {
		srcConfig = paths.RatholeClientConfig
	}
	if data, err := os.ReadFile(srcConfig); err == nil { // #nosec G304 — fixed config paths
		if dup, ok := duplicateTomlTable(string(data)); ok {
			log.Printf("origin layout migration: REFUSING to migrate — %s has a duplicate table [%s]; "+
				"rathole would fail to restart on the new path. Fix the duplicate, then reboot the agent to retry.", srcConfig, dup)
			return
		}
	}

	log.Printf("origin layout migration: consolidating config under %s", paths.ConfigDir)

	if err := sudo("mkdir", "-p", paths.RatholeDir, paths.AgentDir); err != nil {
		log.Printf("origin layout migration: mkdir failed, aborting (will retry next boot): %v", err)
		return
	}

	restartRathole := false

	// rathole client.toml: copy to the new path, repoint the unit, then restart
	// rathole-client once so it reads the relocated config (rathole binds the
	// inode named on its argv, so a path change needs a process bounce — a
	// one-time tunnel blip that reconnects on its own).
	if legacyClient && !fileExists(paths.RatholeClientConfig) {
		if err := sudo("cp", "-p", paths.LegacyRatholeClientConfig, paths.RatholeClientConfig); err != nil {
			log.Printf("origin layout migration: copy client.toml failed, aborting: %v", err)
			return
		}
		_ = sudo("chown", "gopher:gopher", paths.RatholeClientConfig)
		_ = sudo("chmod", "0644", paths.RatholeClientConfig)
	}
	if fileExists(paths.LegacyRatholeVPSKey) && !fileExists(paths.RatholeVPSKey) {
		_ = sudo("cp", "-p", paths.LegacyRatholeVPSKey, paths.RatholeVPSKey)
	}
	if unitReferences(ratholeClientUnit, paths.LegacyRatholeClientConfig) {
		if err := sudoSed(ratholeClientUnit, paths.LegacyRatholeClientConfig, paths.RatholeClientConfig); err != nil {
			log.Printf("origin layout migration: repoint rathole-client unit failed, leaving legacy config in place: %v", err)
		} else {
			restartRathole = true
		}
	}

	// gopher-agent config.env: copy to the new path and repoint EnvironmentFile.
	// No restart — the running agent already holds its token; systemd reads the
	// new EnvironmentFile on the next natural restart.
	if legacyEnv && !fileExists(paths.AgentConfigEnv) {
		if err := sudo("cp", "-p", paths.LegacyAgentConfigEnv, paths.AgentConfigEnv); err == nil {
			_ = sudo("chown", "root:gopher", paths.AgentConfigEnv)
			_ = sudo("chmod", "0640", paths.AgentConfigEnv)
		} else {
			log.Printf("origin layout migration: copy config.env failed: %v", err)
		}
	}
	_ = sudoSed(gopherAgentUnit, paths.LegacyAgentConfigEnv, paths.AgentConfigEnv)

	_ = sudo("systemctl", "daemon-reload")

	// ratholeRelocated stays false if we still need rathole to bounce onto the
	// new path but the restart didn't succeed — in that case the legacy config
	// must stay so the running (old-path) rathole keeps serving until a later
	// restart picks up the repointed unit.
	ratholeRelocated := !restartRathole // nothing to relocate ⇒ already correct
	if restartRathole && fileExists(paths.RatholeClientConfig) {
		if err := sudo("systemctl", "restart", "rathole-client.service"); err != nil {
			log.Printf("origin layout migration: restart rathole-client failed (keeping legacy config; retry on next boot): %v", err)
		} else {
			ratholeRelocated = true
			log.Printf("origin layout migration: rathole-client restarted on %s", paths.RatholeClientConfig)
		}
	}

	// Remove legacy files only once the new layout is confirmed live, so a
	// failure above never strands the box with a unit pointing at a deleted file.
	if ratholeRelocated && fileExists(paths.RatholeClientConfig) {
		_ = sudo("rm", "-f", paths.LegacyRatholeClientConfig, paths.LegacyRatholeVPSKey)
		_ = sudo("rmdir", "--ignore-fail-on-non-empty", paths.LegacyRatholeClientDir)
	}
	if fileExists(paths.AgentConfigEnv) && !unitReferences(gopherAgentUnit, paths.LegacyAgentConfigEnv) {
		_ = sudo("rm", "-f", paths.LegacyAgentConfigEnv)
		_ = sudo("rmdir", "--ignore-fail-on-non-empty", paths.LegacyAgentDir)
	}

	log.Printf("origin layout migration: complete")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// duplicateTomlTable scans a TOML document for a `[table]` header declared more
// than once and returns the first offender. This is the exact error rathole
// rejects with "redefinition of table" — the dominant way a client.toml ends up
// latently broken (a service block appended without de-duping). It deliberately
// only looks at single-bracket table headers; `[[array.of.tables]]` may legally
// repeat. Comments and the values inside a table are ignored. Not a full TOML
// parser — just enough to catch the one thing that detonates on a rathole
// restart, with no external dependency in the agent.
func duplicateTomlTable(content string) (string, bool) {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
			continue
		}
		end := strings.Index(trimmed, "]")
		if end <= 1 {
			continue
		}
		name := strings.TrimSpace(trimmed[1:end])
		if _, ok := seen[name]; ok {
			return name, true
		}
		seen[name] = struct{}{}
	}
	return "", false
}

// unitReferences reports whether a systemd unit file mentions a given path
// (e.g. an ExecStart or EnvironmentFile still pointing at a legacy location).
func unitReferences(unitPath, needle string) bool {
	data, err := os.ReadFile(unitPath) // #nosec G304 — fixed unit paths
	if err != nil {
		return false
	}
	return strings.Contains(string(data), needle)
}

func sudo(args ...string) error {
	full := append([]string{"-n"}, args...)
	out, err := exec.Command("sudo", full...).CombinedOutput() // #nosec G204 — fixed argv from callers
	if err != nil {
		return wrapCmdErr(err, out)
	}
	return nil
}

// sudoSed rewrites every occurrence of from→to in a file in place, using `#` as
// the sed delimiter so the slashes in paths don't need escaping.
func sudoSed(file, from, to string) error {
	expr := "s#" + from + "#" + to + "#g"
	return sudo("sed", "-i", expr, file)
}

func wrapCmdErr(err error, out []byte) error {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return err
	}
	return &cmdError{err: err, out: trimmed}
}

type cmdError struct {
	err error
	out string
}

func (e *cmdError) Error() string { return e.err.Error() + ": " + e.out }
