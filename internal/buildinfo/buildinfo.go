// Package buildinfo exposes build-time metadata (the release version) to the
// rest of the app. Version is "dev" for local builds and is stamped by
// goreleaser at release time via:
//
//	-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.Version=<tag>"
package buildinfo

import "time"

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

// BuildDate is when this binary's built-in strings were frozen: the source
// commit's time in RFC 3339 (`git show -s --format=%cI HEAD`), so a rebuild
// of the same commit stamps the same value. Stamped beside Version:
//
//	-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.BuildDate=<RFC 3339>"
//
// A first-party language bundle generated after it beats the built-in
// locale files; an older one only fills their gaps (ADR-0160 §6).
// scripts/ci/guard-builddate-stamped.sh keeps every Version stamp paired
// with one. Empty in source builds.
var BuildDate = ""

// NewerThanBuild reports whether a bundle generated at generatedAt (RFC 3339)
// is newer than this binary's built-in strings. A missing or unparseable
// generatedAt is never newer (it cannot prove it is). A build without a
// usable BuildDate (a source build) treats every dated bundle as newer.
func NewerThanBuild(generatedAt string) bool {
	return newerThan(generatedAt, BuildDate)
}

func newerThan(generatedAt, buildDate string) bool {
	gen, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil {
		return false
	}
	built, err := time.Parse(time.RFC3339, buildDate)
	if err != nil {
		return true
	}
	return gen.After(built)
}
