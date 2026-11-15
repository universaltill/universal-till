# Code review — iOS: no plan purchase entry point (ut-docs#3434)

Date: 2026-11-07 · Repo: universal-till · Built by: Sonnet agent · Reviewed by: independent Opus 5.5 subagent (diff `origin/main...HEAD` in a throwaway worktree).

## What shipped

- `internal/pages/plan_purchase.go`: seam `planPurchasePlatform = runtime.GOOS` (same pattern as `bluetoothPlatform`), `planPurchaseAllowedOn(goos)` (false only for `ios`), `planPurchaseAllowed()`, and `refusePlanPurchase`, which answers 404.
- The three panel endpoints (`POST /api/subscription/panel`, `/panel/handoff`, `/panel/poll`) call `refusePlanPurchase` as their first statement. That is before `ParseForm`, the PIN elevation prompt, the cloud check-in, `settingsAudit`, the QR code and `enroll.PlansURL`.
- `subscriptionView.NoPlanPurchase`: `CanRenew` is false on iOS (Settings card, Check-now re-render). On iOS the status-bar chip's `openPanel` is false, so the chip is the plain `/settings#subscription` link.
- iOS-only wording keys, each with complete `en.meta.json` metadata:
  - `subscription.banner.lapsed.fix_ios`
  - `subscription.banner.lapsed.fix_via_main_ios`
  - `subscription.check_result.still_lapsed_ios` (max_length 80)
  - `tills.join_error.unsent_day_closes_refused_ios` (placeholder described)
- `internal/manual`: `<!-- plan-purchase -->…<!-- /plan-purchase -->` blocks are pre-rendered both ways at parse time. `Topic.WithoutPlanPurchase()` and `Library.SearchFor(locale, q, planPurchase)` are chosen per request, so the search text is never baked in at startup. `help_page.go` uses both on iOS. `Markdown` stays raw.
- `claim.md` (en, de, ar, fa, tr): the Renew/Change-plan paragraph is wrapped in markers. The "Subscription ended" bullet and the chip sentence are reworded so they read on both platforms.
- Desktop and Android are unchanged; `TestPlanPurchaseNonIOS_Unchanged` pins this.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | Low | Marker parser: a near-miss marker pair (`<!--plan-purchase-->`, `<!-- plan-purchase: iOS -->`) is not recognised. goldmark renders it as an omitted HTML comment, and the purchase paragraph shows on iOS with nothing reported. Unbalanced and nested markers were already refused; a malformed pair was not. | **Fixed in review.** `splitPlanPurchase` now refuses any line whose trimmed text starts with `<!--` and contains `plan-purchase` unless it is exactly the opener or closer. `guard-help-topics.sh` parses every topic, so CI catches it. The new `TestPlanPurchaseBlock_Unbalanced` cases "misspelt pair" and "with text" failed before the fix and pass after it. Applied before commit. |
| 2 | Low | `base.html` still emits the closed `#subscription-panel` dialog on every page on iOS: the title "Renew or change plan" and the loading template. Nothing on iOS opens it: there is no `data-sub-panel-open` element, and the endpoints return 404. It is never visible, only present in the DOM. | Accepted. Optional hardening: gate the dialog behind a `planPurchase` template func. |
| 3 | Info | The parser is line-based and does not know about code fences. A marker line inside a fenced block would be consumed. No topic does this today. | Accepted. |
| 4 | Info | htmx does not swap a 4xx response, so a 404 from the panel would leave the panel's loading text showing. On iOS no element can issue that request, because the platform is fixed at build time. | Accepted, not reachable. |
| 5 | Info | English help says Check now shows "that it is still not active". On desktop the result line reads "Still not renewed…". This is a paraphrase that fits both platforms. | Accepted. |

Hunted with nothing found:
- No remaining iOS opener in templates. `CanRenew` is set in only 2 places, both gated. `openPanel` is gated.
- No `web/public/**/*.js` code opens the panel or links to plans.
- `enroll.PlansURL` has one call site, after the guard.
- The setup wizard has no plan UI.
- The plugin store shows a badge only.
- In `en/` help, no other topic tells the reader to renew, buy or pay.
- `claim` front-matter keywords are neutral.
- CRLF is handled (`TrimSpace` strips `\r`).
- The banner's `.sub` is always a `subscriptionView`. Every construction goes through `subscriptionViewFor`, which sets `NoPlanPurchase`.
- Recurring bugs: no file writes or data paths are touched, so `os.MkdirAll` and `paths.Data` do not apply.

## Extra verification (run by reviewer)

- `go build ./...` passes.
- `go vet ./internal/pages/... ./internal/manual/...` is clean. `gofmt -l internal/` prints nothing.
- `go test ./internal/pages/... ./internal/manual/... -count=1`: all packages pass, before and after the review fix.
- Guards, all passing: guard-i18n, guard-i18n-meta, guard-help-topics, guard-help-terms, guard-help-breadcrumbs, guard-rtl-logical-css, guard-no-showmodal, guard-core-neutral, guard-fixed-dates. guard-help-drift reports advisory warnings only.
- TDD claims re-verified. I reverted three guarded production lines together, ran `-run TestPlanPurchase`, restored the lines and saw the tests pass again:
  1. Removed `refusePlanPurchase` from `POST /api/subscription/panel`. `TestPlanPurchaseIOS_PanelEndpointsRefuse` failed: "= 200, want 404", the choices, renew and choose controls leaked, and "panel audit rows = 1, want 0".
  2. Removed the `.sub.NoPlanPurchase` branch from `subscription_paused_banner.html`:
     - `TestPlanPurchaseIOS_SettingsHasNoOpener` failed: iOS wording missing, and the banner still told the reader to renew.
     - `TestPlanPurchaseIOS_ViaMainBanner` failed: the renewal wording was still there.
  3. Removed `WithoutPlanPurchase()` from `renderHelpPage`. `TestPlanPurchaseIOS_HelpClaimHasNoPurchase` failed in English and German.

## Earlier gates (Tester, UX)

- **Tester:** 17 mutations of the guarded production lines; 16 were caught at once. The one that survived was help search (its `q=QR` check could never fail). That check was replaced with a `q=Escape` assertion on both platforms and re-verified: it now fails when `SearchFor(…, true)` is forced. Driven run: the real binary with Playwright against a fake lapsed cloud. On the linux build the renew button, panel, QR code (`/plans?intent=renew`), chip and help paragraph are unchanged. A build with `-X …planPurchasePlatform=ios` showed no openers, the iOS wording, the chip as a Settings link, 404s from the panel endpoints and the help topic stripped. There were no console errors. Not run on a real iOS device or simulator.
- **UX §7 gate:** first a fail with one required fix. The de/tr/ar/fa `claim.md` still had "renew it in your account" and "opens the panel described below" outside the marker block. These were reworded to match the English, and the help test now checks every locale for the old phrases; it fails when one is restored. The `still_lapsed_ios` copy and English help were also changed to "still not active", as advised. Chip touch target: 36px on every platform, the same as before this change, left for a separate card.

## Verdict

**Approve.** No blocker or medium findings. The finding 1 fix is applied in this commit.

## Deferred

- Optional: gate the hidden `#subscription-panel` dialog markup in `base.html` on iOS (finding 2). Backlog card if wanted.
- Plugin marketplace on iOS stays under #3433.

Pre-flight:
- Platform/feature claim (iOS): `universal-till/README.md` n/a (no plan-purchase claim changes). `ut-website` n/a (it is not the iOS app; nothing there is iOS-specific). `web/help/` topic updated (`claim.md` in all 5 locales, with markers).
- Till user-visible behaviour: `web/help/` updated. `make docs-shots` n/a (docs-shots render desktop, and the desktop UI is unchanged). ut-docs `reference/subscription-plans.md` gains an iOS subsection (ut-docs branch `docs/3434-ios-no-plan-purchase`). README n/a.
- New `en.json` keys: `en.meta.json` updated (4 keys, guard-i18n-meta passes).
- Plan/price/tier: n/a (no prices or tiers changed).
- Review record: this file.
- Follow-up: the deferred item above.
