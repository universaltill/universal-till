//go:build bundledplugins

package bundled

import (
	"embed"
	"io/fs"
)

// The release bundles fetched by scripts/ci/fetch-bundled-plugins.sh sit in
// embedded/<plugin id>/ ; the iOS/appstore build sets the bundledplugins tag.
//
// Every locked file is embedded by NAME, never by directory or glob: if the
// fetch step was skipped or failed, `go build -tags bundledplugins` stops with
// "pattern ...: no matching files found" instead of shipping an app with no
// payment or tax plugins (ut-docs#6565, finding 19).
// TestEmbedOnNamesEveryLockedFile keeps this list in step with
// bundled.lock.json; add a line here whenever a plugin or a pinned file is
// added to the lock.
//
//go:embed embedded/com.universaltill.payment-stripe/manifest.json embedded/com.universaltill.payment-stripe/bin/plugin.wasm embedded/com.universaltill.payment-stripe/locales/en.json
//go:embed embedded/com.universaltill.payment-sumup/manifest.json embedded/com.universaltill.payment-sumup/bin/plugin.wasm embedded/com.universaltill.payment-sumup/locales/de.json embedded/com.universaltill.payment-sumup/locales/en.json embedded/com.universaltill.payment-sumup/locales/tr.json
//go:embed embedded/com.universaltill.tax-uk/manifest.json embedded/com.universaltill.tax-uk/bin/plugin.wasm embedded/com.universaltill.tax-uk/content/index.html embedded/com.universaltill.tax-uk/locales/en-GB.json embedded/com.universaltill.tax-uk/locales/en.json
var embeddedRoot embed.FS

func embeddedFS() fs.FS {
	sub, err := fs.Sub(embeddedRoot, "embedded")
	if err != nil {
		return nil
	}
	return sub
}
