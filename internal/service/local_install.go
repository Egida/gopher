package service

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/embedbin"
	"github.com/smalex-z/gopher/internal/paths"
)

// pkgManager detects the system package manager. Returns "apt", "dnf", or "yum".
// Defaults to "apt" if none of the others are found.
func pkgManager() string {
	if isCommandAvailable("dnf") {
		return "dnf"
	}
	if isCommandAvailable("yum") {
		return "yum"
	}
	return "apt"
}

//go:embed templates/rathole-server.service
var ratholeServerServiceTemplate string

//go:embed templates/rathole-server-initial.toml
var ratholeServerInitialConfig string

//go:embed templates/fail2ban-filter.conf
var fail2banFilterConfig string

//go:embed templates/fail2ban-jail.conf
var fail2banJailConfig string

func buildRatholeServiceUnit(binaryPath string) string {
	unit := ratholeServerServiceTemplate
	unit = strings.ReplaceAll(unit, "{{.BinaryPath}}", binaryPath)
	return unit
}

// hasInstallPermission returns true if local setup can run with current
// privileges. Root always passes. For non-root users, sudo must be available
// (password prompt is acceptable during install).
func hasInstallPermission() bool {
	if os.Getuid() == 0 {
		return true
	}
	// Sudo doesn't need to be passwordless for install — elevation will prompt
	return isCommandAvailable("sudo")
}

// privilegedCmdPrefix returns a "sudo -n" prefix slice when not running as root,
// or nil when already root. The -n flag enforces non-interactive mode so sudo
// won't prompt for passwords and will use the passwordless sudo entry instead.
func privilegedCmdPrefix() []string {
	if os.Getuid() == 0 {
		return nil
	}
	return []string{"sudo", "-n"}
}

// Install runs the full local setup in a background goroutine, streaming logs
// to the shared deploy hub so the frontend WebSocket log viewer can follow along.
//
// Returns ErrOpInProgress if another long-running op (install, firewall
// configure, fail2ban setup) is already streaming through the hub. The
// goroutine releases the op lock when DONE is broadcast.
func (s *LocalSetupService) Install(domain, serverHost string, skipCaddy bool) error {
	if !s.hub.TryAcquireOp() {
		return ErrOpInProgress
	}
	go goSafe("localInstall", func() {
		defer s.hub.ReleaseOp()
		w := &hubWriter{hub: s.hub}
		if err := s.doInstall(domain, serverHost, skipCaddy, w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
			s.hub.Broadcast("\x00ERROR")
			return
		}
		s.hub.Broadcast("\x00DONE")
	})
	return nil
}

// SetupFail2ban installs and configures fail2ban in a background goroutine,
// streaming logs to the deploy hub. Marks Fail2banSetupDone in the DB on success.
//
// Returns ErrOpInProgress if another op is already streaming.
func (s *LocalSetupService) SetupFail2ban() error {
	if !s.hub.TryAcquireOp() {
		return ErrOpInProgress
	}
	go goSafe("setupFail2ban", func() {
		defer s.hub.ReleaseOp()
		w := &hubWriter{hub: s.hub}
		fmt.Fprintln(w, "=== Configuring fail2ban ===")
		if err := installFail2ban(w); err != nil {
			fmt.Fprintf(w, "ERROR: %v\n", err)
			s.hub.Broadcast("\x00ERROR")
			return
		}
		_ = db.MutateSettings(func(s *db.AppSettings) error {
			s.Fail2banSetupDone = true
			return nil
		})
		s.hub.Broadcast("\x00DONE")
	})
	return nil
}

func (s *LocalSetupService) Skip(domain string) error {
	return db.MutateSettings(func(settings *db.AppSettings) error {
		settings.LocalSetupDone = true
		if domain != "" {
			settings.Domain = domain
		}
		return nil
	})
}

func (s *LocalSetupService) doInstall(domain, serverHost string, skipCaddy bool, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Installing Local Services ===")

	// Steps 1-2: caddy + rathole are bundled into the gopher binary and extracted
	// to /opt/gopher/bin by the supervisor at startup — no apt install, no
	// download. (A non-embedded dev build has no binaries to run; it can still
	// write config but you'd run caddy/rathole yourself.)
	if embedbin.Embedded() {
		fmt.Fprintln(logWriter, "Steps 1-2: caddy + rathole are bundled (embedded build) — no system install ✓")
	} else {
		fmt.Fprintln(logWriter, "Steps 1-2: WARNING — non-embedded build; caddy/rathole are not bundled. Config will be written but they won't be supervised.")
	}

	// Step 3: Caddyfile — import-based layout + managed conf.d entries
	if skipCaddy {
		fmt.Fprintln(logWriter, "Step 3: Skipping Caddyfile configuration")
	} else {
		fmt.Fprintln(logWriter, "Step 3: Configuring import-based /etc/caddy/Caddyfile and /etc/caddy/conf.d...")
		existingCaddy := ""
		if data, readErr := os.ReadFile(caddyConfigPath); readErr == nil {
			existingCaddy = string(data)
		}
		bindIP := ""
			if s, sErr := db.GetSettings(); sErr == nil {
				bindIP = s.BindIP
			}
			managedCaddy := buildManagedCaddyfile(existingCaddy, bindIP)
		managedHosts := []string{fmt.Sprintf("router.%s", domain)}
		if tunnels, tunErr := db.GetTunnels(); tunErr == nil {
			for _, tunnel := range tunnels {
				if tunnel.Subdomain != "" {
					managedHosts = append(managedHosts, fmt.Sprintf("%s.%s", tunnel.Subdomain, domain))
				}
			}
		}
		managedCaddy = removeHostsFromCustomSection(managedCaddy, managedHosts)

		if err := sudoMkdir(caddyManagedDir); err != nil {
			return fmt.Errorf("failed to create %s: %w", caddyManagedDir, err)
		}
		if err := writeLocalFile(caddyConfigPath, managedCaddy); err != nil {
			return fmt.Errorf("failed to write %s: %w", caddyConfigPath, err)
		}
		installBindIP := ""
		if installSettings, sErr := db.GetSettings(); sErr == nil {
			installBindIP = installSettings.BindIP
		}
		if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(domain, installBindIP)); err != nil {
			return fmt.Errorf("failed to write router Caddy file: %w", err)
		}
	}

	// Step 4: Rathole server.toml — migrate existing if present, create fresh if not
	fmt.Fprintf(logWriter, "Step 4: Setting up %s...\n", paths.RatholeConfig)
	if err := sudoMkdir(paths.ConfigDir + "/rathole"); err != nil {
		return err
	}
	if _, statErr := os.Stat(paths.RatholeConfig); os.IsNotExist(statErr) {
		if existingConfig := findExistingRatholeConfig(logWriter); existingConfig != "" {
			fmt.Fprintln(logWriter, "  Found existing rathole config, migrating with custom section markers...")
			if err := writeLocalFile(paths.RatholeConfig, migrateRatholeConfig(existingConfig)); err != nil {
				return fmt.Errorf("failed to write rathole config: %w", err)
			}
		} else {
			if err := writeLocalFile(paths.RatholeConfig, ratholeServerInitialConfig); err != nil {
				return fmt.Errorf("failed to write rathole config: %w", err)
			}
		}
	} else {
		fmt.Fprintf(logWriter, "  %s already exists, preserving\n", paths.RatholeConfig)
	}

	// Steps 5-9: no separate systemd units and no apt service enable/reload —
	// gopher.service supervises caddy + rathole as children (see
	// cmd/server/supervise.go). They come up when gopher (re)starts at the end of
	// this install with the config we just wrote.

	// Step 10: fail2ban — install and configure (best-effort; non-fatal)
	fmt.Fprintln(logWriter, "Step 10: Configuring fail2ban...")
	fail2banOK := true
	if err := installFail2ban(logWriter); err != nil {
		fmt.Fprintf(logWriter, "  WARN: fail2ban setup failed (non-fatal): %v\n", err)
		fail2banOK = false
	}

	// Step 11: Persist settings
	var dashboardPrivate bool
	if err := db.MutateSettings(func(settings *db.AppSettings) error {
		if skipCaddy {
			settings.Domain = ""
			settings.ServerHost = serverHost
			settings.DashboardPrivate = false // no Caddy — must keep dashboard port public
		} else {
			settings.Domain = domain
			settings.ServerHost = domain // domain doubles as the rathole host when Caddy is active
			settings.DashboardPrivate = true // Caddy is set up; restrict dashboard port, use router.domain
		}
		settings.LocalSetupDone = true
		settings.Fail2banSetupDone = fail2banOK
		dashboardPrivate = settings.DashboardPrivate
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}
	// Apply dashboard port visibility to firewall now that settings are persisted.
	ApplyDashboardPort(dashboardPrivate)

	fmt.Fprintln(logWriter, "=== Local Setup Complete ===")
	if skipCaddy {
		fmt.Fprintln(logWriter, "Dashboard: local mode (reverse proxy disabled)")
	} else {
		fmt.Fprintf(logWriter, "Dashboard: https://router.%s\n", domain)
	}

	// Embedded build: kick gopher so the supervisor brings up caddy + rathole
	// with the config just written (on a fresh edge the supervisor no-op'd at
	// boot because /etc/gopher didn't exist yet). Delayed so the WebSocket DONE
	// reaches the dashboard before the restart drops the connection — mirrors
	// update.go's self-restart pattern. Requires GOPHER_MANAGED=1 on the unit.
	if embedbin.Embedded() {
		fmt.Fprintln(logWriter, "Restarting gopher so the supervisor starts caddy + rathole...")
		go goSafe("postInstallRestart", func() {
			time.Sleep(2 * time.Second)
			restart := append(append([]string{}, privilegedCmdPrefix()...), "systemctl", "restart", "gopher")
			_ = exec.Command(restart[0], restart[1:]...).Run() // #nosec G204 — fixed args
		})
	}
	return nil
}

func installFail2ban(logWriter io.Writer) error {
	if !isCommandAvailable("fail2ban-client") {
		fmt.Fprintf(logWriter, "  Installing fail2ban via %s...\n", pkgManager())
		sudo := privilegedCmdPrefix()
		var installCmd []string
		switch pkgManager() {
		case "dnf", "yum":
			installCmd = append(sudo, pkgManager(), "install", "-y", "-q", "fail2ban")
		default:
			update := append(sudo, "apt-get", "update", "-qq")
			if err := runLocalCmd(logWriter, update[0], update[1:]...); err != nil {
				return err
			}
			installCmd = append(sudo, "apt-get", "install", "-y", "-qq", "fail2ban")
		}
		if err := runLocalCmd(logWriter, installCmd[0], installCmd[1:]...); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(logWriter, "  fail2ban already installed ✓")
	}

	if err := writeLocalFile("/etc/fail2ban/filter.d/gopher-auth.conf", fail2banFilterConfig); err != nil {
		return fmt.Errorf("failed to write fail2ban filter: %w", err)
	}
	if err := writeLocalFile("/etc/fail2ban/jail.d/gopher.conf", fail2banJailConfig); err != nil {
		return fmt.Errorf("failed to write fail2ban jail: %w", err)
	}

	sudo := privilegedCmdPrefix()
	enableCmd := append(sudo, "systemctl", "enable", "fail2ban")
	if err := runLocalCmd(logWriter, enableCmd[0], enableCmd[1:]...); err != nil {
		return err
	}
	// reload-or-restart: starts the daemon fresh if not running (loads our config from
	// disk on startup), or reloads it if already running. Avoids the race where
	// `fail2ban-client reload` runs before the freshly-started daemon's socket is up.
	restartCmd := append(sudo, "systemctl", "reload-or-restart", "fail2ban")
	if err := runLocalCmd(logWriter, restartCmd[0], restartCmd[1:]...); err != nil {
		return err
	}
	fmt.Fprintln(logWriter, "  fail2ban configured ✓")
	return nil
}

func installLocalCaddy(logWriter io.Writer) error {
	sudo := privilegedCmdPrefix()
	switch pkgManager() {
	case "dnf", "yum":
		pm := pkgManager()
		steps := [][]string{
			append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --batch --yes --no-tty -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg`),
			append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/config.rpm.txt' | tee /etc/yum.repos.d/caddy-stable.repo`),
			append(sudo, pm, "install", "-y", "caddy-"+build.CaddyVersion),
		}
		for _, args := range steps {
			if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
				return err
			}
		}
	default: // apt
		steps := [][]string{
			append(sudo, "apt-get", "update", "-y"),
			append(sudo, "apt-get", "install", "-y", "debian-keyring", "debian-archive-keyring", "apt-transport-https", "curl"),
			append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --batch --yes --no-tty -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg`),
			append(sudo, "bash", "-c", `curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list`),
			append(sudo, "apt-get", "update", "-y"),
			append(sudo, "apt-get", "install", "-y", "caddy="+build.CaddyVersion),
		}
		for _, args := range steps {
			if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
				return err
			}
		}
	}
	return nil
}

func installLocalRathole(logWriter io.Writer) error {
	var archTag string
	switch runtime.GOARCH {
	case "arm64":
		archTag = "aarch64-unknown-linux-musl"
	default:
		archTag = "x86_64-unknown-linux-gnu"
	}
	url := fmt.Sprintf(
		"https://github.com/%s/releases/download/%s/rathole-%s.zip",
		build.RatholeRepo, build.RatholeVersion, archTag,
	)

	// Ensure unzip is available before attempting to extract.
	if !isCommandAvailable("unzip") {
		pm := pkgManager()
		fmt.Fprintf(logWriter, "  unzip not found, installing via %s...\n", pm)
		pkgSudo := privilegedCmdPrefix()
		var installCmd []string
		switch pm {
		case "dnf", "yum":
			installCmd = append(pkgSudo, pm, "install", "-y", "-q", "unzip")
		default: // apt
			update := append(pkgSudo, "apt-get", "update", "-qq")
			if err := runLocalCmd(logWriter, update[0], update[1:]...); err != nil {
				return fmt.Errorf("failed to run apt-get update: %w", err)
			}
			installCmd = append(pkgSudo, "apt-get", "install", "-y", "-qq", "unzip")
		}
		if err := runLocalCmd(logWriter, installCmd[0], installCmd[1:]...); err != nil {
			return fmt.Errorf("failed to install unzip: %w", err)
		}
	}

	sudo := privilegedCmdPrefix()
	steps := [][]string{
		{"curl", "-fsSL", url, "-o", "/tmp/rathole.zip"},
		{"unzip", "-q", "-o", "/tmp/rathole.zip", "-d", "/tmp/rathole-dl"},
		append(sudo, "mv", "/tmp/rathole-dl/rathole", "/usr/local/bin/rathole"),
		append(sudo, "chmod", "+x", "/usr/local/bin/rathole"),
		{"rm", "-rf", "/tmp/rathole.zip", "/tmp/rathole-dl"},
	}
	for _, args := range steps {
		if err := runLocalCmd(logWriter, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}
