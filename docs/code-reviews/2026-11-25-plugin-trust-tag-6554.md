# Review: translated trust tag on the plugin card (ut-docs#6554)

Branch `fix/6554-plugin-trust-tag`. Files: `internal/pages/plugins_page.go` (`installedTrustKeys`,
`trustLabel`/`trustHint` per card), `web/ui/pages/plugins.html`, `web/locales/en.json` +
`web/i18n-meta/en.meta.json` (`plugins.trust.revoked`, `plugins.trust.revoked_hint`),
`internal/plugins/update_checker.go` (doc comment), `web/help/en/plugins.md`, docs-shots for `plugins`,
tests `plugins_page_test.go`, `plugin_api_import_trust_test.go`, `update_checker_test.go`.

Pre-flight: Any change: review record = this file; help = `web/help/en/plugins.md` + `make docs-shots`;
en.json keys + meta = yes (English only, ADR-0160); migrations/catimport = n/a; follow-ups → new Triage card
(see below).

## What the card asked, and what shipped

1. **Raw trust value on the card.** The Plugins page tag rendered `plugins.trust_level` verbatim
   (`untrusted`/`trusted`/`system`). It now shows the store badge's words: `system` → "Universal Till",
   `trusted`/`verified` → "Verified", `revoked` → new "Not trusted", anything else (empty, unknown, padded)
   → "Unverified", with the matching hint as tooltip. Translated server-side; the raw `trust` stays in the
   payload. A label is never upgraded: the mapping is an exact match, and `system` is written only by
   `bundled.Sync` / `builtinlayouts` (reviewer traced every writer of `trust_level`).
2. **File import accepts any trust_level.** Already fixed on `main` (`plugin_api.go:1080`, allow-list
   untrusted|verified|trusted). No test pinned it; `TestImportFromFile_RefusesUnknownTrustLevel_6554` now does
   (`system`, `official`, `"Trusted "`, `revoked` → 400 "invalid trust_level").
3. **`UpdateInfo.TrustTier` never read.** It is read: `GET /api/plugins/check-updates` serialises it.
   Kept (no response change), documented, and `update_checker_test.go` asserts it carries the catalogue tier.

## Phases

Sonnet built (Dev); BA/Architect/UX design ran as one session-model subagent (easy card, compressed to fit
the 45-min budget; noted here); Tester was a separate subagent; independent review by Opus 5.5 (different
model from the author).

Tester: mutation checks — reverting the template, the label, the mapping, the import allow-list or the
TrustTier copy each fails a named test. `gofmt`, `go build`, `go vet`, `golangci-lint` (pages, plugins) clean;
`go test ./internal/pages/...` pass; 184/193 `build`-job guards pass, the rest environment-only (catimport
tags, shellcheck binary, nightly-assets, a helper) plus docs-shots (fixed by regenerating). Driven run of the
real binary: /plugins at 1024×600 and 360×800 (en) and RTL fa — tags read Not trusted / Universal Till /
Verified / Unverified, tooltips match, no raw words, no overflow. Not checked: till dark theme, real
German/Persian for the new key, touch hardware.

## Review findings (Opus 5.5) — verdict: ship

- **Low** — a manager can set `trusted`/`verified` without marketplace verification (file import API with
  `trust_level=trusted`, or `POST /api/plugins/trust`); the card then says "Verified, registered developer".
  Barely a regression (the raw word showed before), manager-only. → follow-up card.
- **Nit** — mapping normalised case/space, so " System " mapped to official. **Fixed:** exact match, test now
  pins " System " → unverified.
- **Nit** — `max_length` 16 on `plugins.trust.revoked` too tight for German. **Fixed:** 24.
- **Nit** — help names "Not trusted", which only the API can set. Accurate; left.
- **Nit** — de/tr/ar/fa help topics keep the old words; Weblate's job (ADR-0160). Left.

Follow-ups (new Triage card): manager-set trust levels need their own hint or imports stay untrusted only;
`UpdateInfo` has no json tags, so `check-updates` emits PascalCase keys (snake_case rule).
