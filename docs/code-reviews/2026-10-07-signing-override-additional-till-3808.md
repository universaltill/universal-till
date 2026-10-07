# Review — signing override refused on an additional till (ut-docs#3808)

**Date:** 2026-10-07 · **Lane:** lane:cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

- `POST /api/fiscal/signing-override` (`internal/pages/fiscal_api.go`) now
  refuses with **409** on a till that follows a main till
  (`tillFollowsMain`), after the ADR-0048 never-configured refusal and
  before any session/PIN check. The `fiscal.signing_override_*` keys are
  shop-wide and the admin bundle is main-till-wins, so a local grant there
  was silently reverted by the next pull (and the main-till write-through
  refuses a non-empty override by design). Option (a) from the card:
  fail-closed, smallest change, same shape as `tseRequireMainTill`.
- The refusal uses the existing translated key
  `settings.error.change_on_main_till` — no new `en.json` key, so no
  language-pack follow-ups.
- The "known gap" `settings-write:allow` annotation is replaced with a
  main-till-only annotation (`TestSettingsWriteGuard_NoDirectShopWideWrites`
  still enforces it).
- Help: `web/help/{en,de,tr,ar,fa}/sell.md` — one sentence: grant the
  override on the main till; every till follows it.

## Tests

- New `TestFiscalOverride_RefusedOnAdditionalTill`: owner session and
  cashier+owner-PIN both get 409 with the translated message (en and fa),
  nothing stored, no `fiscal_override` audit row, tender stays blocked.
- TDD verified twice (author and reviewer): without the guard the test
  fails (200 granted); with it, it passes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1–3 | nit | de/ar/fa help used non-standard words for "additional/main till" | Fixed ("beigetretene Kasse", "الصندوق الأساسي/المنضم", "صندوق پیوسته") |
| 4 | nit | An additional till cut off from its main till with a failing TSE can no longer lift the block locally until it reconnects | Accepted: the chosen fail-closed option. Before this change the local grant only "worked" because no pull arrived. The gate still reads local settings only, so offline-first is intact |
| 5 | nit | A half-enrolled till (primary_url set, no bearer) is refused too | Accepted: same as `tseRequireMainTill` |
| 6 | — | Generic copy "Change this setting on the main till." | Accepted: the API is not used by any UI form, and the key is translated everywhere |

The reviewer checked for side doors: `/api/settings/upsert` and
`/api/sync/settings/apply` refuse a non-empty override, and cloud
`set_till_setting` excludes fiscal keys. It also confirmed that a
main-till grant reaches additional tills (fiscal.* is shop-wide in
`DumpAdmin`, and `EvaluateGate` reads the pulled row).

## Gate

gofmt clean, `go build`/`go vet` clean, full `go test ./...`, guards: i18n,
help-drift, help-topics, compliance-claims, settings-write (Go test).

**Verdict:** safe to merge.
