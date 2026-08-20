# Code review — till surfaces cloud `registered_required` as an actionable message (ut-docs#704)

- **Date:** 2026-08-20
- **Repo:** universal-till
- **Branch:** `fix/704-till-registered-required`
- **Card:** universaltill/ut-docs#704 (p3, complexity:easy) — AC #2 (till-side surfacing)
- **Reviewer:** independent fresh-context subagent (Sonnet).

## What changed

`internal/pages/plugins_store_page.go` — the `/api/plugins/store/download`
handler gained a case: when the cloud returns a `marketplace.APIError` with
`Code == "registered_required"` (the new ut-cloud REST code from the companion
change), the till responds **403** with the i18n key
`plugins.install.error.registered_required`, mirroring the existing
`not_entitled`/`listing_not_found` cases. Without it, this fell through to the
generic `502 download failed: <err>` path with a non-localized message.

`web/locales/{en,ar,fa,tr}.json` — added
`plugins.install.error.registered_required` to all four shipped locales
(en base + ar/fa RTL + tr), with genuine translations.

`internal/pages/plugins_store_api_test.go` — added
`TestStoreAPI_DownloadRegisteredRequired_403`, stubbing a cloud server that
returns the exact 403 `registered_required` envelope and asserting the till
surfaces the i18n key at 403, not the generic 502.

## Verification

- `go build ./...` clean; `go test ./internal/pages/... ./internal/plugins/...` all pass.
- Guards: `guard-i18n.sh` (all locales match en.json), `guard-compliance-claims.sh`,
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh` — all pass.

## Review findings

No blockers, majors, or minors. Independent review confirmed the code string
`"registered_required"` matches exactly on both sides (ut-cloud emits it,
`marketplace.APIError.Code` decodes it), the new i18n key exists in all four
locales with identical spelling, ar/fa are real RTL-appropriate translations,
and `guard-i18n.sh` does a set comparison (no key ordering enforced, so the
insertion point between `not_entitled` and `not_found` is fine).

## Cross-repo note — language packs & merge ordering

Adding a core `en.json` key requires the external `ut-plugin-language-{de,es}`
packs to carry it, or `lang-pack-drift` (blocking on push to `main`) goes red.
Both packs were updated in the same effort with real German/Spanish translations
(`ut-plugin-language-de#…`, `ut-plugin-language-es#…`). Merge ordering is
strict: **core merges first** (a pack key core's `main` lacks is an "orphan"
that fails the pack's own CI), so core `main`'s `lang-pack-drift` check is
briefly red until the two pack PRs land — expected, self-healing on the next
push, and `lang-pack-drift` is deliberately a non-required check.
