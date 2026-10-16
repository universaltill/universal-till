# Review — fonts shipped inside language bundles (ut-docs#4374)

**Date:** 2026-10-16 · **Card:** ut-docs#4374 (ADR-0160 §8, task 13) ·
**Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable, fresh context,
detached worktree.

## What shipped
- **`internal/plugins/bundle_fonts.go`**: `Manager.syncBundleFonts()` (from
  `Init` and `Reload`) lists `fonts/*.woff2` of every **active** plugin whose
  on-disk manifest says `canonical_type: "language"`. Only plain names
  (`IsBundleFontName`), regular files and files starting with the `wOF2`
  signature count. `BundleFonts()` returns a copy under its own RWMutex.
- **`internal/pages/plugin_fonts.go`**:
  - `GET /plugin-fonts/{plugin}/{version}/{file}` serves only an exactly
    listed font: `font/woff2`, nosniff, `default-src 'none'; sandbox`, and
    `Access-Control-Allow-Origin: *` (plugin page frames have an opaque
    origin, and `@font-face` fetches are CORS-mode).
  - `GET /plugin-fonts/bundle-fonts.css` generates one `@font-face` per font
    (`ut-bundle-N`) plus `--ut-bundle-fonts`; `no-cache` + ETag.
  - Both auth-exempt, LAN-reachable (languard), demo-allowed, reserved from
    plugin page routes, skipped by the help-topic and page-http-error route
    scanners.
- **`web/public/app.css`**: the body stack adds
  `var(--ut-bundle-fonts, sans-serif)` **after** the generic `sans-serif`, so
  a bundle font only draws glyphs the system fonts lack — never Latin text
  or digits (a language plugin can't restyle prices).
- Every page head that loads app.css loads the stylesheet too (base, login,
  setup, self-order, self-order shop, order tracking, the plugin page frame).
- Help topic `plugins.md` (en/de/tr/ar/fa): one sentence.
- ut-cloud: the scanner already accepted font files; a pinning test is in
  ut-cloud's own review record. ut-docs: `architecture/plugin-architecture.md` §7.

## Review
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Should-fix | The plugin page sandbox frame (`pluginFrameTmpl`) loaded app.css but not the bundle fonts, and the font response would be CORS-refused in its opaque origin. | **Fixed**: `<link>` added, `Access-Control-Allow-Origin: *` on the font route; tests `TestPluginFrameDocument_LoadsBundleFonts_4374` and the ACAO assertion. Driven check: the font is `loaded` inside a real empty-sandbox srcdoc frame. |
| 2 | Low | No woff2 signature check: any blob named `*.woff2` reached every browser's font parser. | **Fixed**: `wOF2` magic required (fail closed); test covers a fake and a truncated file. |
| 3 | Nit | One file = one family at weight 400; separate bold files are never selected. | Accepted; documented in plugin-architecture §7 ("one subset per script"). |
| 4 | Nit | An open persistent shell keeps old font URLs after a pack auto-update. | Accepted; documented (new font shows from the next full load). |
| 5 | Nit | The stylesheet lists installed language ids/versions unauthenticated. | Accepted — same class as `/plugin-icons`. |
| 6 | Nit | de help wording "Schrift" vs "Schriftart" ambiguous. | **Fixed**: "Schriftsystem". |

Reviewer re-verified the TDD claims: with the non-test changes reverted the
4374 tests fail for the right reason (undefined symbols; languard 403;
app.css stack and all six templates named), and pass restored.

## Verified beyond unit tests
Driven run of the built binary (throwaway data dir, `UT_AUTH=off`) with a
seeded `com.universaltill.language-th` bundle carrying a woff2 subset
(renamed system Loma, Latin glyphs kept on purpose), Chromium at 1024×600:
- `/`, `/login`, `/self-order`: CDP `getPlatformFontsForNode` reports Thai
  text drawn by the bundle font (`isCustomFont: true`), digits
  `12,345.67 EUR` by the system font; only `127.0.0.1` requested by the
  page; no console errors. The system has its own Thai font, so this also
  shows the bundle font beats system fallback.
- Control, plugin disabled: Thai falls back to system Loma, no web font
  loaded, the font URL 404s, the stack stays valid.
- Screenshot of the sale screen read: layout unchanged.
- Not looked at: dark theme, RTL locales, the Pi's WebKitGTK.

Gate: `go build`, `go vet`, `go test ./...` (every package ok;
`internal/plugins` needs >10 min in this 4-CPU container — passes with
`-timeout 30m`, unrelated to this change), `golangci-lint` 0 issues, CI build
guards (deadcode and shellcheck-version could not run locally: tool built
with an older Go / no shellcheck binary — no shell or dead code touched).

**Verdict:** safe to merge.
