// Package i18nmeta embeds the key metadata (en.meta.json) and the
// namespace-to-area map (areas.json) — ADR-0160 §2/§3. The till reads them
// for translate mode's key panel (internal/i18nmeta, ut-docs#4370).
package i18nmeta

import "embed"

//go:embed *.json
var FS embed.FS
