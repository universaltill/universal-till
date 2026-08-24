# 2026-08-24 — EnsurePaymentMethod's raw error-text leak (ut-docs#923)

## What shipped

`ut-docs#921`'s own review (`docs/code-reviews/2026-08-24-tender-error-i18n-fallback.md`)
found and explicitly deferred a sibling raw-English leak in the same
`/api/pos/tender` handler: `EnsurePaymentMethod`'s failure path still did
`http.Error(w, err.Error(), http.StatusInternalServerError)` — verbatim Go
error text (e.g. a raw SQL error) regardless of locale — at two call sites:
the JSON-payments branch and the form-encoded quick-tender fallback.
`ut-docs#923` split this out as its own narrowly-scoped card.

`EnsurePaymentMethod`'s only real failure mode is a DB-layer error on its
own FK-upsert (`internal/data/pos_repo.go`) — not a reachable business
rejection the way underpayment/decline are — so the fix reuses the
existing generic `pos.toast.tender_failed` copy ("Sale could not be
completed — try again or ask an administrator for help") rather than
adding a new locale key, mirroring the neighboring `pos.toast.payment_declined`
branch's `http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "..."), status)`
shape already established in the same file. The 500 status is unchanged
(correct — this is a genuine internal error, not a 200-with-toast business
rejection).

## Independent review

Fresh-context **Sonnet** subagent (this card is `complexity:easy`, built at
Sonnet — routing per the scrum-master skill's "one case where 'different
model' relaxes to 'different instance'"), isolated worktree.

**Findings: none, blocking or non-blocking.** Confirmed:
- `gofmt`, `go build ./...`, `go vet ./internal/pages/...` clean.
- `go test ./internal/pages/... -race -run 'TestTenderHandler|TestClassifyTenderError'`
  — all 20 tests green, including the 2 new ones.
- `guard-i18n.sh` and `guard-data-access.sh` green.
- **Independently re-verified the TDD claim**: reverted both hunks back to
  the raw `http.Error(w, err.Error(), ...)` calls, reran the two new tests
  — both **failed** with the real symptom (raw SQL error text leaking:
  `"SQL logic error: no such table: payment_methods (1)"`); restored the
  fix, confirmed the working tree matched the original diff exactly
  (`git diff` against the PR branch came back empty), reran — both
  **passed**.
- **Verified the fault-injection isolation is sound**: `EnsureStockLocation`
  (called earlier in the same handler, `pos_api.go:619`) only touches
  `stock_locations`; `EnsurePaymentMethod` only touches `payment_methods`.
  Dropping `payment_methods` alone can't affect `EnsureStockLocation`'s
  earlier success, so the provoked failure is deterministically isolated to
  the code path under test.
- No real client/shop name or secret-shaped literal in the diff.
- Repository pattern intact — no new SQL added outside `internal/data`; the
  diff only calls the existing `repo.EnsurePaymentMethod`.
- No new locale key added (reuses `pos.toast.tender_failed`), so no
  `lang-pack-drift` follow-up needed in the external language packs.
- Money/offline-first/kiosk-isolation rules not implicated (no money-typed
  values touched, no `/self-order` routes, no new network dependency).
- No UI/manual-topic impact — this is an internal error-message fix behind
  an already-existing, already-documented rejection path, not a new
  feature or a change to what a shop owner sees documented.

## Verified beyond automated tests

- Full independent re-derivation of the fault-injection technique (table
  drop) and confirmation it isolates the correct call path, not just
  trusting the PR description's claim.
- Revert-then-restore TDD re-verification done by the reviewer personally,
  not taken on the implementer's word.

## Non-goals / explicitly deferred (pre-existing, not this card's scope)

- The repo-wide `http.Error(w, err.Error(), ...)` sweep (~29 files) —
  already tracked as `ut-docs#924`.
- `EnsureStockLocation`'s own identical raw-error leak, a few lines earlier
  in the same handler (`pos_api.go:~621`) — same defect class, different
  call site; filed as its own new Backlog card, `ut-docs#929`, while
  working this one (kept separate to stay reviewable, same reasoning
  #921→#923 already used).
- `web/public/**`'s hardcoded English JS strings — a known, documented
  `guard-i18n.sh` scanning gap noted in #921's own review; unrelated to
  this diff.

## Safe-to-merge verdict

**Yes.** No findings. Full gate green (targeted `-race` run on the affected
package/tests — a separate, unrelated WASM-JIT-compilation timeout in
`internal/plugins` under a full `./internal/... -race` sweep was confirmed
pre-existing and unrelated: different files entirely, and the flagged test
passes in 1.6s without `-race`). TDD claim independently re-verified.
Merging with `merge_method: "merge"` (never squash/rebase — ut-docs#250)
once CI is confirmed green on the PR's head.
