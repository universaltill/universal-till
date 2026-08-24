# 2026-08-24 — EnsureStockLocation's raw error-text leak (ut-docs#929)

## What shipped

`ut-docs#923`'s own review (`docs/code-reviews/2026-08-24-ensure-payment-method-i18n-923.md`)
flagged and explicitly deferred a sibling raw-English leak a few lines
earlier in the same `/api/pos/tender` handler: `EnsureStockLocation`'s
failure path still did `http.Error(w, err.Error(), http.StatusInternalServerError)`
— verbatim Go error text (e.g. a raw SQL error) regardless of locale.
`ut-docs#929` split this out as its own narrowly-scoped card, same reasoning
`#921`→`#923` already used.

`EnsureStockLocation`'s only real failure mode is a DB-layer error on its
own upsert (`internal/data`) — not a reachable business rejection the way
underpayment/decline are — so the fix reuses the existing generic
`pos.toast.tender_failed` copy ("Sale could not be completed — try again or
ask an administrator for help") rather than adding a new locale key,
mirroring the `EnsurePaymentMethod` fix's
`http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "..."), status)` shape
already established in the same file. The 500 status is unchanged (correct
— this is a genuine internal error, not a 200-with-toast business
rejection).

## Independent review

Fresh-context **Sonnet** subagent (this card is `complexity:easy`, built at
Sonnet — routing per the scrum-master skill's "one case where 'different
model' relaxes to 'different instance'").

**Verdict: PASS. Findings: none, blocking or non-blocking.** Confirmed:

- `gofmt -l` on both changed files clean; `go build ./...` clean.
- `go test ./internal/pages/...` — all packages green, including the new
  test, no regressions.
- `guard-i18n.sh` green — confirmed the reused `pos.toast.tender_failed` key
  exists with matching content in all four shipped locales (en/ar/fa/tr),
  so no new key and no `lang-pack-drift` follow-up needed.
- **Independently re-verified the TDD claim**: stashed the `pos_api.go`
  hunk back to the raw `http.Error(w, err.Error(), ...)` call, reran the
  new test — **failed** with the real symptom (raw SQL error text leaking:
  `"SQL logic error: no such table: stock_locations (1)"`); restored the
  fix, reran — **passed**, with the raw detail only reaching the server log
  line, not the response body.
- **Verified the fault-injection isolation is sound**: dropping
  `stock_locations` provokes the failure at the very first DB call the
  handler makes after the basket check (`EnsureStockLocation`, called
  before the payment loop and thus before `EnsurePaymentMethod`), so the
  failure is deterministically pinned to the call under test.
- No real client/shop name or secret-shaped literal in the diff.
- Repository pattern intact — no new SQL added outside `internal/data`; the
  diff only reacts to the existing `repo.EnsureStockLocation` call.
- Money/offline-first/kiosk-isolation rules not implicated (no money-typed
  values touched, no `/self-order` routes, no new network dependency).
- No UI/manual-topic impact — an internal error-message fix behind an
  already-existing, already-documented rejection path, not a new feature or
  a change to what a shop owner sees documented.

## Verified beyond automated tests

- Full independent re-derivation of the fault-injection technique (table
  drop) and confirmation it isolates the correct call path, not just
  trusting the PR description's claim.
- Revert-then-restore TDD re-verification done by the reviewer personally,
  not taken on the implementer's word.

## Non-goals / explicitly deferred (pre-existing, not this card's scope)

- The repo-wide `http.Error(w, err.Error(), ...)` sweep (~29 files) —
  already tracked as `ut-docs#924`.
- `web/public/**`'s hardcoded English JS strings — a known, documented
  `guard-i18n.sh` scanning gap noted in #921/#923's own reviews; unrelated
  to this diff.

## Safe-to-merge verdict

**Yes.** No findings. Full gate green (`gofmt`, `go build ./...`,
`go test ./internal/pages/...`, all CI-blocking guards from the repo's
`CLAUDE.md` list). TDD claim independently re-verified. Merging with
`merge_method: "merge"` (never squash/rebase — ut-docs#250) once CI is
confirmed green on the PR's head.
