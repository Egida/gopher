package embedbin

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/build"
)

// Proves the full chain with the REAL embedded binaries: embed -> extract ->
// runnable executable of the right arch. Skips unless scripts/fetch-deps.sh has
// populated internal/embedbin/bin/ (i.e. a release/deploy build).
func TestRunBundle_ExtractsRunnableBinaries(t *testing.T) {
	if !Embedded() {
		t.Skip("binaries not fetched; run scripts/fetch-deps.sh")
	}
	dir := t.TempDir()
	if err := ExtractAll(dir, RunBundle()); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}

	// caddy prints its version and exits 0 — assert it's our pinned version.
	out, err := exec.Command(dir+"/caddy", "version").CombinedOutput()
	if err != nil {
		t.Fatalf("running extracted caddy: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), build.CaddyVersion) {
		t.Fatalf("caddy version = %q, want it to contain %q", out, build.CaddyVersion)
	}

	// rathole: prove the extracted file is a runnable executable of this arch.
	// A non-zero exit (ExitError) still means it executed; only an exec/format
	// error means a wrong-arch or corrupt binary.
	if err := exec.Command(dir+"/rathole", "--help").Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("extracted rathole is not a runnable executable: %v", err)
		}
	}
}

func TestRatholeForOrigin_AllArches(t *testing.T) {
	if !Embedded() {
		t.Skip("binaries not fetched; run scripts/fetch-deps.sh")
	}
	for _, uname := range []string{"x86_64", "aarch64", "armv7l"} {
		if data, ok := RatholeForOrigin(uname); !ok || len(data) == 0 {
			t.Errorf("RatholeForOrigin(%q): ok=%v len=%d, want servable bytes", uname, ok, len(data))
		}
	}
	if _, ok := RatholeForOrigin("riscv64"); ok {
		t.Error("RatholeForOrigin(riscv64) = ok, want unsupported")
	}
}
