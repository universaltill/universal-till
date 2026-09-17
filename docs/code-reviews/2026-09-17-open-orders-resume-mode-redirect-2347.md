# Code review: mode-aware resume redirect on /open-orders (ut-docs#2347)

**Card:** ut-docs#2347 — "Resume redirects (/open-orders, both plain and
auto-park) always target '/', dropping the notice and the basket view in
backoffice/self_order display modes."

**Complexity:** easy. **Reviewer:** fresh-context Sonnet subagent (no
worktree isolation — the diff was uncommitted at review time, so the
agent reviewed the live working tree directly via `git diff`), per this
card's `complexity:easy` model routing (build inline, review via a
fresh-context Sonnet subagent).

## What changed

- `internal/pages/index_page.go`: added `saleScreenReturnURLWithMsg(mode,
  msgKey string) string`, next to the existing `saleScreenReturnURL`,
  composing the mode-aware target with an optional `msg=` query parameter
  — `&msg=` when the target already carries a query (`backoffice`'s
  `?stay=1`), `?msg=` otherwise.
- `internal/pages/open_orders_page.go`: `POST /open-orders/resume` now
  reads `display.mode` and redirects success through
  `saleScreenReturnURL(mode)` / `saleScreenReturnURLWithMsg(mode,
  "hold.toast.parked_and_resumed")` instead of a hardcoded `"/"` /
  `"/?msg=..."`. `resumeNotFound`/`resumeFailed` are untouched — they
  correctly stay mode-independent (`/open-orders?err=...`, not the sale
  screen).
- Tests: `TestSaleScreenReturnURLWithMsg` (pure unit test, all three
  modes × with/without msg) in
  `open_orders_backtosale_redirect_test.go`; a new
  `newOpenOrdersResumeModeTestDeps` fixture plus
  `TestOpenOrdersResume_PlainSuccess_ModeAwareRedirect` /
  `TestOpenOrdersResume_ParkedPrior_ModeAwareRedirect` in a new file,
  `open_orders_resume_mode_redirect_test.go`, covering the real HTTP
  handler end to end for all three modes.

## TDD verification

Reverted the fix once as part of building it (stash/pop against the two
production files), confirmed the new handler-level tests fail with
exactly the pre-fix redirect (`Location: /` instead of `/?stay=1` /
`/self-order`); reapplied, confirmed green. The reviewer independently
re-verified this a second time from cold (reverting just the two
production `.go` files and confirming the package fails to build against
the new tests, then restoring and confirming green) rather than trusting
the claim.

## Review findings

- **Should-fix (not a blocker):** the new fixture
  (`newOpenOrdersResumeModeTestDeps`) hand-rolls a third copy of the
  `held_sales`/`tables`/`table_claims`/`tills` schema, duplicating
  `newHoldTestDeps` (`hold_api_test.go`) rather than extending
  `newOpenOrdersFullFixtureMux`'s real-migration DB with an `Engine`
  field. Accepted as-is: it mirrors an already-existing pattern in this
  same package (`newHoldTestDeps` itself), not a new class of drift risk,
  and fixing it was out of scope for an easy-tier redirect fix. Left as
  documented tech debt rather than expanding this card's scope.
- **Nit:** `saleScreenReturnURLWithMsg` concatenates `msgKey` into the
  query string unescaped. Harmless today (every caller passes a hardcoded
  literal i18n key; matches the existing unescaped-literal convention
  already used for `?err=hold.error.not_found` elsewhere in this file) —
  worth a defensive `url.QueryEscape` only if this helper ever takes a
  non-literal key.

No blockers found. Full gate green: `gofmt -l .`, `go build ./...`, `go
vet ./...`, `go test ./internal/pages/...`, `golangci-lint run
./internal/pages/...` (0 issues), plus `guard-data-access.sh`,
`guard-i18n.sh`, `guard-page-http-error.sh`, `guard-kiosk-engine.sh`,
`guard-htmx-loaded.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`
(pre-existing baselined drift only, unrelated), `guard-compliance-claims.sh`.

**Verdict: ship as-is.**
