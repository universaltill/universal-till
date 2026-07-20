// Package locales embeds the base translation JSON files into the binary.
package locales

import "embed"

//go:embed *.json
var FS embed.FS
