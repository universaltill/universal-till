# Code review: refund quantity guard rejects NaN/Infinity (ut-docs#1711)

**Branch:** `fix/1711-refund-nan-guard`
**Author:** Farshid Mirza (pipeline, lane:cloud-54)
**Reviewer:** independent Sonnet subagent (fresh context, no prior reasoning), per `MODEL-ROUTING.md`'s easy-complexity review tier

## What

`internal/pages/refund_page.go`'s `refundLinesFromForm` — the quantity
validation shared by `POST /api/refund` and `POST /api/refund/preview`
(ut-docs#1217) — parses each `qty_N` form field with `strconv.ParseFloat`
and rejected only `perr != nil || qty <= 0`. Go's `ParseFloat` successfully
parses `"NaN"`/`"Inf"`/`"Infinity"` (and their negatives), and every ordered
comparison against `NaN` is `false`, so:

- `qty_0=NaN` passed both `qty <= 0` and the later `qty > remaining+1e-9`
  guard, and (pre-existing, verified live) reached `pos.CompleteSale`,
  which rejected it anyway — but only by accident, via
  `l.LineDiscount.IsNegative() || l.LineDiscount > lineBase`
  (`internal/pos/sales.go:337`) tripping on the implementation-defined
  `int64` a `NaN`-tainted discount computation produces. That surfaces as a
  generic "Sale could not be completed" 400, not this layer's own
  `refundInvalidQuantityError`.
- `qty_0=Infinity`/`Inf` passed `qty <= 0` (`+Inf > 0`) and was instead
  caught by the exceeds-remaining check further down — a 409
  ("only N left to refund"), the wrong error entirely for a malformed
  quantity.
- `qty_0=-Infinity`/`-Inf` was already correctly rejected pre-fix (`-Inf <=
  0` is a normal, well-defined `true` comparison — no `NaN` involved).

Fix: explicit `math.IsNaN(qty) || math.IsInf(qty, 0)` alongside the
existing `qty <= 0` check, rejecting via the same `refundInvalidQuantityError`
(400) every other malformed quantity already uses. Matches existing
precedent in this repo (`internal/pages/common/state.go:260`,
`ParseServiceChargeRateBasisPoints`).

## Independent review

Full record of the review pass itself (fresh-context Sonnet subagent, read
`refund_page.go` and `refund_page_test.go` in full, traced
`pos.CompleteSale`/`validateLine`/`money.MulQty` downstream before judging):

- **[non-blocking, fixed] Test asserted status code only, so the NaN and
  -Infinity/-Inf subtests didn't actually fail pre-fix** (confirmed live —
  see Verification below) — didn't enforce the AC's requirement that the
  rejection go through `refundInvalidQuantityError` specifically, not
  whatever else happens to return 400. Fixed: the test now asserts the
  response body contains `"invalid quantity for line 1"` (the exact
  `refundInvalidQuantityError.Error()` string), not just the status code.
- **[non-blocking, fixed] Test's doc comment overstated a downstream safety
  net that doesn't exist** — it claimed `pos.CompleteSale` "does catch it
  today" as if by design; traced `validateLine` and confirmed it has the
  same blind spot (`qty <= 0` only) — what actually produced a 400 pre-fix
  for the NaN case was an accidental side effect (see "What" above), not a
  designed check. Comment rewritten to describe the accidental path
  precisely, including noting the same underlying gap exists unguarded on
  `/api/pos/scan`'s form-qty path (`pos_api.go:425-434`, out of scope for
  this card — not fixed here).
- Correctness of `math.IsNaN(qty) || math.IsInf(qty, 0)` (both signs,
  matching the AC's "NaN and Infinity/-Infinity"): confirmed sufficient — no
  missed float edge case (subnormals, `-0`, large-but-finite values are
  legitimate/pre-existing behavior, unaffected and out of scope).
  `math.IsInf(qty, 0)` (unsigned) rather than splitting +/- is the right,
  idiomatic call.
  `-Inf` half of the new check is redundant with the pre-existing `qty <= 0`
  (verified: pre-fix response body for `-Inf` is byte-identical to
  post-fix) — kept anyway for symmetry/self-documentation, per the
  reviewer's own recommendation (harmless, and matches the guard's shape at
  every other malformed-input check in this function).
- Caller interaction: both `POST /api/refund` (`errors.As(&invalidQty)`,
  400) and `POST /api/refund/preview` (generic `err != nil` → renders `—`)
  handle the new/existing error path correctly and consistently — verified
  in code, no gap.
- Style: matches this repo's existing guard-shape precedent
  (`internal/pages/common/state.go:260`) and the surrounding function's
  comment density.

**Verdict: APPROVE WITH NON-BLOCKING NOTES.** Both findings were about test/
comment quality, not the shipped behavior; both fixed in this same commit
before merge.

## Verification

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- `go test ./internal/pages/...` green (whole package, including the new
  test).
- `golangci-lint run ./internal/pages/...` 0 issues.
- Guards run locally: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh` — all pass (no locale/help/UI changes in this
  diff, so these are no-ops as expected, not silently skipped).
- **TDD re-verified personally, both before and after the reviewer's
  fixes**: reverted `refund_page.go` alone (test kept), re-ran
  `TestPostRefund_NaNAndInfiniteQuantityAreRejected` — after strengthening
  the test to assert on the error body, `NaN`/`Infinity`/`Inf` subtests now
  genuinely FAIL against the pre-fix code (`Infinity`/`Inf`: 409 instead of
  400; `NaN`: wrong 400 body, the generic sale-rejection message instead of
  `refundInvalidQuantityError`'s), confirming the test would have caught a
  regression the earlier status-code-only version would have missed.
  `-Infinity`/`-Inf` pass both before and after (pre-existing correct
  behavior, kept for guard symmetry). Restored the fix; full suite green
  again.
- No i18n/help-doc/ADR implications — this is a pure input-validation fix
  with no new user-facing string, page, or behavior visible to a shop
  operator.

Closes universaltill/ut-docs#1711
