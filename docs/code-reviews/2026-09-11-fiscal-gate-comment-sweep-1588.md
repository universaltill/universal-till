# Code review: fiscal-gate comment sweep (ut-docs#1588)

**Date:** 2026-09-11
**Change:** comment-only rewording of stale "German TSE hard gate" comments to
name the gate's real DE+TR scope, across `internal/pages`.
**Author:** Dev phase (Sonnet, inline).
**Reviewer:** independent fresh-context Sonnet subagent (no shared context
with the author), per this pipeline's easy-tier review rule.

## What changed

`fiscal.RequiresHardGate` (`internal/fiscal/fiscal.go`) has covered Germany
(DE) and, since ut-docs#1208, Turkey (TR) for a while. Several comments in
`internal/pages` still described the same gate as a "German TSE hard gate",
left over from before Turkey was added. Comment-only fix, no behavior
change, across 14 sites in 7 files:

- `internal/pages/pos_api.go` (3 sites — the 3 originally named in the issue,
  though the issue's cited line numbers had drifted from intervening commits)
- `internal/pages/index_page.go` (1 site)
- `internal/pages/self_order_shop.go` (1 site)
- `internal/pages/refund_page.go` (2 sites, found during review, not in the
  issue's original list)
- `internal/pages/shifts_api.go` (3 sites, found during review)
- `internal/pages/inventory_api.go` (1 site, found during review)
- `internal/pages/refund_fiscal_gate_test.go`,
  `internal/pages/fiscal_kiosk_banner_test.go`,
  `internal/pages/inventory_return_fiscal_gate_test.go` (1 comment each,
  found during review)

## Review findings

The independent review round caught one real issue in the first draft: the
initial wording ("DE+TR TSE hard gate") corrected the market list but kept
"TSE", which is Germany-specific device vocabulary — Turkey's device is a
YN ÖKC, not a TSE (see `fiscal.go`'s own `RequiresPerSaleDeviceReceipt`
comment, which explicitly contrasts the two). That traded one
Germany-only claim for another. Fixed by switching to the market-neutral
"fiscal-signing-device hard gate" phrasing already used elsewhere in the
same files (e.g. `pos_api.go`'s existing, untouched
`fiscalTSEFailingError` comment), applied consistently across all 14 sites.

The review also found 9 more instances of the identical stale phrasing in
files the original issue didn't list (`refund_page.go`, `shifts_api.go`,
`inventory_api.go`, and 3 test-file comments) — folded into this same PR
since it's the identical mechanical fix, not a new decision, consistent
with the issue's own title ("sweep **remaining** ... comments").

## Verified

- `gofmt -l` on every touched file: clean.
- `go build ./...`: clean.
- `go vet ./internal/pages/...`: clean.
- `go test ./internal/pages/...` (full package + subpackages): all green,
  before and after the review's wording fix.
- Manually confirmed every hunk is comment-only (no code, string literal,
  or directive-attachment changes) via `git diff`.
- Confirmed `fiscal.RequiresHardGate` (`internal/fiscal/fiscal.go:191`)
  returns true only for `"DE"`/`"TR"` today, matching the new wording.

## Not done / deferred

- `internal/fiscal/tse_credential_store.go`'s missing blank line above
  `package fiscal` (noted as a pre-existing, unrelated nit in ut-docs#1588's
  body) — left alone, out of this card's scope.
- No further grep hits for "German TSE hard gate" remain in `internal/pages`
  after this PR (verified).
