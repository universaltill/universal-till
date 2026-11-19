# Review — a delete's `before` carries the net-quantity pair together, or neither (ut-docs#6203)

**Date:** 2026-11-14 · **Lane:** `lane:cloud-41` · **Author:** Sonnet (Dev) · **Reviewer/Tester:** Opus 5.5, independent, fresh context (`complexity:easy`)

## What shipped

- `internal/catalogpull/push.go` (delete branch of `sealMark`): `net_quantity_value` / `net_quantity_unit` go into a delete's `before` only when the base holds both halves — the same rule `coupleNetQuantity` applies to updates. A base with one half would have sent a lone half; the cloud (contract `cloud-catalog-changes` 1.1.0) refuses that whole call with 400 and the sealed mark was re-sent unchanged, stalling the outbox. Hardening: no path writes a one-half base today (finding F3 of the #6202 review).
- `push_fake_test.go`: the fake cloud refuses a delete whose `before` holds a lone half, checked before the live-row lookup as the real cloud does at parse time (ut-cloud `internal/catalogchanges/change.go:186`).
- `push_delete_netqty_6203_test.go`: `TestPushDeleteWithALoneNetQuantityHalfOmitsBoth_6203` (both lone-half cases; the base is stripped with `json_remove`) and `TestPushDeleteWithTheWholeNetQuantityPairKeepsBoth_6203` (a full pair still goes).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | low | The fake checked the lone half only after its `!live` early return, so a lone half on a delete of an already-gone row was accepted; the real cloud refuses it while parsing. | **Fixed:** check moved above `!live`. |
| R2 | info | Scoping: the delete branch is only reached for items (other entities return earlier), and the check is independent of the seeded-sku skip. | No change. |

## Verification

- TDD: Dev saw both lone-half subtests fail on the old `push.go` (`Accepted:0 … Pending:1, want the delete accepted` — the stall). Reviewer re-verified it independently in a separate worktree: reverted `push.go` → fails with the same message; restored → passes (also with `-race -run 6203`).
- `gofmt -l` clean, `go vet`, `go build ./...`, `golangci-lint run ./internal/catalogpull/...` 0 issues, `go test ./internal/catalogpull -count=1` ok (re-run after R1). Full-package `-race` left to CI (>15 min in the cloud sandbox). `guard-data-access.sh`, `guard-fixed-dates.sh` pass.
- No UI or runtime surface: the driven run is the fake-cloud push harness.

Pre-flight: any change — review record this file; till user-visible behaviour n/a (push path only, nothing on screen); `docs/data-model.md` n/a (its upload section already states the pair rule); `en.json` n/a; no migration; no follow-ups.

**Verdict:** approve.
