// Package embedbin materializes the external binaries Gopher bundles (Caddy and
// rathole) from the embedded payload onto disk, so the supervisor can exec them
// by path. The edge runs caddy + its own-arch rathole; it also serves rathole
// for every origin arch (RatholeForOrigin).
//
// Extraction is version-gated: a binary is (re)written only when it's missing or
// its recorded version differs from the embedded one, so ordinary restarts do no
// disk writes. The payload (payload.go) is embedded from internal/embedbin/bin/,
// populated by scripts/fetch-deps.sh before a release build; a committed
// bin/.gitkeep lets plain `go build`/`go test` work with no binaries present
// (Embedded() then reports false), matching the cmd/server/agents.go convention.
package embedbin

import (
	"fmt"
	"os"
	"path/filepath"
)

// Binary is one bundled executable to materialize on disk.
type Binary struct {
	Name    string // file name under the target dir, e.g. "caddy"
	Version string // version stamp used to decide whether a rewrite is needed
	Data    []byte // the embedded bytes
}

// Extract materializes b into dir/<b.Name> with mode 0755, but only when it's
// missing or its recorded version differs from b.Version — so ordinary restarts
// do no disk writes. The write is atomic (temp file + rename): that avoids a
// half-written executable if gopher is killed mid-extract, and lets us replace a
// binary that is *currently running* (rename swaps the path while the running
// process keeps the old inode; an in-place truncating write would fail ETXTBSY).
//
// It returns the destination path and whether it actually (re)wrote the file.
func Extract(dir string, b Binary) (path string, wrote bool, err error) {
	path = filepath.Join(dir, b.Name)
	stamp := filepath.Join(dir, "."+b.Name+".version")

	// Already the right version and the binary is present? Nothing to do.
	if cur, readErr := os.ReadFile(stamp); readErr == nil && string(cur) == b.Version {
		if _, statErr := os.Stat(path); statErr == nil {
			return path, false, nil
		}
	}

	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("embedbin: mkdir %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b.Data, 0o755); err != nil {
		return "", false, fmt.Errorf("embedbin: write %s: %w", tmp, err)
	}
	// WriteFile's mode is masked by umask; force 0755 so it's executable.
	if err = os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("embedbin: chmod %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("embedbin: rename %s -> %s: %w", tmp, path, err)
	}
	// Best-effort stamp; if it fails we just re-extract next start (harmless).
	_ = os.WriteFile(stamp, []byte(b.Version), 0o644)
	return path, true, nil
}

// ExtractAll materializes every binary in bins into dir, stopping at the first
// error.
func ExtractAll(dir string, bins []Binary) error {
	for _, b := range bins {
		if _, _, err := Extract(dir, b); err != nil {
			return err
		}
	}
	return nil
}
