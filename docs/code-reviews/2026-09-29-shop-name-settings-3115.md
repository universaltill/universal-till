# 2026-09-29 — Shop name editable in Settings (ut-docs#3115 slice A, + ut-docs#3020)

Branch `feat/3115-shop-name-settings`. Lane `lane:cloud-24`. Built by Opus 5.5 (Dev subagent);
reviewed by Fable in an isolated worktree.

## What shipped

- **Settings → My shop → Shop name** card (`#settings-store-name`, first in the My shop
  category, uislot `settings-store-name`). `POST /api/settings/store-name`:
  - validates before the elevation gate with the new `config.NormalizeStoreName`. It
    mirrors ut-cloud's `claims.NormalizeStoreName` check for check: UTF-8, trim, 1–80
    runes, no control / bidi override-isolate / invisible Cf characters (ZWNJ/ZWJ
    allowed, not alone). It also refuses placeholders ("My Store", "Universal Till
    store"), so anything the till accepts, the cloud accepts too;
  - a refusal is a 400 `text/html` fragment in the card's own `#store-name-msg`, not the
    page-wide error banner (app.js ut-docs#916 pattern);
  - `settings` permission; a cashier gets the in-place PIN elevation;
  - saves through `saveShopSettings`, so an additional till forwards to the main till and
    is refused when the main till is unreachable;
  - audit `settings / store.name / store_name_changed {old, new}`; an unchanged name is a
    no-op on a till that decides for itself.
- **Self-order kiosk reads the shop name live** (ut-docs#3020): `kioskShopName` reads
  `store.name` and falls back to the boot-time `cfg.StoreName` while the setting is unset
  or still a placeholder. Receipts and reports already read it live.
- i18n: 6 new keys (`settings.store_name.{title,help,error_required,error_too_long,error_invalid_chars}`,
  `elevation.summary.store_name`) in en/ar/fa/tr. The de/es packs follow in
  `ut-plugin-language-{de,es}` branch `i18n/3115-store-name`.
- User manual: `web/help/{en,de,tr,ar,fa}/display.md` gets a new step 9, "Settings → Shop
  name"; the steps after it are renumbered. Topic hashes are refreshed. No screenshotted
  pixel changes (the only `/settings` shot is the category grid), so there is a surface-hash
  refresh (`Docs-Shots-Unchanged: true`) instead of new PNGs.
- `demo_mode.go` allows the route. Demo tills are per-visitor throwaways (ADR-0113), so this
  is the same call as till-name.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Kiosk header kept the boot-time `cfg.StoreName` after a rename (existing gap, now routine) | **Fixed**: `kioskShopName` + `TestSelfOrder_ShopNameIsReadLive` (failed first: "after a rename the kiosk must show Corner Bakery"). Placeholder fallback keeps `UT_STORE_NAME` and `TestSelfOrderPage_ServesAnonymousRequest` working. Folds in #3020. |
| 2 | minor | On an additional till the no-op check compared against a possibly stale local mirror, which could silently drop a rename | **Fixed**: short-circuit only when `!tillFollowsMain`. `TestSettingsWriteThrough_StoreNameForwardsEvenWhenMirrorMatches` failed first ("main till calls = 0, want 1"). `old` in the additional till's audit is documented as its mirror's value. |
| 3 | minor | No forward-path test | **Fixed** by the test in #2 (lands on main, mirrors back, main-side `setting_changed_via_till` audit). |
| 4 | minor | A failing elevated retry on an additional till gives no feedback | **Accepted**: identical to the shop-type/till-name/staff-languages precedent; not new. |
| 5 | nit | `maxlength="80"` counts UTF-16 units, the server counts runes | Accepted: harmless (only stricter for astral characters). |
| 6 | nit | `ask_api.go` / `setup_page.go` still use the literal `"store.name"` | Accepted; left for a later tidy-up. |
| 7 | nit | Cloud accepts "My Store", the till refuses it | Accepted: intentional, one-directional. |

Reviewer verified with no problem found: Unicode parity with ut-cloud; escaping in the card,
the elevation summary, the hidden field and the error fragment; the elevation retry flow with
the HTML error fragment; the help renumbering across all five languages; the demo entry.

**TDD re-verified by the reviewer** (worktree, source reverted): config and pages fail to
compile without the source. With only the constant restored, all six handler/card tests fail
(404 / card missing). Restored, they pass. The two tests added after review were each run
against the unfixed code (failures quoted above) and then against the fix.

## Gate (after the last edit, sequential)

`gofmt -l .` empty · `go build ./...` · `go vet ./...` · `go test ./...` exit 0 ·
`golangci-lint run ./...` 0 issues · every `scripts/ci/guard-*.sh` in `ci.yml` passes except
two that can't run in the cloud sandbox:
- `guard-shellcheck-version.sh`: no shellcheck binary; no scripts touched.
- `guard-deadcode-baseline.sh`: flags `internal/logging` `Stderr`/`timestampWriter.Write`.
  They are reachable only from `cmd/unitill-desktop`, which the sandbox skips (no GTK
  headers). The branch doesn't touch `internal/logging`; CI analyses all roots.

Playwright: `store-name-3115.spec.ts` (rename + reload, blank refusal) and
`self-order-brand-mark-298.spec.ts` pass; the Dev also ran `staff-languages-3086` and
`settings-osk`.

## Verified beyond automated tests

The Dev looked at Chromium screenshots of Settings → My shop:
- the normal and blank-name error states at 1024×600 and 360×800, in en, de (longest) and
  fa (RTL);
- the on-screen keyboard forced on: `inputmode="none"`, one keyboard only.

No clipping; the error sits inside the card; no global banner. **Not checked:** real touch
hardware (emulated touch only), the dark theme, and a live additional till with its main till
unreachable (covered by the shared `saveShopSettings` tests).

## Deferred / follow-ups

- #3246: the till → cloud half (check-in report, last writer wins, cloud-side audit,
  round-trip tests).
- New card: the staff-languages card's plain-text 400 still raises the page-wide banner
  (same ut-docs#916 fix).
- The setup wizard still checks only trim + placeholder, not `NormalizeStoreName` (noted on
  #3116).

**Verdict: safe to merge.**
