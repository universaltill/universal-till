//go:build bundledplugins

package bundled

import (
	"embed"
	"io/fs"
)

// The release bundles fetched by scripts/ci/fetch-bundled-plugins.sh sit in
// embedded/<plugin id>/ ; the iOS/appstore build sets the bundledplugins tag.
//
//go:embed all:embedded
var embeddedRoot embed.FS

func embeddedFS() fs.FS {
	sub, err := fs.Sub(embeddedRoot, "embedded")
	if err != nil {
		return nil
	}
	return sub
}
