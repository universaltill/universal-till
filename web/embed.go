// Package web embeds the bundled UI templates and default static assets
// into the binary. Before this, internal/httpx read them from disk via
// relative paths ("web/ui/...", "web/public/...") — which only worked when
// the process's current working directory happened to be the repo/install
// root. Anywhere else (a packaged install launched by a shortcut/service
// with a different working directory) meant a broken app: a panic on every
// page render, and no CSS/JS/logo at all. Embedding removes that
// dependency entirely — the binary is self-contained regardless of where
// or how it's launched from.
package web

import "embed"

//go:embed ui public
var FS embed.FS
