# Code review: refund screen missing TSE override banner (ut-docs#1001)

**Branch:** `fix/1001-refund-screen-override-banner` · **Commit:** `3ea0fe62`
**Reviewer:** independent subagent, fresh context (no prior involvement in
this change) · **Author:** pipeline Dev step (this cycle)

## What shipped

Follow-up to #731 (`2026-08-25-refund-tse-gate-731.md`), filed as that
review's own deferred item: the sale screen (`/`) shows a persistent
banner while an owner-granted ADR-0048 TSE override is active
(`fiscal.banner.override_active`); `/refund/{receipt}` was gated
identically on submit but showed no equivalent banner, so a cashier
processing a refund under an active override got no warning until they
submitted the form.

Fix: `GET /refund/{receipt}` (`internal/pages/refund_page.go`) now calls
the existing read-only `evaluateFiscalGate` and, when
`gate.Decision == fiscal.AllowedWithOverride`, passes
`fiscalOverrideActive`/`fiscalOverrideUntil` into the template —
`web/ui/pages/refund.html` renders the same `pos-notice`/
`fiscal-override-banner` markup `index.html` already uses, reusing the
existing `fiscal.banner.override_active` locale key (no new key needed;
it already reads "sales **and refunds**" since #731). `POST /api/refund`
and its `enforceFiscalGate` call are untouched. New regression test
`TestFiscalGate_RefundScreenBannerDuringOverride`
(`internal/pages/refund_fiscal_gate_test.go`). `sell.md` (all 4 locales)
broadened "a banner stays on the sale screen" to "the sale and refund
screens."

## Independent review — verdict: approve, no findings requiring a fix

Checked, and confirmed clean:

1. **Correctness / enforcement isolation.** `refund_page.go`'s GET
   handler calls `evaluateFiscalGate` (a pure settings read, no writes,
   no side effects — confirmed by reading its body in `pos_api.go:90-93`)
   directly, exactly as `index_page.go`'s sale screen already does. It
   never calls `enforceFiscalGate` (the sentinel-error-producing function
   the `POST /api/refund` handler calls at `refund_page.go`'s
   `enforceFiscalGate(r.Context(), d)` call site, unchanged in this
   diff) — so there is no risk of the GET path becoming, weakening, or
   duplicating the real enforcement path. `git diff` on the POST handler
   block is empty. On a gate-read error (`gErr != nil`) the GET handler
   silently renders no banner and falls through to the normal page —
   same fail-open-on-read-error behavior as `index_page.go`'s identical
   `if g, gErr := evaluateFiscalGate(...); gErr == nil && ...` guard, so
   this isn't a new failure mode, it's the same one already accepted for
   the sale screen. The two implementations
   (`internal/pages/refund_page.go` vs `internal/pages/index_page.go`,
   and `web/ui/pages/refund.html` vs `web/ui/pages/index.html`) are
   line-for-line the same pattern, comment style included.

2. **i18n.** No new locale key was needed. Confirmed
   `fiscal.banner.override_active` exists in all four `web/locales/*.json`
   files (flat dotted-key format, line 1600 in each) and already reads
   "sales and refunds…" in every locale (en/ar/fa/tr) — updated by #731,
   so reusing it for the refund screen is accurate as-is, no drift.
   `guard-i18n.sh` passes (1277 keys, all locales match).

3. **Test quality.** `TestFiscalGate_RefundScreenBannerDuringOverride`
   mirrors `fiscal_kiosk_banner_test.go`'s
   `TestFiscalGate_SaleScreenBannerDuringOverride` structure: seeds a
   failing-but-configured TSE, asserts no `fiscal-override-banner` before
   a grant, grants the override via a real `POST
   /api/fiscal/tse-override` (admin session), then asserts the banner
   *is* present. Not a tautology — **personally re-verified via
   revert/restore in this checkout**: reverted `refund_page.go` and
   `refund.html` to the pre-fix `HEAD~1` content with
   `git checkout -- <files>`, re-ran the new test, confirmed it fails
   (`--- FAIL: TestFiscalGate_RefundScreenBannerDuringOverride`, no
   banner ever rendered), restored the fix from a pre-made backup,
   confirmed byte-identical to the original diff, and confirmed the
   full `TestFiscalGate` suite (19 tests) and the whole
   `internal/pages` package pass again afterward.
   One minor asymmetry versus the sale-screen test, not a blocker: the
   sale-screen version also asserts the banner disappears once the
   override window expires; the refund version doesn't repeat that
   third case. Low risk — both render paths call the identical
   `evaluateFiscalGate`, and expiry itself is already covered at the
   gate level (`internal/fiscal/fiscal_test.go`) and at the sale-screen
   render level — but a literal 1:1 port would have included it. Not
   asking for a fix; noting it as a nit.

4. **Money / data-access / kiosk-engine.** Not the focus of this change
   and none touched: `guard-data-access.sh` and `guard-kiosk-engine.sh`
   both pass; `/refund/{receipt}` is a cashier-only route (not
   `/self-order`/`/api/self-order/*`), so kiosk isolation doesn't apply
   here regardless. No raw SQL added outside `internal/data`/`internal/db`
   (`refund_page.go`'s new code reads only `d.CurrentState()` and calls
   the existing `evaluateFiscalGate` helper). No `money.Money` handling
   in this diff at all.

5. **Help-doc wording.** `/refund/{receipt}` is claimed by `sell.md`'s
   own `routes:` front matter (not a separate `refunds.md` topic), so
   editing `sell.md` in all 4 locales is the right file, consistent with
   #731's precedent. Each edit is a single, localized clause change
   inside the existing sentence, not a rewrite:
   - **en** (`web/help/en/sell.md:89`): "a banner stays on the sale
     screen" → "a banner stays on the sale **and refund** screens."
   - **tr** (`web/help/tr/sell.md:29`): "satış ekranında" → "satış ve
     iade ekranlarında" (sale screen → sale-and-refund screens).
   - **fa** (`web/help/fa/sell.md:29`): "صفحه فروش" → "صفحه فروش و
     بازپرداخت" (sale screen → sale-and-refund screen).
   - **ar** (`web/help/ar/sell.md:29`): "شاشة البيع" → "شاشتي البيع
     والاسترجاع" — correctly uses Arabic **dual** grammatical number
     (شاشتي, "the two screens of") rather than a naive plural/
     concatenation, so this reads as a native, considered edit, not a
     mechanical string paste.
   All four edits leave the surrounding sentence otherwise untouched.
   `guard-help-topics.sh` and `guard-compliance-claims.sh` both pass.

## Commands run (personally, this checkout)

- `gofmt -l internal/pages/refund_page.go
  internal/pages/refund_fiscal_gate_test.go` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/pages/... -run TestFiscalGate -v` — all 19
  `TestFiscalGate_*` tests pass, including the new one.
- `go test ./internal/pages/...` (full package, no filter) — pass
  (75.4s).
- `bash scripts/ci/guard-i18n.sh` — pass (1277 keys, all locales match).
- `bash scripts/ci/guard-help-topics.sh` — pass (route coverage intact,
  `/refund/{receipt}` still claimed by `sell.md`).
- `bash scripts/ci/guard-compliance-claims.sh` — pass (220 files
  scanned).
- `bash scripts/ci/guard-data-access.sh` — pass.
- `bash scripts/ci/guard-kiosk-engine.sh` — pass.

## TDD re-verification (personal, not taken on trust)

Reverted `internal/pages/refund_page.go` and `web/ui/pages/refund.html`
to their pre-fix content (`git checkout -- <files>`, confirmed against
`HEAD~1`), re-ran
`TestFiscalGate_RefundScreenBannerDuringOverride` — failed, no
`fiscal-override-banner` ever rendered (the pre-fix template has no such
block and the GET handler never computes `fiscalOverrideActive`).
Restored both files from a pre-revert backup, confirmed
byte-for-byte identical to the intended fix via `diff`, then re-ran the
full `TestFiscalGate` suite and the whole `internal/pages` package —
both green.

## Process note (not a code finding)

This review's revert/restore was done inline against the shared
checkout, not an isolated worktree (`ut-docs#386`'s documented risk).
Partway through this review, `git log` showed the working-tree changes
already landed as commit `3ea0fe62` on this branch — committed by
something other than this review turn (the review issued no `git
commit`). By the time that commit was made, this review's own
revert-then-restore cycle had already completed and the restored files
were verified byte-identical to the intended fix, so the commit that
landed carries the correct, working code — not a broken mid-revert
snapshot. Still, this is exactly the race `ut-docs#386` warns about;
future reviews of this shape should run the revert/restore step in an
isolated worktree rather than the shared checkout, per the `reviewer`
skill's own guidance, even when (as here) it happens to resolve safely.

## Deferred / follow-ups

None new. This closes the last item #731 deferred
(ut-docs#1001 itself); #731's other three deferred items
(ut-docs#998, #999, #1000) are unrelated to this diff and remain open on
their own cards.

## Safe-to-merge verdict

**Approve.** No blockers, no should-fix items. One nit noted above (test
doesn't repeat the sale-screen test's expiry-disappearance case) — low
risk given the shared `evaluateFiscalGate` implementation and existing
expiry coverage elsewhere, not required before merge.
