# Code review — refund/return fiscal.sign.ask dispatch never carried the offline signal (ut-docs#1493)

- **Date:** 2026-09-04
- **Branch:** `fix/1493-refund-return-offline-signal`
- **Reviewer:** independent read via an Opus subagent (complexity:medium →
  reviewer runs at Opus, per the `reviewer`/`scrum-master` skills'
  model-routing table), no shared context with the implementation, run in
  an isolated worktree.
- **Verdict: SAFE TO MERGE.** One CI-blocking finding (stale docs-shots
  manifest) fixed before merge; one wording nit (an overclaiming code
  comment) fixed before merge; two informational findings accepted as
  pre-existing and out of this card's scope.

## What shipped

`dispatchFiscalSignAsk`'s known-offline short-circuit (ADR-0044 D1,
`internal/pages/fiscal_sign_hook.go`) already skipped the `fiscal.sign.ask`
cloud call entirely when `SaleInput.Offline` was true — but only the sale
path (`pos_api.go`'s `completeTender`) ever set that field. The refund
path (`POST /api/refund`) and the inventory-return path
(`POST /api/inventory/return`) never did, so a refund/return on a
known-offline till burned the full 3s `fiscalSignAskBudget` on a call
already known to fail, and its declaration landed as a generic
backend-timeout reason instead of the honest known-offline one. Documented
as a known gap in `ut-docs/reference/contracts/fiscal-sign-ask.md`'s
"Known-offline short-circuit" section since the ut-docs#1405 review that
found it.

- `refund_page.go`: `saleInput.Offline` set from the request's `offline`
  form field via the existing `formFlagTruthy` helper (same one
  `completeTender` already uses), before the existing
  `dispatchFiscalSignAsk` call.
- `inventory_api.go`: `ReturnRequest` gains an `Offline bool` JSON field;
  the form-encoded branch reads the same `offline` field via
  `formFlagTruthy`. `returnInput.Offline` set before dispatch.
- `refund.html`/`inventory.html`: a hidden `<input id="offline-flag"
  name="offline" value="0">` inside each form, mirroring `index.html`'s
  existing pattern — kept current by the *already-existing*
  `web/public/app.js`'s `updateOfflineFlag()` (`navigator.onLine` +
  window online/offline listeners); no new JS.
- New tests: known-offline short-circuit coverage for both paths
  (`TestRefundFiscalSignAsk_KnownOfflineShortCircuits`,
  `TestCreateReturn_FiscalSignAsk_KnownOfflineShortCircuits`), baseline
  approved-dispatch coverage for `CreateReturn`'s `fiscal.sign.ask` call
  (`TestCreateReturn_FiscalSignAsk_ApprovedHasNoMarker` — this dispatch,
  added by ut-docs#1405, had no dedicated test before this card), and
  template-render regression checks
  (`TestRefundPage_RendersOfflineFlag`, an addition to
  `TestInventoryPredictsDaysLeft`) confirming the hidden input actually
  renders on `GET /refund/{receipt}` and `GET /inventory` — a broken
  template edit could otherwise still return 200 with none of the POST
  tests noticing, since they build the request by hand.

## Review findings

**Fixed before merge:**

- **`guard-docs-shots.sh` was red at the pre-review commit** (green at its
  parent) — the guard hashes every file under `web/ui/**`, `web/public/**`
  and non-test `internal/pages/**.go`; this diff touched four of them, so
  the recorded `surface_sha256` in `web/help/img/manifest.json` drifted
  even though no topic prose or visible screen actually changed (both new
  inputs are `type="hidden"`). Fixed with `make docs-shots` (96
  screenshots regenerated, only 4 PNGs changed — `sell`×2 locales,
  `till-designer`, `invoices` — and those are pixel-level Chromium-version
  rendering noise the script itself warns about, unrelated to this diff's
  own pages; no `refund`/`inventory` topic exists to screenshot). New
  surface hash `1b36f305141b0d8b…` committed; only `topics.*` hashes are
  unchanged, confirming no manual prose went stale.
- **Comment overclaim in `refund_page.go`**: the original comment said the
  offline signal came from "navigator.onLine + the optional manual
  override" — not true on this page. `app.js`'s manual-override checkbox
  (`#offline-override`) only exists on `index.html`; `refund.html` and
  `inventory.html` deliberately don't get one (this card's own stated
  non-goal — the BA/Architect scope was auto-detected `navigator.onLine`
  only). Narrowed the comment to say so explicitly rather than adding the
  checkbox (out of scope) or leaving the doc wrong.

**Accepted as pre-existing, out of scope, no change made:**

- The inventory-return **HTML form** path (`#return-form`) has no line-item
  inputs and no JS to build them (`internal/pages/inventory_api.go`'s own
  comment: "Form handling for lines array would need custom parsing") —
  `CreateReturn` rejects any submit with zero lines, so a real browser
  return can only ever go through the JSON path today. The new
  `#offline-flag`/`formFlagTruthy` wiring on the form-encoded branch is
  therefore currently dead-but-harmless there; the JSON path (which the
  new `ReturnRequest.Offline` field serves) is the one that matters in
  practice. Pre-existing gap, not introduced or worsened by this card —
  noted so the new render-assertion test isn't mistaken for proof the
  HTML form path itself is usable end-to-end.
- `app.js`'s `htmx:configRequest` listener re-computing the flag on that
  same event fires too late to affect the request it's attached to
  (htmx gathers form values before firing the event) — freshness comes
  entirely from the load-time call plus the online/offline window
  listeners, which is sufficient since neither page uses `hx-boost`
  (confirmed: every navigation is a full page load, so the flag is always
  correct at render time). Pre-existing behavior, unaffected by this
  diff.

**Confirmed fine, no change needed:**

- `ReturnRequest.Offline`'s `omitempty` JSON tag: harmless, since the
  struct is decode-only (never marshaled) — `{"offline":false}` and an
  omitted field both decode to the correct `false` default either way.
- Both hidden inputs sit inside their `<form>` element, so htmx's
  form-triggered POST auto-includes them — no `hx-include` needed (unlike
  `index.html`, where the flag lives outside the pay buttons' own
  context).
- RTL/i18n: no impact — `type="hidden"` carries no text and no layout.
  `guard-i18n.sh` green.
- ADR-0048 hard-gate ordering is honored in both handlers (gate check
  before dispatch), and the new tests exercise the real route end-to-end
  with no gate stub — a gate refusal would 409 and fail them; the
  "got 1 invocation" assertion in the reverted-fix TDD check proves
  dispatch was genuinely reached, not short-circuited by an accidental
  gate block.
- Neither of the two recurring bug classes this pipeline watches for
  applies: nothing in this diff writes to disk.
- No help-manual topic update needed — nothing user-visible changed (both
  inputs are invisible); `guard-help-topics.sh` green.
- No real client/shop names, no literal secrets.

## Verification beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on every changed `.go` file
  — clean.
- `go test ./internal/pages/...` (the exact CI invocation, no `-race`) —
  green, ~58s, both before and after the post-review fixes.
- `go test $(go list ./... | grep -v '/internal/plugins$')` (the full CI
  `Test` step) — every package green.
- A `-race` run of `./internal/pages/...` alone hit the package's
  documented 600s-timeout-is-not-a-real-hang artifact in this sandboxed
  environment (this repo's CI never runs `-race` at all — confirmed by
  grepping `ci.yml`); re-run with an extended 25-minute timeout completed
  clean in ~777s, confirming it was sandbox slowness under the race
  detector, not a real hang or a regression from this diff.
- All plausibly-relevant CI-blocking guards run and green:
  `guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-page-http-error`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots` (after the fix above), `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `guard-e2e-fixtures-import`, `check-brand-assets.sh`,
  `guard-makefile-version`.
- **TDD claim independently re-verified by the reviewer, not just
  asserted:** reverted only the two `Offline:`/`req.Offline =`
  assignments to `false` in an isolated worktree (tests and templates
  untouched), reran the two known-offline tests, confirmed both fail with
  the exact predicted assertion ("...must never dispatch to the signer,
  got 1 invocations" — proving the real dispatch was reached, not blocked
  by the fiscal gate), then restored with `git checkout --` and confirmed
  all five new/touched tests pass again.

Refs: ADR-0044, ut-docs#1405, ut-docs#999, ut-docs#1493.
