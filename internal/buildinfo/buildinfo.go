// Package buildinfo exposes build-time metadata (the release version) to the
// rest of the app. Version is "dev" for local builds and is stamped by
// goreleaser at release time via:
//
//	-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.Version=<tag>"
package buildinfo

// Version is the release version, e.g. "0.1.2". Overridden at build time.
var Version = "dev"

// MarketplacePublicKey is the marketplace's Ed25519 plugin-signing public key
// (64 hex characters), compiled into release builds (ADR-0158):
//
//	-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.MarketplacePublicKey=<hex>"
//
// When set it is the only key the till trusts: UT_MARKETPLACE_PUBLIC_KEY and
// a persisted/fetched key are ignored. Empty in source builds, which keep the
// env/lazy-fetch path (ADR-0148) and refuse every plugin until they have a
// key — unless built with -tags devplugins. CI resolves the value with
// scripts/ci/resolve-marketplace-key.sh.
var MarketplacePublicKey = ""
