# Code review: order-type prompt placement setting (ut-docs#2282)

**Date:** 2026-09-16
**Card:** universaltill/ut-docs#2282 — "Sell screen: setting to choose WHERE
dine-in/takeaway is asked — top of basket, before the first item, or at Pay"
**Complexity:** medium
**Implementer:** Sonnet (Dev subagent)
**Reviewer:** Opus (independent subagent, isolated worktree, fresh context)

## What shipped

A new setting `sale.order_type_prompt` (`top` default / `before_item` /
`at_pay`) controlling when the cashier is asked dine-in vs. takeaway.
`top` preserves today's always-visible basket-top toggle unchanged
(existing shops see no behavior change until they opt in). `before_item`
intercepts the first item add to an empty basket; `at_pay` intercepts the
Pay action, if not yet chosen — both via a new `#order-type-prompt-modal`
reusing the existing `basket.order_type.dine_in`/`takeaway` locale keys and
posting to the existing `/api/pos/order-type` endpoint.

**A scope change is folded into the same PR**: the product owner withdrew
the per-line cashier control entirely (ut-docs#2282 comment 13:24 UTC,
ut-docs#2309) — order type is sale-level only now. The per-line HTTP
endpoint (`POST /api/pos/line-order-type`) and cashier markup
(`.line-order-type`, `.order-type-mixed`) are removed; the domain layer
(`sale_lines.order_type`, `tax.rate.ask`, `SetLineOrderType`,
`SummarizeOrderType`, `NormalizeLineOrderType`) is kept intact for history,
DSFinV-K/reporting and any resumed/LAN-synced sale predating this change.
**Document-first companion PR**: `ut-docs`#2332 amends ADR-0073 to record
this withdrawal (ADR-0073 Decision 2 previously mandated the now-removed
control; Decision 1's data model is unaffected).

Key files: `internal/data/pos_repo.go` (KV constants), `internal/pages/settings_page.go`
(new endpoint + generic-upsert parity), `internal/httpx/httpx.go` +
`web/ui/layouts/base.html` + `internal/pages/init.go` (live-published prompt
mode), `internal/pos/service.go` + `internal/pos/hold.go` (`OrderTypeChosen`
flag), `web/ui/partials/basket.html`, `web/public/app.js` (interception
logic), `web/ui/pages/index.html` + `web/public/app.css` (new modal),
`internal/pages/pos_api.go` (per-line handler removed), `internal/uislot/slot.go`
+ `web/ui/pages/settings.html` (new settings card), locale files (en/ar/fa/tr;
de/es untouched), `README.md`, help docs + `sell`/`display` screenshots.

## Independent review — verdict: PASS-WITH-FIXES

Spawned as an Opus subagent in an isolated git worktree with no visibility
into the Dev subagent's own reasoning. Findings:

1. **BLOCKING (fixed)**: removing the per-line handler left
   `pos.Service.SetLineOrderType` reachable only from tests, which
   `guard-deadcode-baseline.sh`'s `deadcode -test=false` scan reports as
   unreachable — a hard CI failure as submitted. Fixed by adding the
   expected entry to `scripts/ci/deadcode-baseline.txt` (this is the
   guard's own documented shape for a test-only-reachable function kept
   deliberately, not a defect) plus a doc comment on the method explaining
   why.
2. **SHOULD-FIX (fixed)**: no test pinned that the removed endpoint is
   actually gone — a re-added handler with no template would have passed
   the whole suite. Added `TestLineOrderTypeEndpointIsGone_SaleLevelEndpointRemains`
   asserting a 404 and that the sale-level endpoint still works.
3. **Document-first gap (addressed via a companion PR, not this one)**:
   ADR-0073 Decision 2 is contradicted with no superseding/amending ADR.
   `ut-docs`#2332 lands the amendment (see that PR's own review record).
4. **Noted, correctly out of scope**: ut-docs#2309's remaining
   acceptance criteria (kitchen-label collapse, further test rewrites,
   removing the now-truly-orphaned `mixed`/`all_dine_in`/`all_takeaway`
   keys) are deliberately left alone here — `mixed` remains a real,
   reachable state for a sale resumed or LAN-synced from before this
   change, and all three keys still have live template usages (verified:
   5/1/1 references). ut-docs#2309 stays open as the follow-up.
5. **Noted, not changed**: a defensive gap in `showOrderTypePromptModal`'s
   fallback path (calls `onChosen()` immediately if the modal element is
   absent) is unreachable on every current sale-screen surface (all four
   `/api/pos/scan*` fragments always carry the modal) — flagged for
   awareness if a future page reuses those fragments without it, not
   fixed as speculative hardening of dead code.

### TDD re-verification performed personally
- **JS interception fix** (`htmx.ajax` with values snapshotted at
  confirm-time, working around a stale-detached-`#basket`-node bug the
  same class as ut-docs#1337, plus an unrelated ut-docs#1177 listener
  clearing the scan-row field on the same native `submit` event): reverted
  to a naive `evt.detail.issueRequest()` → the `before_item` e2e test
  failed for real (`Coca-Cola` never appeared in `#basket`, the swap landed
  in a detached node) → restored → 5/5 e2e tests pass. Reverted again with
  values read late (inside the callback) → same real failure, confirming
  the second half of the claim independently. Read the vendored
  `htmx.min.js` to confirm the actual swap-then-`afterRequest` ordering
  that makes the fix work and rules out a double-prompt/infinite-loop risk.
- **Per-line removal, Go level**: re-registered the deleted handler →
  the new regression test failed with a real 200-not-404 error → restored
  → passes. Removed the new deadcode-baseline entry → the guard's own
  `comm -23` comparison reproduced the exact CI failure → restored →
  clean. Confirmed the full must-keep list (`sale_lines.order_type` in
  both `001_init.sql` and `026_kiosk_counter_orders.sql`, `tax.rate.ask`'s
  `OrderType` field, `SummarizeOrderType`/`NormalizeLineOrderType` still
  exercised by their existing, unmodified test files) is genuinely intact.

### Checklist — all verified PASS
Default (`top`) preserved for existing shops (four independent checks: no
migration, KV-unset fallback, template default, JS fallback); `htmx:confirm`
gating correctly re-reads fresh state each time with no double-prompt or
recursion risk; offline-first preserved (the modal is a business-logic
prompt in the same category as `#hold-modal`, only request is the existing
local `/api/pos/order-type` endpoint); kiosk isolation clean
(`guard-kiosk-engine.sh` plus manual confirmation self-order fragments
never load `app.js` or render `basket.html`); i18n complete across
en/ar/fa/tr with orphaned per-line keys removed from all four, de/es
untouched; no new hardcoded RTL-unsafe CSS; no `autofocus` on the modal's
choice buttons; settings persistence byte-for-byte parity with the sibling
`order-no-scheme` pattern including the ut-docs#2121 live-republish lesson;
no raw SQL outside the repository layer; help manual and README genuinely
updated (screenshots opened and visually confirmed, not just guard-passed).
Also confirmed the card's "must-not-regress": a resumed held/open sale
never re-prompts (`RestoreHeld` sets `orderTypeChosen = true`, and that is
the only restore call site in the codebase).

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` clean.
- Full `go test ./...` — green, both before and after the reviewer's fixes
  and after merging `main` a second time.
- `golangci-lint run ./...` — 0 issues.
- `guard-data-access`, `guard-kiosk-engine`, `guard-i18n`,
  `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`,
  `guard-help-drift`, `guard-page-http-error`, `guard-plugin-menu-read` —
  all green.
- e2e `order-type-prompt-placement-2282.spec.ts` — 5/5 pass; a broader
  510-test `--project=default` regression sweep run by the reviewer — all
  pass.
- `guard-deadcode-baseline` and `shellcheck`/`guard-shellcheck-version`
  could not run to completion in this sandbox (no GTK/WebKit pkg-config
  headers, no `shellcheck` binary) — the deadcode guard's *logic* was
  independently reproduced with a raw `deadcode` invocation (see Finding 1)
  to confirm the fix before this environment gap made the wrapper script
  itself fail; both gaps are pre-existing and unrelated to this diff (real
  CI on `ubuntu-latest` has both).

## Safe to merge

Yes, once `ut-docs`#2332 (the ADR-0073 amendment) has landed —
`merge_method: "merge"` per this repo's standing rule (ut-docs#250).

## Deferred (tracked elsewhere, not blocking)

- ut-docs#2309: kitchen-label collapse, remaining test rewrites, and
  removing the now fully-orphaned `mixed`/`all_dine_in`/`all_takeaway`
  keys once no resumed pre-change sale can still produce them.
