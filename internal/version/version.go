// Package version exposes build/version metadata for TeleCollection.
// Version is overridden at build time via -ldflags "-X ...version.Version=...".
package version

// Version is the semantic version of the build. Default is a dev sentinel.
var Version = "0.0.0-dev"

// String returns the current version string.
func String() string {
	return Version
}
