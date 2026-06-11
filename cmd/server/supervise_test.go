package main

import "testing"

// In a default (non-embedded) test build, no binaries are compiled in, so
// startBundledChildren must be a safe no-op: nil supervisor, nil error, and
// crucially it must NOT spawn anything that would collide with the
// systemd-managed caddy/rathole on a live edge. This guards the "deploying the
// wiring changes nothing until we opt into embedding" property.
func TestStartBundledChildren_NoopWhenNotEmbedded(t *testing.T) {
	sup, err := startBundledChildren()
	if err != nil {
		t.Fatalf("startBundledChildren err = %v, want nil", err)
	}
	if sup != nil {
		t.Fatal("startBundledChildren returned a supervisor in a non-embedded build; must be a no-op")
	}
}
