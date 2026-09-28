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

// HelpFS carries the built-in user manual (web/help/<locale>/<id>.md, plus
// its screenshots). Embedded for the same reason as the UI, and one more: the
// manual has to be readable when the line is down, which is exactly when
// somebody goes looking for it.
//
//go:embed help
var HelpFS embed.FS

// ReleaseNotesFS carries the owner-facing release notes
// (web/release-notes/<locale>/v<X.Y.Z>.md, ut-docs#3091) that Settings →
// About shows. Embedded for the same offline reason as the manual (ADR-0003):
// "what changed in this update" must be readable with the line down.
//
//go:embed release-notes
var ReleaseNotesFS embed.FS
