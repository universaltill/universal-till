// Package buildinfo exposes build-time metadata (the release version) to the
// rest of the app. Version is "dev" for local builds and is stamped by
// goreleaser at release time via:
//
//	-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.Version=<tag>"
package buildinfo

// Version is the release version, e.g. "0.1.2". Overridden at build time.
var Version = "dev"
