# 2026-09-08 — Payment-method case canonicalization for the fiscal-device gate (ut-docs#1795)

## What shipped

The refund handler (`internal/pages/refund_page.go`) and the sale/tender
handler (`internal/pages/pos_api.go`) read a `method` string straight
from the request (JSON body or form field) and used it, uncanonicalized,
in four places:

1. `repo.EnsurePaymentMethod(ctx, method)` — upserts a `payment_methods`
   row keyed by the exact string.
2. `pos.PaymentInput{MethodID: method, ...}` — persisted on the sale/refund.
3. `blockingPaymentEventWithResponseAndID`'s plugin-entry exact-match
   lookup (`e.EntryKey != method`) — routes to the correct payment
   plugin's webhook, e.g. Turkey's ÖKC fiscal-device plugin (manifest
   key `"okc"`, `fiscal.MethodKeyOKC = "okc"`).
4. Every `fiscal.MethodKeyOKC` fail-closed evidence check (ut-docs#1779
   sale-side, ut-docs#1788 refund-side).

Because none of this was case-folded, a request with `method=OKC` (or
any other casing) silently missed the exact-match lookup: the plugin was
never invoked, and the fail-closed evidence check never applied. The
tender/refund would complete as an ordinary payment with the real fiscal
device never contacted and no legal receipt (*mali fiş*/*iade fişi*) —
the exact sidestep this card names. Found by the independent review of
ut-docs#1788, filed as this card.

**Fix:** canonicalize `method` (`strings.TrimSpace` + `strings.ToLower`)
once, at the request boundary, before any use:
- `internal/pages/pos_api.go`'s `/api/pos/tender` handler, both the
  JSON-payments loop and the form-encoded fallback branch.
- `internal/pages/refund_page.go`'s refund handler.

Canonicalizing at the read site (rather than making each comparison
site case-insensitive) was the deliberate choice: it keeps
`EnsurePaymentMethod`, the persisted `MethodID`, the plugin-entry
lookup, and the `fiscal.MethodKeyOKC` checks all agreeing on one form,
and avoids a differently-cased request minting a second, orphaned
`payment_methods` row (e.g. `"OKC"` alongside the real `"okc"`) that a
purely comparison-side fix would still allow.

New regression tests (both submit `method="OKC"`, assert the plugin's
webhook handler WAS invoked via a closure bool, and assert refusal (402)
with no sale/return row persisted when the plugin approves with no
`fiscal_device` evidence — mirroring the pre-existing exact-case sibling
tests already in both files):
- `TestTenderHandler_UppercaseOKCMethod_StillRoutesToPluginAndFailsClosed`
  (`internal/pages/pos_api_test.go`)
- `TestPostRefund_UppercaseOKCMethod_StillRoutesToPluginAndFailsClosed`
  (`internal/pages/refund_page_test.go`)

TDD genuinely red→green: both tests written and confirmed failing (with
the expected "handler ... was never called" message) against the
pre-fix code, then passing once the canonicalization landed — done
independently by both Dev and the Reviewer (Reviewer reverted the fix
hunks in its own isolated worktree and reproduced red, then restored and
confirmed green).

## Independent review

Sonnet, fresh context, isolated worktree (`Agent(isolation: "worktree")`)
— per this card's `complexity:easy` routing (Dev/Sonnet builds, a
fresh-context Sonnet subagent reviews). **Verdict: SAFE TO MERGE.**

(First review attempt reported "branch identical to main" — a real
process gap, not a code finding: that worktree was created before the
fix was committed to the branch, so it genuinely had nothing to diff.
Re-run against the actual commit; see "Process note" below.)

**Finding (LOW, fixed):** `internal/pages/self_order_shop.go`'s
`POST /api/self-order/checkout` handler reads `method` the same
uncanonicalized way and feeds the identical `completeTender` →
`blockingPaymentEventWithResponseAndID` / `fiscal.MethodKeyOKC` sink —
not covered by the original diff. Traced why it wasn't currently
exploitable there: this handler validates `method` by exact match
against `repo.ListActiveNonCashPaymentMethods()` and explicitly refuses
to call `EnsurePaymentMethod` for anything unrecognized (fails closed to
`invalid_method` rather than routing anywhere), so a mismatched case
today just gets rejected rather than silently bypassing the fiscal gate.
But that's an incidental property of the whitelist check, not a design
guarantee — and this is the *anonymous, auth-exempt* surface (ADR-0020,
reachable by any LAN client), the higher-risk of the two kinds of
checkout in this codebase. **Fixed**: same
`strings.ToLower(strings.TrimSpace(...))` canonicalization applied at
`self_order_shop.go`'s checkout handler, for consistency and
defense-in-depth rather than relying on the whitelist alone.

**Findings accepted as-is (informational, filed as follow-ups, no
change needed in this PR):**
- `internal/plugins/manifest.go`'s `validatePaymentEntryKeys` doesn't
  enforce that a plugin's payment entry key is itself lowercase, and
  `FindPaymentKeyConflicts` compares keys with SQLite's default
  case-sensitive collation — so nothing today stops a second,
  differently-cased key from installing alongside an existing one
  without a conflict error. Not exploitable by an unprivileged request
  (plugins are Ed25519-verified; the real tax-tr manifest already uses
  lowercase `"okc"`), and this fix's "one canonical lowercase form"
  assumption isn't itself mechanically enforced at the one place new
  keys enter the system. Worth a small follow-up card — filed as
  ut-docs#1811.
- Lowercasing only applies to *new* writes; any pre-existing mixed-case
  `payment_methods`/`payments.method_id` rows (if this bug was ever
  actually triggered live) won't be retroactively merged. Expected,
  correct behavior for a forward-looking fix (never silently rewrite
  historical fiscal rows) — noted for whoever deploys, in case a
  one-off data check is warranted separately.

**Checked and found no issues:** `strings.ToLower` is safe for this
identifier space (all real keys — `fiscal.MethodKeyOKC = "okc"`,
reserved sentinels `"cash"/"unknown"/"split"` — are plain ASCII, and
Go's `ToLower` is simple Unicode folding, not locale-aware, so no
Turkish-İ-style edge case applies to any key actually in use); no other
call site of `blockingPaymentEventWithResponseAndID` exists besides the
three (now four, with the self-order fix) covered sites; `sync_sales.go`
replaying an already-completed sale from a replica never re-invokes the
fiscal gate or plugin routing, so it's out of scope for this bug class;
the two new tests exercise the real vulnerable path (installed tax-tr
plugin, blocking subscriber, approve-with-no-evidence response,
asserting both plugin invocation and the 402/no-persisted-row outcome)
— not a strawman. No money/repository-pattern/i18n issues: no new
user-facing strings, no raw SQL added outside `internal/data`.

## Verification beyond automated tests

- `go test ./...` (full suite, all packages): green, no regressions.
- `gofmt -l`: clean. `go build ./...`: clean. `go vet ./...`: clean.
- `golangci-lint run ./internal/pages/...`: 0 issues.
- `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-kiosk-engine.sh`:
  both green (the self-order fix in particular is the one the
  kiosk-engine guard exists for — it doesn't reference `d.Engine`, only
  `d.KioskEngine`, unchanged from before this diff).
- `scripts/ci/guard-i18n.sh`, `scripts/ci/guard-help-topics.sh`: green
  (no user-facing strings or routes touched).

## Process note

The first review pass was spawned before the local commit existed on
the feature branch (the Dev/commit step and the review-agent launch
raced under this session's own workflow), so that worktree's `git diff
main...fix/1795-...` was genuinely empty — correctly reported as such,
not a false pass. Re-running after the commit landed produced the real
review above. Recorded here so a future reader of this file's history
doesn't mistake the first attempt for a skipped review step.

## Non-goals

Manifest-key case enforcement (see follow-up finding above) and any
backfill of historically mis-cased `payment_methods` rows are
deliberately out of scope for this card.
