// Package layoutsalon embeds the shipped Salon layout plugin (ADR-0088,
// ut-docs#1904's own e2e proof of the mechanism) into the till binary, so a
// distributed build carries plugin.json and its locale files with no
// dependency on this source directory existing on disk at runtime — the
// same reason internal/data/seeddata embeds its SQL rather than reading it
// from a relative path.
//
// This content ships INSIDE the signed till binary, at the same trust
// boundary as core code — never fetched from the marketplace. ut-docs#1902
// (internal/plugins/builtinlayouts) installs it via plugins.PersistManifest
// with InstallOptions{TrustLevel: "system"}, skipping the network/signature
// steps a third-party marketplace listing needs while still running every
// ADR-0088 install-time validation (validateLayoutEntries, protected-key and
// conflict checks) PersistManifest always performs.
package layoutsalon

import "embed"

// ManifestJSON is plugins/layout-salon/plugin.json, byte for byte.
//
//go:embed plugin.json
var ManifestJSON []byte

// Locales is plugins/layout-salon/locales/*.json, keyed by locale (e.g.
// "locales/en.json") — the exact shape internal/plugins.Manager.syncLocales
// expects when it later reads these files back from paths.Plugins().
//
//go:embed locales
var Locales embed.FS
