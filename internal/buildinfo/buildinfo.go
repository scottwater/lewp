// Package buildinfo carries version and build metadata for the lewp binary.
//
// The variables below are overridden at link time by bin/build via -ldflags
// (-X). When the binary is built without those flags (for example `go run` or
// a plain `go build` during local development) they retain the placeholder
// values, which is fine for day-to-day use.
package buildinfo

// These are intentionally package-level vars so they can be set with
// `go build -ldflags "-X github.com/scottwater/lewp/internal/buildinfo.Version=..."`.
var (
	// Version is the released version or `git describe` output.
	Version = "dev"
	// Commit is the short git commit the binary was built from.
	Commit = "unknown"
	// Date is the UTC compile time in RFC 3339 format.
	Date = "unknown"
)
