//go:build !bundledplugins

package bundled

import "io/fs"

// A default build embeds nothing: the bundled set is empty.
func embeddedFS() fs.FS { return nil }
