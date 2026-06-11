//go:build !embedbins

package embedbin

// Stub payload for ordinary builds (`go build`/`go test`/IDE). The real
// binaries are only compiled in under the `embedbins` tag, so default builds
// need no fetched binaries and stay fast. Embedded reports false so callers can
// fall back (dev mode runs caddy/rathole from PATH instead).

// Embedded reports whether real binaries were compiled in.
func Embedded() bool { return false }

// RunBundle returns the binaries the edge extracts and runs. Empty in non-embed
// builds.
func RunBundle() []Binary { return nil }

// RatholeForOrigin returns the rathole binary to serve to an origin with the
// given `uname -m`. Always (nil,false) in non-embed builds.
func RatholeForOrigin(uname string) ([]byte, bool) { return nil, false }
