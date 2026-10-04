# Review: payment-plugin tender fails closed when the plugin declares a hook it cannot answer (ut-docs#3648)

- **Card:** ut-docs#3648 (filed P2, but the same money-integrity family as
  #3633/#2278/#2280/#2304/#368). Found while building #3633's own
  regression coverage; #3633 was closed in the meantime by a different PR
  (universal-till#1699) that proved the *unconfigured-but-loaded* case was
  already safe and filed #3648 for the *never-loaded* case as a follow-up,
  which this PR resolves.
- **Branch:** `test/3633-unconfigured-payment-plugin-fail-closed`, reviewed
  at `1002fdb`, then rebased onto `origin/main` after #1699 landed. Two
  test cases this PR originally added (unset-secret-declines,
  configured-approves) were dropped from
  `internal/pages/pos_tender_unset_secret_test.go` post-rebase as redundant
  with #1699's `pos_api_wasm_payment_test.go`, which already covers that
  exact scenario. Only the genuinely new case — the broken/unloaded-plugin
  one #3648 is about — remains, renamed to avoid identifier collisions with
  the landed file: `TestTender_RealWasmPaymentPlugin_ModuleMissingDeclinesAndRecordsNoSale`.
- **Built by:** Opus (Dev) — **reviewed by:** Fable 5.1 (independent model, isolated worktree)
- **Date:** 2026-10-04

## What shipped

`blockingPaymentEventDispatch` (`internal/pages/refund_page.go`) used to treat
"no live subscriber for `payment.<key>.authorize` / `.refund`" as "nothing to
check" for every payment entry. That is right for a hook-less, post-settle-only
method (qrpay-shaped), but wrong for a plugin that *declares* the hook and
simply is not loaded — module missing / `install_state='broken'` (#368), WASM
runtime down, or a `Sync` mid-resubscribe. In that state a card tender
completed the sale and printed the receipt without ever asking the provider.

The fix, inside the existing `!bus.HasSubscribers(event)` branch:

1. `PluginRepo.HasActiveHook(ctx, e.PluginID, event)` (pre-existing repo
   method, `internal/data/plugin_repo.go:723`) — declared and active ⇒ return
   an error (fail closed); undeclared ⇒ the old `(nil, false, nil)` pass-through.
2. A lookup error also fails closed, matching the ut-docs#2278 rule already
   applied two lines above for `ListPaymentEntries`.

No new SQL, no new mechanism, no new UI, no locale keys. The existing decline
paths carry it: `paymentDeclinedError` → 402 + `pos.toast.payment_declined` on
tender; `refund.error.provider_declined` → 402 on refund; a Problems-ring
warning on the ADR-0136 reversal path.

Files:

- `internal/pages/refund_page.go` — the 17-line gate change + doc comment.
- `internal/pages/payment_event_test.go` — the old "no subscriber ⇒ no gate"
  assertion (which encoded the bug) inverted; a hook-less `qrpay` entry added
  and asserted as pass-through for both `.refund` and `.authorize`.
- `internal/pages/pos_tender_unset_secret_test.go` (new) — one real-chain
  test for #3648's own scenario: a real wasip1 guest compiled at test time,
  its module file deliberately absent so the real `WasmRuntime.Sync` marks
  the plugin broken and never subscribes it, tendered through the real
  `POST /api/pos/tender`. (The unset-secret-declines/configured-approves
  cases this file originally also carried are now #1699's
  `pos_api_wasm_payment_test.go`, landed on `main` while this branch was in
  flight — dropped here as duplicates after the rebase.)
- `internal/pages/testdata/unsetsecret_guest/main.go` (new) — the guest:
  reads `stripe_secret_key` through the real `settings_get` host fn, exit 2
  when unset/empty, exit 0 (approve) when set. (Shared source; #1699 also
  compiles it for its own, different, scenario.)

## Independent verification (not taken from Dev/Tester reports)

### TDD re-derivation

Reverted only `internal/pages/refund_page.go` to `1002fdb~1` (tests kept),
ran the module-missing test, restored, re-ran:

```
=== REVERT production fix (keep tests) ===
--- FAIL: TestTender_RealWasmPaymentPlugin_ModuleMissingDeclinesAndRecordsNoSale (0.36s)
    pos_tender_unset_secret_test.go:191: want 402 (declined) for an unapproved
    plugin tender, got 200: <div class="basket receipt-view" id="basket"> …
      (the 200 body is the rendered journal: receipt 000000001, £1.20, tender "stripe")
FAIL	github.com/universaltill/universal-till/internal/pages	0.402s
=== RESTORE fix ===  worktree == 1002fdb
--- PASS: TestBlockingPaymentEventGate (0.34s)
--- PASS: TestTender_RealWasmPaymentPlugin_ModuleMissingDeclinesAndRecordsNoSale (0.02s)
ok  	github.com/universaltill/universal-till/internal/pages	4.552s
```

Re-run after the post-#1699 rebase and test-file trim (dropping the two
cases now owned by #1699, renaming identifiers to resolve the collision)
reproduced the same result — full output in this card's close-out comment.
All of `internal/pages` and `internal/plugins` (non-race) stayed green
alongside #1699's own tests, confirming no regression from either PR on
the other.

The failure without the fix is the real bug (a sale row and receipt for a
card tender nobody authorized), not a compile error or a fixture quirk. The
positive control (configured secret ⇒ 200, exactly one sale) proves the
decline tests cannot pass for the wrong reason (guest never loading, hook
never wired, handler refusing every plugin tender).

### Gate (in the worktree)

| Check | Result |
|---|---|
| `go build ./...` | OK |
| `go vet ./...` | OK |
| `gofmt -l internal/` | empty |
| `scripts/ci/guard-data-access.sh` | ✓ no inline SQL outside `internal/data` / `internal/db` |
| `scripts/ci/guard-i18n.sh` | ✓ (no new keys; `pos.toast.payment_declined` and `refund.error.provider_declined` exist in all 4 locales) |
| `scripts/ci/guard-core-neutral.sh` | ✓ 547 files, no un-reviewed offender |
| `scripts/ci/guard-help-topics.sh` | ✓ |
| `go test ./internal/pages/... ./internal/plugins/...` (non-race) | all green — `internal/pages` ok 230s, `internal/plugins` ok 391s, plus catalog/catalogsync/common/itemsnav/settingsnav/builtinlayouts/marketplace/oauth ok (no pre-existing test depended on the fail-open behaviour) |
| `golangci-lint`, `guard-deadcode-baseline.sh` | not runnable in this container (tool binaries built with go1.25/go1.26 against a go1.27 module) — environment limitation, not a diff finding; CI runs both. The diff adds no new unexported production symbol, so `unused`/deadcode cannot regress. |

Race mode not re-attempted: pre-existing >30 min budget for these two
packages (Tester's report); the change adds one `QueryRowContext` on a branch
that already held no lock and introduces no goroutine or shared state.

### Logic read

- **Declared vs undeclared** — `HasActiveHook` reads `plugin_hooks WHERE
  plugin_id=? AND event=? AND is_active=1`, the same table and predicate
  `ListPluginHookEvents` uses when `Sync` builds the subscription list
  (`wasm_runtime.go:307`). So "declared" here is exactly "Sync would have
  subscribed it had the module loaded" — no drift possible between the two.
  `markBroken` only flips `plugins.install_state`; hook rows stay active, so a
  broken plugin is correctly seen as declared. `ListPaymentEntries` already
  filters `p.is_active = 1 AND pe.is_active = 1`, so a disabled or
  uninstalled plugin's method is still ungated (unchanged; its button is not
  offered either).
- **Event name** — `strings.TrimSuffix(e.TriggerEvent, ".requested") + "." +
  suffix` is the same derivation the bus is keyed on; the lookup uses that
  string verbatim.
- **Hot path** — the DB lookup runs only when there is no subscriber; a
  loaded plugin's tender pays nothing extra.
- **Generic** — keyed on `e.PluginID` from the entry row; no plugin-ID,
  vendor or country list (core-neutral, no ADR needed: no new mechanism).
- **Money** — the diff moves no amounts; payloads keep `amount.Minor()`.
- **Recurring bugs** — no production file write; the test writes under
  `t.TempDir()` with `os.MkdirAll` first; no cwd-relative data path.
- **Secrets / names** — `deadbeef`, `https://example.test/p.wasm`,
  `com.test.payment-brokenhook` are placeholders; no real shop or client
  name.

### The three call sites and the refund/reversal blast radius

| Call site | Before | After | Judgment |
|---|---|---|---|
| `completeTender` authorize (`pos_api.go:403`) | declared-but-unloaded ⇒ sale completes | `paymentDeclinedError` ⇒ 402, no sale, basket kept, cause logged server-side | the card |
| Refund gate (`refund_page.go:962`) | declared-but-unloaded ⇒ return recorded locally, provider never asked to send money back | `blocked != nil` ⇒ 402 `refund.error.provider_declined`, no return row | **correct and in scope** |
| ADR-0136 reversal (`pos_api.go:3034`) | declared-but-unloaded ⇒ silent no-op: capture stays with the provider, nobody told | error ⇒ existing Problems-ring warning naming method/amount/`authorize_request_id`; refusal outcome unchanged | strictly better |

My independent view on the refund-side expansion: it is the same bug in the
other direction, not overreach. A card return that the till records while
the provider's plugin is not running tells the customer they have their
money back when nothing moved — a money-integrity failure the card's own
title covers ("never complete when the plugin can't actually act"), and the
card text names `.refund` explicitly. Splitting it would leave
`blockingPaymentEventDispatch` fail-closed on one suffix and fail-open on
the other with no principled line between them. The reversal path cannot
realistically hit the new branch (a leg is only "captured" if its
`.authorize` had a live subscriber moments earlier; only a `Sync` racing in
between removes it) and when it does, surfacing an un-reversed capture to a
human is exactly what ADR-0136 asked for. ADR-0146's `refundDelivered`
semantics are untouched: the new branch returns `dispatched=false`, so a
refused refund never reads as "money already moved".

Operator-visible consequence to be aware of (not a defect): a shop whose
card plugin is broken can neither take nor refund on that method until the
plugin is restored. The toast says "declined" rather than "plugin not
loaded"; the log line carries the real cause. Making that visible before the
tap is #3636 (manifest `required` setting, disabled button, Settings badge)
— deliberately not in this PR.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | — | Production change: correct, minimal, generic, fails closed on lookup error. No defects found. | accepted |
| 2 | minor | `reverseCapturedPaymentLegs` doc comment (`pos_api.go:3011`) still says "a method with no `.refund` subscriber is a clean no-op". Still true for hook-less methods; now a declared-but-unloaded method logs the existing Problems-ring warning instead. Behaviour is the better one; prose is slightly incomplete. | accepted (comment-only; not worth widening the diff before merge — fold into #3636 or the next touch of that function) |
| 3 | minor, docs (ut-docs) | `architecture/wasm-runtime.md` §"Payment authorization (2026-07-14)" line ~302: "No subscriber = the old post-settle-only behaviour (qrpay unchanged)" — now only true for an *undeclared* hook. | fixed — one sentence added pointing at `reference/payment-provider-contract.md`'s "Declared but not loaded fails closed (ut-docs#3633)" paragraph, same ut-docs commit as that paragraph |
| 4 | info | `golangci-lint` / `deadcode` could not run locally (toolchain mismatch in the container). | CI covers it |

No in-scope code change was needed in review; the reviewed worktree is
byte-identical to `1002fdb`. After review, rebasing onto `origin/main`
surfaced a Go identifier collision between this branch's test file and
#1699's (landed on `main` in the interim, covering the unconfigured-settings
scenario this branch's early commit also happened to test) — the
orchestrator resolved it by dropping the two now-duplicate cases and
renaming the remaining identifiers (see "Branch" above); no production code
or review finding was affected, and the full gate was re-run clean
post-rebase.

## #324 manual check

No new page, route, setting or control. The one operator-visible behaviour —
"a declined card leaves the basket untouched" — is already the sentence
`web/help/en/payments.md` step 3 uses, and it remains accurate for this new
decline cause. No help topic update required for this PR; #3636's UI will
need one.

## Out of scope, noted for the backlog

- None new. (The visibility/UX half is already #3636.)

## Verdict

**Safe to merge: yes.** The fix closes a real fail-open on a P1 money path,
is reproduced end-to-end through the real WASM runtime and the real tender
handler, keeps hook-less providers untouched, inherits the existing
localized decline UX, and the full non-race gate is green.
