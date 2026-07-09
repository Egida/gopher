package service

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
)

type TunnelService struct {
	local localOps
}

func NewTunnelService(local localOps) *TunnelService {
	return &TunnelService{local: local}
}

const (
	machineSSHTunnelPrefix   = "machine-"
	machineSSHTunnelSuffix   = "-ssh"
	machineAgentTunnelSuffix = "-agent"

	// Agent is considered "active" if its last successful health poll
	// landed within this window. Health service polls every 60s, so 2
	// minutes is two missed polls' worth of grace.
	agentActiveWindow = 2 * time.Minute
)

func machineSSHTunnelID(machineID string) string {
	return machineSSHTunnelPrefix + machineID + machineSSHTunnelSuffix
}

func parseMachineSSHTunnelID(id string) (string, bool) {
	if !strings.HasPrefix(id, machineSSHTunnelPrefix) || !strings.HasSuffix(id, machineSSHTunnelSuffix) {
		return "", false
	}
	machineID := strings.TrimSuffix(strings.TrimPrefix(id, machineSSHTunnelPrefix), machineSSHTunnelSuffix)
	if machineID == "" {
		return "", false
	}
	return machineID, true
}

func machineAgentTunnelID(machineID string) string {
	return machineSSHTunnelPrefix + machineID + machineAgentTunnelSuffix
}

// agentTunnelStatus derives a tunnel-list status from machine.AgentInstalled
// + AgentLastSeen freshness. "active" once we've had a successful poll
// recently, "offline" if we have a record but it's stale, "pending" before
// the first poll lands.
func agentTunnelStatus(m *db.Machine) string {
	if m.AgentLastSeen != nil && time.Since(*m.AgentLastSeen) <= agentActiveWindow {
		return "active"
	}
	if m.AgentInstalled {
		return "offline"
	}
	return "pending"
}

func machineTunnelStatus(status string) string {
	if status == "connected" {
		return "active"
	}
	return status
}

func (s *TunnelService) List() ([]db.Tunnel, error) {
	tunnels, err := db.GetTunnels()
	if err != nil {
		return nil, err
	}
	machines, err := db.GetMachines()
	if err != nil {
		return nil, err
	}
	for _, machine := range machines {
		// SSH tunnel — only for SSH-enabled machines. The agent back-channel
		// below is synthesized INDEPENDENTLY so agent-only machines (SSH disabled
		// → TunnelPort 0) still surface their control-plane tunnel. A `continue`
		// here previously skipped both, hiding agent-only machines everywhere the
		// tunnel list is consumed (tunnels page, network map).
		if machine.TunnelPort != 0 {
			tunnels = append(tunnels, db.Tunnel{
				ID:          machineSSHTunnelID(machine.ID),
				MachineID:   machine.ID,
				Name:        machine.Name + " SSH",
				Subdomain:   "",
				LocalPort:   22,
				RatholePort: machine.TunnelPort,
				Protocol:    "tcp",
				Private:     !machine.PublicSSH,
				Status:      machineTunnelStatus(machine.Status),
				Managed:     true,
				Kind:        "machine-ssh",
				CreatedAt:   machine.CreatedAt,
				UpdatedAt:   machine.UpdatedAt,
			})
		}

		// gopher-agent back-channel — only when the machine has agent
		// fields allocated (always for new bootstraps; populated on
		// migration for older ones via AgentInstaller). Always private
		// (127.0.0.1 on both ends), kind="machine-agent" so the UI can
		// group/style it as management plumbing rather than a user tunnel.
		if machine.AgentRemotePort > 0 && machine.AgentLocalPort > 0 {
			tunnels = append(tunnels, db.Tunnel{
				ID:          machineAgentTunnelID(machine.ID),
				MachineID:   machine.ID,
				Name:        machine.Name + " Agent",
				Subdomain:   "",
				LocalPort:   machine.AgentLocalPort,
				RatholePort: machine.AgentRemotePort,
				Protocol:    "tcp",
				Private:     true,
				Status:      agentTunnelStatus(&machine),
				Managed:     true,
				Kind:        "machine-agent",
				CreatedAt:   machine.CreatedAt,
				UpdatedAt:   machine.UpdatedAt,
			})
		}
	}
	return tunnels, nil
}

func (s *TunnelService) ListByMachine(machineID string) ([]db.Tunnel, error) {
	return db.GetTunnelsByMachine(machineID)
}

func (s *TunnelService) Get(id string) (*db.Tunnel, error) {
	return db.GetTunnel(id)
}

// Probe runs a live connectivity check on the tunnel and returns one of
// "active", "idle", or "offline". It uses the same logic as the background
// monitor so the result is consistent with what the dashboard shows.
func (s *TunnelService) Probe(t *db.Tunnel) string {
	return probeTunnel(*t)
}

func (s *TunnelService) NextPort() (int, error) {
	return db.NextRatholePort()
}

func (s *TunnelService) Create(req dto.CreateTunnelRequest) (*db.Tunnel, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	if req.LocalPort == 22 {
		return nil, &apperrors.ValidationError{Field: "local_port", Message: "port 22 is reserved for machine SSH tunnels"}
	}
	transport := req.Transport
	if transport != "udp" {
		transport = "tcp"
	}
	// UDP tunnels cannot have HTTP subdomain routing
	if transport == "udp" {
		req.Subdomain = ""
		req.NoTLS = false
	}
	if req.Subdomain != "" && settings.Domain == "" {
		return nil, &apperrors.ValidationError{Field: "subdomain", Message: "URL routing is disabled; leave subdomain empty"}
	}

	if req.Subdomain != "" {
		if err := config.ValidateSubdomain(req.Subdomain); err != nil {
			return nil, &apperrors.ValidationError{Field: "subdomain", Message: err.Error()}
		}
		exists, err := db.CheckSubdomainExists(req.Subdomain)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, &apperrors.ConflictError{Message: "subdomain already exists"}
		}
	}
	if err := config.ValidatePort(req.LocalPort); err != nil {
		return nil, &apperrors.ValidationError{Field: "local_port", Message: err.Error()}
	}

	var ratholePort int
	if req.RatholePort != 0 {
		if err := config.ValidatePort(req.RatholePort); err != nil {
			return nil, &apperrors.ValidationError{Field: "rathole_port", Message: err.Error()}
		}
		exists, err := db.CheckRatholePortExists(req.RatholePort)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, &apperrors.ConflictError{Message: fmt.Sprintf("server port %d is already in use by another tunnel", req.RatholePort)}
		}
		ratholePort = req.RatholePort
	} else {
		var err error
		ratholePort, err = db.NextRatholePort()
		if err != nil {
			return nil, err
		}
	}

	// Bot protection requires a subdomain (needs Host-header routing through proxy).
	botProtection := req.BotProtectionEnabled && req.Subdomain != "" && transport != "udp"
	// Bot protection is only meaningful if the raw port is closed — otherwise it's
	// trivially bypassed by hitting the rathole port directly. Enforce private.
	private := req.Private || botProtection

	tunnel := &db.Tunnel{
		ID:                   shortToken(),
		MachineID:            req.MachineID,
		Name:                 req.Name,
		Subdomain:            req.Subdomain,
		LocalPort:            req.LocalPort,
		RatholePort:          ratholePort,
		RatholeToken:         shortToken(),
		Protocol:             "tcp",
		Transport:            transport,
		NoTLS:                req.NoTLS,
		Private:              private,
		BotProtectionEnabled: botProtection,
		BotProtectionTTL:     req.BotProtectionTTL,
		BotProtectionAllowIP: req.BotProtectionAllowIP,
		TLSSkipVerify:        req.TLSSkipVerify && req.Subdomain != "" && !req.NoTLS && transport != "udp",
		Status:               "inactive",
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}

	if err := db.CreateTunnel(tunnel); err != nil {
		return nil, err
	}
	db.LogEvent("tunnel_created", tunnel.ID, tunnel.Name)

	// Open firewall port if Gopher manages the firewall (non-fatal).
	ApplyTunnelPort(tunnel.RatholePort, tunnel.Transport, tunnel.Private)

	// Push configs to server + client (non-fatal: tunnel is saved even if this fails)
	machine, machErr := db.GetMachine(req.MachineID)
	if machErr == nil && s.local != nil {
		log.Printf("tunnel create: pushing config for tunnel %s to machine %s (port %d)", tunnel.ID, machine.ID, machine.TunnelPort)
		if cfgErr := s.local.AddServiceTunnel(tunnel, machine); cfgErr != nil {
			log.Printf("tunnel create: config push failed for tunnel %s: %v", tunnel.ID, cfgErr)
			// Annotate status (transient — the 30s monitor overwrites it) AND
			// record a persistent event, so the operator can still tell "config
			// push failed" from a plain "offline" after the status is clobbered.
			tunnel.Status = fmt.Sprintf("config-error: %v", cfgErr)
			_ = db.UpdateTunnel(tunnel)
			db.RecordEvent(&db.Event{
				Severity:     "error",
				Source:       "tunnel",
				Kind:         "tunnel_config_error",
				ResourceType: "tunnel",
				ResourceID:   tunnel.ID,
				ResourceName: tunnel.Name,
				Message:      fmt.Sprintf("Tunnel %q config push failed — it won't serve until fixed: %v", tunnel.Name, cfgErr),
			})
		} else {
			log.Printf("tunnel create: config push succeeded for tunnel %s", tunnel.ID)
		}
	} else if machErr != nil {
		log.Printf("tunnel create: could not load machine %s: %v — skipping config push", req.MachineID, machErr)
	}

	return tunnel, nil
}

func (s *TunnelService) Update(id string, req dto.UpdateTunnelRequest) (*db.Tunnel, error) {
	// Machine SSH tunnels are virtual (not in the tunnels table).
	// Only the Private field can change — it maps to Machine.PublicSSH.
	if machineID, ok := parseMachineSSHTunnelID(id); ok {
		return s.updateMachineSSHPrivacy(machineID, req.Private)
	}

	tunnel, err := db.GetTunnel(id)
	if err != nil {
		return nil, err
	}
	if req.LocalPort == 22 {
		return nil, &apperrors.ValidationError{Field: "local_port", Message: "port 22 is reserved for machine SSH tunnels"}
	}
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}

	// Capture the original subdomain BEFORE mutating it below. The Caddy
	// reconcile near the end compares oldSubdomain against the final value to
	// decide whether to rewrite conf.d/<id>.caddy. Capturing it after the
	// mutation made that compare a no-op, so subdomain edits updated the DB
	// (and the UI) but never touched Caddy — it kept serving the old block.
	oldSubdomain := tunnel.Subdomain

	if req.Subdomain != tunnel.Subdomain {
		if req.Subdomain != "" && settings.Domain == "" {
			return nil, &apperrors.ValidationError{Field: "subdomain", Message: "URL routing is disabled; leave subdomain empty"}
		}
		if err := config.ValidateSubdomain(req.Subdomain); err != nil {
			return nil, &apperrors.ValidationError{Field: "subdomain", Message: err.Error()}
		}
		exists, err := db.CheckSubdomainExists(req.Subdomain)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, &apperrors.ConflictError{Message: "subdomain already exists"}
		}
		tunnel.Subdomain = req.Subdomain
	}

	oldPrivate := tunnel.Private
	oldBotProtection := tunnel.BotProtectionEnabled
	oldTLSSkipVerify := tunnel.TLSSkipVerify
	oldLocalPort := tunnel.LocalPort
	tunnel.Name = req.Name
	tunnel.LocalPort = req.LocalPort
	tunnel.Private = req.Private
	// Private tunnels KEEP their subdomain — "private" means the rathole port
	// binds to 127.0.0.1 (no raw public port), but the tunnel is still served
	// via its Caddy subdomain (reverse-proxy-only). Clearing the subdomain here
	// was the bug that made the URL hint vanish on toggle-to-private.
	// Bot protection requires a subdomain and TCP transport.
	tunnel.BotProtectionEnabled = req.BotProtectionEnabled && tunnel.Subdomain != "" && tunnel.Transport != "udp"
	// Bot protection is only enforceable if the raw port is closed (otherwise it's
	// bypassed by hitting the rathole port directly), so it implies private.
	if tunnel.BotProtectionEnabled {
		tunnel.Private = true
	}
	tunnel.BotProtectionTTL = req.BotProtectionTTL
	tunnel.BotProtectionAllowIP = req.BotProtectionAllowIP
	tunnel.TLSSkipVerify = req.TLSSkipVerify && tunnel.Subdomain != "" && !tunnel.NoTLS && tunnel.Transport != "udp"
	tunnel.UpdatedAt = time.Now()

	if err := db.UpdateTunnel(tunnel); err != nil {
		return nil, err
	}

	// If privacy setting changed, update rathole bind_addr and firewall. Compare
	// against the final tunnel.Private (not req.Private) — bot protection can
	// force private even when the request didn't ask for it.
	if oldPrivate != tunnel.Private && s.local != nil {
		log.Printf("tunnel update: privacy changed for %s (private=%v), reconciling server config", tunnel.ID, tunnel.Private)
		if err := s.local.ReconcileServerConfig(); err != nil {
			log.Printf("tunnel update: reconcile failed: %v", err)
		}
		ApplyTunnelPort(tunnel.RatholePort, tunnel.Transport, tunnel.Private)
		// The Caddy upstream depends on privacy (private → localhost, public →
		// bind_ip), and on a bind_ip host the existing block is now stale. The
		// subdomain itself didn't change, so rewrite it here. No-ops without a
		// subdomain or configured domain.
		if err := s.local.WriteServiceTunnelCaddy(tunnel); err != nil {
			log.Printf("tunnel update: rewrite caddy block after privacy change for %s: %v", tunnel.ID, err)
		}
	}

	// If LocalPort changed, the client.toml's `local_addr = "localhost:<port>"`
	// for this tunnel is now stale and the client will silently keep routing
	// to the old port. Push the regenerated client.toml. Server-side reconcile
	// is unaffected (rathole-server doesn't see local_addr) so we skip it.
	if oldLocalPort != tunnel.LocalPort && s.local != nil {
		machine, machErr := db.GetMachine(tunnel.MachineID)
		if machErr != nil {
			log.Printf("tunnel update: load machine for client push failed: %v", machErr)
		} else if pushErr := s.local.AddServiceTunnel(tunnel, machine); pushErr != nil {
			log.Printf("tunnel update: client.toml push for %s failed: %v", tunnel.ID, pushErr)
		}
	}

	// If subdomain changed, the on-disk Caddy block is stale (managed file
	// path is keyed by tunnel ID, content holds the old subdomain). Three
	// transitions to handle:
	//   "" → "x"        : write new block, reload
	//   "x" → "y"       : overwrite block, reload
	//   "x" → ""        : remove block (privacy flipped to private), reload
	if oldSubdomain != tunnel.Subdomain && s.local != nil {
		switch {
		case tunnel.Subdomain == "":
			// Subdomain cleared (or flipped private) → drop the Caddy file.
			if err := s.local.RemoveServiceTunnelCaddy(tunnel); err != nil {
				log.Printf("tunnel update: remove caddy block for %s: %v", tunnel.ID, err)
			}
		case tunnel.Transport != "udp":
			// Subdomain set/changed → (re)write the block (private tunnels
			// included — they're reverse-proxy-only). No-ops if no domain is
			// configured. The managed file is keyed by tunnel ID, so the
			// rewrite replaces the old subdomain's block in place.
			if err := s.local.WriteServiceTunnelCaddy(tunnel); err != nil {
				log.Printf("tunnel update: rewrite caddy block for %s: %v", tunnel.ID, err)
			}
		}
	}

	// If bot protection or TLS skip verify toggled (and the subdomain branch
	// above didn't already rewrite), refresh the Caddy block.
	if oldSubdomain == tunnel.Subdomain && (oldBotProtection != tunnel.BotProtectionEnabled || oldTLSSkipVerify != tunnel.TLSSkipVerify) && tunnel.Subdomain != "" && s.local != nil {
		if err := s.local.WriteServiceTunnelCaddy(tunnel); err != nil {
			log.Printf("tunnel update: refresh caddy block for %s: %v", tunnel.ID, err)
		}
	}

	return tunnel, nil
}

// updateMachineSSHPrivacy toggles the SSH tunnel visibility for a bootstrapped machine.
// Private=true → bind 127.0.0.1 (jumpbox only); Private=false → bind 0.0.0.0 (public).
func (s *TunnelService) updateMachineSSHPrivacy(machineID string, private bool) (*db.Tunnel, error) {
	machine, err := db.GetMachine(machineID)
	if err != nil {
		return nil, err
	}
	oldPublicSSH := machine.PublicSSH
	machine.PublicSSH = !private
	machine.UpdatedAt = time.Now()
	if err := db.UpdateMachine(machine); err != nil {
		return nil, err
	}
	if oldPublicSSH != machine.PublicSSH && s.local != nil {
		log.Printf("tunnel update: machine %s SSH visibility changed (public=%v), reconciling", machineID, machine.PublicSSH)
		if err := s.local.ReconcileServerConfig(); err != nil {
			log.Printf("tunnel update: reconcile failed: %v", err)
		}
		if machine.PublicSSH {
			ApplyPublicSSHPort(machine.TunnelPort) // public SSH → edge rate-limited
		} else {
			ApplyTunnelPort(machine.TunnelPort, "tcp", true)
		}
	}
	// Return a synthetic tunnel matching what List() would emit.
	return &db.Tunnel{
		ID:          machineSSHTunnelID(machineID),
		MachineID:   machineID,
		Name:        machine.Name + " SSH",
		LocalPort:   22,
		RatholePort: machine.TunnelPort,
		Protocol:    "tcp",
		Private:     !machine.PublicSSH,
		Status:      machineTunnelStatus(machine.Status),
		Managed:     true,
		Kind:        "machine-ssh",
		CreatedAt:   machine.CreatedAt,
		UpdatedAt:   machine.UpdatedAt,
	}, nil
}

func (s *TunnelService) Delete(id string) error {
	if _, isMachineSSHTunnel := parseMachineSSHTunnelID(id); isMachineSSHTunnel {
		return &apperrors.ValidationError{Field: "id", Message: "cannot delete machine SSH tunnel directly; delete the machine instead"}
	}

	tunnel, err := db.GetTunnel(id)
	if err != nil {
		return err
	}
	if tunnel.LocalPort == 22 {
		return &apperrors.ValidationError{Field: "local_port", Message: "port 22 tunnel cannot be deleted directly; delete the machine instead"}
	}

	machine, machErr := db.GetMachine(tunnel.MachineID)
	if machErr == nil && s.local != nil {
		_ = s.local.RemoveServiceTunnelClient(tunnel, machine)
	}

	db.LogEvent("tunnel_deleted", id, tunnel.Name)
	if err := db.DeleteTunnel(id); err != nil {
		return err
	}

	// Close firewall port if Gopher manages the firewall (non-fatal).
	RevokeTunnelPort(tunnel.RatholePort, tunnel.Transport)

	if s.local != nil {
		// Best-effort: the DB row is already gone (the source of truth for the
		// next reconcile), so run BOTH cleanup steps even if one fails — a
		// reconcile error must not skip the Caddy-block removal, or the
		// subdomain keeps 502-serving a tunnel that no longer exists.
		var cleanupErrs []string
		if err := s.local.ReconcileServerConfig(); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("server reconcile: %v", err))
		}
		if err := s.local.RemoveServiceTunnelCaddy(tunnel); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("caddy cleanup: %v", err))
		}
		if len(cleanupErrs) > 0 {
			return fmt.Errorf("tunnel deleted, but edge cleanup was incomplete (will self-heal on next reconcile): %s", strings.Join(cleanupErrs, "; "))
		}
	}
	return nil
}
