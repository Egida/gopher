package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

type MachineService struct {
	deploy *DeployService
	local  localOps
}

func NewMachineService(deploy *DeployService, local localOps) *MachineService {
	return &MachineService{deploy: deploy, local: local}
}

func (s *MachineService) List() ([]db.Machine, error) {
	return db.GetMachines()
}

func (s *MachineService) Get(id string) (*db.Machine, error) {
	return db.GetMachine(id)
}

func (s *MachineService) Create(req dto.CreateMachineRequest) (*db.Machine, error) {
	machine := &db.Machine{
		ID:         shortToken(),
		Name:       req.Name,
		Host:       req.Host,
		Port:       req.Port,
		Username:   req.Username,
		PrivateKey: req.PrivateKey,
		Status:     "pending",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if machine.Port == 0 {
		machine.Port = 22
	}

	if err := db.CreateMachine(machine); err != nil {
		return nil, err
	}
	return machine, nil
}

func (s *MachineService) Update(id string, req dto.UpdateMachineRequest) (*db.Machine, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}

	machine.Name = req.Name
	machine.Host = req.Host
	machine.Port = req.Port
	machine.Username = req.Username
	if req.PrivateKey != "" {
		machine.PrivateKey = req.PrivateKey
	}
	machine.UpdatedAt = time.Now()

	if err := db.UpdateMachine(machine); err != nil {
		return nil, err
	}
	return machine, nil
}

// DeleteResult reports the outcome of a machine delete to the API caller.
// Server-side cleanup always runs (machine row, tunnel rows, Caddy, rathole
// reconcile) and any failure there returns an error from Delete. The
// client-cleanup step is best-effort and its result is reported here so the
// dashboard can show a warning toast when the box wasn't actually torn down
// (e.g. tunnel was already dead, agent unreachable, no SSH key on file).
type DeleteResult struct {
	ID                string `json:"id"`
	ClientCleanupOK   bool   `json:"client_cleanup_ok"`
	ClientCleanupPath string `json:"client_cleanup_path,omitempty"` // "agent" | "ssh" | "skipped"
	ClientCleanupErr  string `json:"client_cleanup_error,omitempty"`
}

func (s *MachineService) Delete(id string) (*DeleteResult, error) {
	return s.delete(id, false)
}

// DeleteFromClient is the self-delete entry point: skips the
// remote-uninstall step because the client is already tearing itself
// down (this call comes FROM that teardown). Server-side cleanup —
// tunnels, Caddy, rathole config, machine record — runs as usual.
func (s *MachineService) DeleteFromClient(id string) (*DeleteResult, error) {
	return s.delete(id, true)
}

func (s *MachineService) delete(id string, fromClient bool) (*DeleteResult, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}
	tunnels, err := db.GetTunnelsByMachine(id)
	if err != nil {
		return nil, err
	}

	result := &DeleteResult{ID: id, ClientCleanupOK: true, ClientCleanupPath: "skipped"}

	// Server-driven delete: SSH/agent into the client first — while the
	// rathole back-channel is fully active — and trigger gopher-uninstall.
	// Doing this before ReconcileServerConfig avoids a race where rathole
	// restarts mid-delete and the SSH tunnel briefly disappears.
	//
	// Self-delete: the client is the caller, gopher-uninstall is already
	// running there. Skip the remote step to avoid the duplicate trigger.
	// Non-bootstrapped machines (created directly via /api/machines/ or the
	// external API with no tunnel) have no client-side install to clean —
	// skip the remote teardown entirely instead of reporting it as a
	// failure. ClientCleanupOK stays true and the handler returns 204.
	if s.local != nil && !fromClient && machine.TunnelPort > 0 {
		if machine.AgentInstalled && machine.AgentRemotePort > 0 {
			result.ClientCleanupPath = "agent"
		} else {
			result.ClientCleanupPath = "ssh"
		}
		if err := s.local.RemoveMachineClient(machine); err != nil {
			log.Printf("client cleanup for %s (%s) failed via %s: %v", machine.ID, machine.Name, result.ClientCleanupPath, err)
			result.ClientCleanupOK = false
			result.ClientCleanupErr = err.Error()
		}
	}

	// Best-effort teardown. The client was already uninstalled above, so bailing
	// on the first error would leave the DB / server.toml / Caddy diverged (a
	// half-deleted machine whose client is gone). Push through every deletion and
	// always run the final reconcile so state converges; collect failures and
	// report them at the end.
	var cleanupErrs []string
	for i := range tunnels {
		tunnel := &tunnels[i]
		if err := db.DeleteTunnel(tunnel.ID); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("delete tunnel %s: %v", tunnel.ID, err))
		}
		// Close the firewall port the tunnel opened (gopher-mode gated, no-op
		// otherwise) — mirrors TunnelService.Delete; without this, deleting a
		// machine strands every GOPHER_TUNNELS ACCEPT rule it held.
		RevokeTunnelPort(tunnel.RatholePort, tunnel.Transport)
		if s.local != nil {
			if err := s.local.RemoveServiceTunnelCaddy(tunnel); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Sprintf("caddy cleanup %s: %v", tunnel.ID, err))
			}
		}
	}

	db.LogEvent("machine_deleted", id, machine.Name)
	if err := db.DeleteMachine(id); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Sprintf("delete machine: %v", err))
	}
	// Close the machine's SSH-tunnel port too (bootstrap opened it via ApplyTunnelPort).
	RevokeTunnelPort(machine.TunnelPort, "tcp")

	// Single reconcile after all DB deletions — run it regardless of the above so
	// server.toml converges to what's left (avoids multiple rathole restarts).
	if s.local != nil {
		if err := s.local.ReconcileServerConfig(); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("server reconcile: %v", err))
		}
	}
	if len(cleanupErrs) > 0 {
		return result, fmt.Errorf("machine teardown incomplete (will self-heal on next reconcile): %s", strings.Join(cleanupErrs, "; "))
	}
	return result, nil
}

func (s *MachineService) Deploy(id string) error {
	machine, err := db.GetMachine(id)
	if err != nil {
		return err
	}

	go s.deploy.DeployClient(machine) //nolint:errcheck
	return nil
}

// RecoverMachine drives the server-side recovery flow: tries the agent push
// first, then SSH-via-tunnel, exactly as RetryPendingConfigPush does for the
// health-loop. Exposed as a separate entry point so the API handler doesn't
// need to know about LocalSetupService internals.
//
// Returns the underlying push error verbatim so the operator can see why
// recovery failed in the dashboard (e.g. "no space left on device" tells
// them to free disk first; "i/o timeout" tells them the tunnel is fully
// down and only the manual script will work).
func (s *MachineService) RecoverMachine(id string) error {
	machine, err := db.GetMachine(id)
	if err != nil {
		return err
	}
	local, ok := s.local.(interface {
		RetryPendingConfigPush(*db.Machine) error
	})
	if !ok {
		return fmt.Errorf("recovery not supported in this build")
	}
	return local.RetryPendingConfigPush(machine)
}

// CanonicalRatholeConfig returns the client.toml that this machine should
// currently be running, derived from the DB and the current server-side noise
// pubkey. Provides a manual recovery handle for the case where automated
// config push can't land (machine unreachable, disk full, agent broken).
// Equivalent to what mergeClientManagedConfig would produce against an empty
// existing file — the canonical "fresh paste" version.
func (s *MachineService) CanonicalRatholeConfig(id string) (string, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return "", err
	}
	settings, err := db.GetSettings()
	if err != nil {
		return "", fmt.Errorf("load settings: %w", err)
	}
	tunnels, err := db.GetTunnelsByMachine(machine.ID)
	if err != nil {
		return "", fmt.Errorf("load tunnels for machine: %w", err)
	}
	cfg, err := mergeClientManagedConfig("", machine, tunnels, ratholeHostFromSettings(settings), settings.RatholeNoisePubKey)
	if err != nil {
		return "", err
	}
	return cfg, nil
}

func (s *MachineService) Status(id string) (map[string]interface{}, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}

	// Prefer the agent: if it's installed, its live status already covers
	// rathole-client state without any SSH. Only reach for SSH when there's no
	// agent AND a usable private key is stored.
	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if st, aerr := NewAgentClient(machine).Status(ctx); aerr == nil {
			return map[string]interface{}{
				"id":             id,
				"connected":      true,
				"rathole_status": map[bool]string{true: "active", false: "inactive"}[st.Rathole.Active],
			}, nil
		}
	}

	var client *sshpkg.SSHClient
	if machine.TunnelPort > 0 && machineHasSSHPrivateKey(machine) {
		if sshKey, kerr := db.GetSSHKeyForMachine(machine); kerr == nil {
			client, err = sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
		}
	}
	if client == nil {
		if machine.Host != "" && machine.PrivateKey != "" {
			client, err = sshpkg.NewClient(machine.Host, machine.Port, machine.Username, machine.PrivateKey)
		} else {
			return map[string]interface{}{"id": id, "connected": false, "error": "no agent and no stored SSH private key"}, nil
		}
	}
	if err != nil {
		return map[string]interface{}{
			"id":        id,
			"connected": false,
			"error":     err.Error(),
		}, nil
	}
	defer client.Close()

	output, err := client.Execute("systemctl is-active rathole-client 2>&1 || echo 'not installed'")
	status := "unknown"
	if err == nil {
		status = output
	}

	return map[string]interface{}{
		"id":             id,
		"connected":      true,
		"rathole_status": status,
	}, nil
}

// RefreshNetworkInfo discovers the machine's WAN + LAN IP and stores the public
// IP. Prefers the agent (GetNetworkInfo, no SSH); falls back to SSH only when a
// private key is stored and the agent path didn't produce an answer (e.g. agent
// older than 0.2.2 → Unimplemented).
func (s *MachineService) RefreshNetworkInfo(id string) (map[string]interface{}, error) {
	machine, err := db.GetMachine(id)
	if err != nil {
		return nil, err
	}

	// Agent-first: no SSH, no private key needed. On old agents this returns
	// Unimplemented and we fall through to the SSH path.
	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		wan, lan, aerr := NewAgentClient(machine).NetworkInfo(ctx)
		cancel()
		if aerr == nil && (wan != "" || lan != "") {
			return s.finalizeNetworkInfo(machine, wan, lan), nil
		}
	}

	// SSH fallback needs a stored private key; when public-only, surface that.
	if machine.TunnelPort > 0 && !machineHasSSHPrivateKey(machine) && machine.PrivateKey == "" {
		return map[string]interface{}{"id": id, "error": "network-info discovery unavailable: no agent support and no stored SSH private key (public-only machine)"}, nil
	}

	var client *sshpkg.SSHClient
	var sshKeyErr error
	if machine.TunnelPort > 0 {
		key, kerr := db.GetSSHKeyForMachine(machine)
		if kerr != nil {
			sshKeyErr = kerr
		} else {
			client, _ = sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, key.PrivateKey)
		}
	}
	if client == nil && machine.Host != "" {
		p := machine.Port
		if p == 0 {
			p = 22
		}
		client, err = sshpkg.NewClient(machine.Host, p, machine.Username, machine.PrivateKey)
		if err != nil {
			return map[string]interface{}{"id": id, "error": err.Error()}, nil
		}
	}
	if client == nil {
		// Surface the SSH-key lookup failure when it was the actual reason
		// the tunnel path didn't take. The previous "no ssh access method"
		// message was misleading — the machine had a tunnel, just no key.
		if sshKeyErr != nil {
			return map[string]interface{}{"id": id, "error": fmt.Sprintf("ssh key lookup failed: %v", sshKeyErr)}, nil
		}
		return map[string]interface{}{"id": id, "error": "no ssh access method"}, nil
	}
	defer client.Close()

	// WAN (public) IP — try opendns first, fall back to ipify.
	wanOut, _ := client.Execute(
		`dig +short myip.opendns.com @resolver1.opendns.com 2>/dev/null | head -1 || curl -sf --max-time 5 https://api.ipify.org 2>/dev/null`,
	)
	publicIP := strings.TrimSpace(wanOut)

	// LAN (private) IP from the machine's own NIC.
	lanOut, _ := client.Execute(`hostname -I 2>/dev/null | awk '{print $1}'`)
	return s.finalizeNetworkInfo(machine, publicIP, strings.TrimSpace(lanOut)), nil
}

// finalizeNetworkInfo persists the discovered public IP and shapes the response,
// shared by the agent and SSH discovery paths.
func (s *MachineService) finalizeNetworkInfo(machine *db.Machine, publicIP, privateIP string) map[string]interface{} {
	if privateIP == "" {
		privateIP = machine.Host
	}
	if publicIP != "" && publicIP != machine.PublicIP {
		machine.PublicIP = publicIP
		machine.UpdatedAt = time.Now()
		_ = db.UpdateMachine(machine)
	}
	return map[string]interface{}{
		"id":         machine.ID,
		"public_ip":  publicIP,
		"private_ip": privateIP,
		"is_nat":     publicIP != "" && privateIP != "" && publicIP != privateIP,
	}
}

// ReassignSSHKey makes newKeyID the machine's single gopher-managed authorized
// key: the new key is installed and every prior gopher-managed key is removed,
// so the origin's authorized_keys never accumulates stale keys. The operator's
// own (non-gopher) keys are never touched.
func (s *MachineService) ReassignSSHKey(machineID, newKeyID string) error {
	machine, err := db.GetMachine(machineID)
	if err != nil {
		return err
	}
	newKey, err := db.GetSSHKey(newKeyID)
	if err != nil {
		return err
	}
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no active tunnel — cannot push key")
	}

	// Agent-first: set the single managed key — no SSH, no stored private key
	// needed. Older agents (<0.2.2) error (Unimplemented) and we fall through.
	installed := false
	if machine.AgentInstalled && machine.AgentRemotePort > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		aerr := NewAgentClient(machine).SetManagedKey(ctx, machine.Username, newKey.PublicKey)
		cancel()
		if aerr == nil {
			installed = true
		}
	}

	if !installed {
		// SSH fallback — needs the current key's private half.
		currentKey, err := db.GetSSHKeyForMachine(machine)
		if err != nil {
			return fmt.Errorf("cannot determine current SSH key: %w", err)
		}
		if currentKey.PrivateKey == "" {
			return fmt.Errorf("no agent support and current key is public-only — cannot install the new key; re-bootstrap the machine to rotate keys")
		}
		client, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, currentKey.PrivateKey)
		if err != nil {
			return fmt.Errorf("failed to connect to machine: %w", err)
		}
		defer client.Close()
		if _, err := client.Execute(setManagedKeyShell(newKey.PublicKey)); err != nil {
			return fmt.Errorf("failed to install new key on machine: %w", err)
		}
	}

	machine.SSHKeyID = newKeyID
	machine.UpdatedAt = time.Now()
	return db.UpdateMachine(machine)
}

// managedKeyMarker tags gopher's single managed authorized_keys entry so it can
// be found + replaced without touching operator-owned keys. Kept in sync with
// cmd/agent's managedKeyMarker and bootstrap.sh's marker.
const managedKeyMarker = "gopher-managed"

// setManagedKeyShell builds a shell snippet that makes pubKey the single
// gopher-managed key in ~/.ssh/authorized_keys: it strips every prior managed
// line (comment == marker) and writes this key tagged with the marker,
// atomically. Same logic as the agent's SetManagedKey, for the SSH fallback.
func setManagedKeyShell(pubKey string) string {
	// %q quotes for the shell the same way the rest of this file does; the awk
	// program normalizes the key to "type blob <marker>".
	return fmt.Sprintf(
		`ak=~/.ssh/authorized_keys; mkdir -p ~/.ssh; touch "$ak"; chmod 700 ~/.ssh; chmod 600 "$ak"; `+
			`line=$(printf '%%s\n' %q | awk '{print $1, $2, %q; exit}'); `+
			`grep -v " %s$" "$ak" > "$ak.tmp" 2>/dev/null || true; `+
			`printf '%%s\n' "$line" >> "$ak.tmp"; mv "$ak.tmp" "$ak"`,
		pubKey, managedKeyMarker, managedKeyMarker,
	)
}
