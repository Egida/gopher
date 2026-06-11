package embedbin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtract_WritesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	p, wrote, err := Extract(dir, Binary{Name: "tool", Version: "1", Data: []byte("AAAA")})
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true nil", wrote, err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "AAAA" {
		t.Fatalf("content = %q, want AAAA", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755 (executable)", fi.Mode().Perm())
	}
}

func TestExtract_SkipsWhenVersionUnchanged(t *testing.T) {
	dir := t.TempDir()
	b := Binary{Name: "tool", Version: "1", Data: []byte("AAAA")}
	if _, _, err := Extract(dir, b); err != nil {
		t.Fatal(err)
	}
	// Tamper with the on-disk copy; a skipped extract must leave it untouched.
	p := filepath.Join(dir, "tool")
	if err := os.WriteFile(p, []byte("TAMPERED"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, wrote, err := Extract(dir, b) // same version -> skip
	if err != nil || wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=false nil", wrote, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "TAMPERED" {
		t.Fatalf("skipped extract rewrote the file: %q", got)
	}
}

func TestExtract_RewritesOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Extract(dir, Binary{Name: "tool", Version: "1", Data: []byte("OLD")}); err != nil {
		t.Fatal(err)
	}
	p, wrote, err := Extract(dir, Binary{Name: "tool", Version: "2", Data: []byte("NEW")})
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true (version changed)", wrote, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "NEW" {
		t.Fatalf("content = %q, want NEW", got)
	}
}

func TestExtract_RewritesWhenBinaryDeletedButStampKept(t *testing.T) {
	dir := t.TempDir()
	b := Binary{Name: "tool", Version: "1", Data: []byte("AAAA")}
	if _, _, err := Extract(dir, b); err != nil {
		t.Fatal(err)
	}
	// Stamp says "1" but the binary is gone — must re-extract, not trust the stamp.
	if err := os.Remove(filepath.Join(dir, "tool")); err != nil {
		t.Fatal(err)
	}
	_, wrote, err := Extract(dir, b)
	if err != nil || !wrote {
		t.Fatalf("Extract: wrote=%v err=%v, want wrote=true (binary was missing)", wrote, err)
	}
}

func TestExtract_NoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Extract(dir, Binary{Name: "tool", Version: "1", Data: []byte("AAAA")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tool.tmp")); !os.IsNotExist(err) {
		t.Fatal("tool.tmp left behind after extract")
	}
}
