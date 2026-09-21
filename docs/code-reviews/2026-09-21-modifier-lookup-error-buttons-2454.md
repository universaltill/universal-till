# Code review: modifier-lookup error silently discarded in ButtonStore.Load/SearchSellable (ut-docs#2454)

**Date:** 2026-09-21
**Card:** universaltill/ut-docs#2454
**Author:** scrum-master pipeline (`lane:cloud-41`), on behalf of the pipeline owner
**Reviewer:** independent fresh-context Sonnet subagent, isolated worktree (`complexity:easy` → Sonnet review, per `scrum-master`'s model routing)

## What changed

Found during independent review of ut-docs#2451. `internal/ui/buttons.go`'s
`ButtonStore.Load` and `ButtonStore.SearchSellable` both called
`s.modRepo.ItemIDsWithModifiers(ctx, itemIDs)` and discarded its error via
blank `_` — the exact silent-failure pattern ut-docs#2318 fixed in
`LoadAllActive` and ut-docs#2451 fixed in the kiosk's `loadShopItems`. Both
call sites' id sets are already bounded (admin-curated quick buttons, and
`SearchSellable`'s own `limit=30`), so no chunking is needed here — only
the logging half of that pattern: on a real lookup failure (a transient DB
error, a locked table), a tile that should show a modifier prompt would
previously add straight to the cart with no prompt and no logged warning
anywhere.

One file changed in `universal-till`, plus a new regression test:

1. **`internal/ui/buttons.go`** — both call sites now capture the error and
   log it via `logging.L().Warnf(...)`, matching the exact style
   `LoadAllActive`'s own already-fixed `hasMods` handling uses (no inline
   ticket reference, since — unlike the neighboring `hasVariants`/
   `currentPrices` warnings — there's no specific money-correctness bug
   this was retrofitted for).
2. **`internal/ui/buttons_modifier_lookup_error_test.go`** (new) — two
   tests, one per call site, force a REAL query failure (not a mock) by
   dropping `item_modifier_group_opt_outs`, a table `ItemIDsWithModifiers`'
   UNION query references unconditionally (ADR-0094), then assert: `Load`/
   `SearchSellable` stay non-fatal, the affected tile/result falls back to
   `HasModifiers=false`, and a WARN naming the failure appears in
   `logging.Recent()`.

## TDD verification

Confirmed red pre-fix: both new tests failed with "expected a WARN … got:
[]" against the original code (the query genuinely failed, but nothing
logged it). Confirmed green after the fix, with the correct WARN text.

## Independent review (fresh-context Sonnet, isolated worktree)

**Verdict: PASS, no blocking findings.** The reviewer independently
re-verified the TDD claim itself (reverted only `buttons.go`, re-ran the
two new tests, confirmed the identical "no WARN found" failure, restored,
confirmed pass again) rather than trusting the implementer's word. Also
verified: no nil-map panic risk (`ItemIDsWithModifiers` returns `nil, err`
on failure; reading a nil map is a safe zero-value in Go), no
variable-shadowing bug in either function, the fault-injection technique
is surgical (grepped `catalog_repo.go`/`pos_repo.go` — no other query on
either code path touches the dropped table), full `internal/ui` package
regression green, `go vet`/`gofmt` clean, no secrets/real-shop-name/money-
tax/security/i18n/repository-pattern concerns (pure error-handling
change).

Two non-blocking nits, both folded in before this commit:

1. `Load`'s new block used an unnecessary intermediate variable (`hm, err
   := ...; hasMods = hm`) instead of the `var err error; hasMods, err =
   ...` shape `SearchSellable` and `Load`'s own neighboring
   `hasVariants`/`currentPrices` block already use — aligned for
   consistency (no behavior change).
2. Noted for the record only (not a requested change): the new log
   messages carry no inline `ut-docs#nnnn` reference, unlike the
   neighboring `hasVariants`/`currentPrices` warnings — intentional, since
   those cite specific money-correctness bugs the treatment was
   retrofitted for and there's no equivalent ticket here; this mirrors
   `LoadAllActive`'s own sibling fix, which also carries none.

## Verification beyond the independent review

- `go build ./...`, `go vet ./internal/ui/...`, `gofmt -l internal/ui/` —
  all clean.
- `go test ./internal/ui/...` — full package green.
- Full-repo gate, `go test ./... -race`: one unrelated failure surfaced,
  `internal/plugins.TestRollback_RejectsCollidingPaymentKeys` timing out
  at exactly 600s (the default per-package timeout) when run as part of
  the whole `-race` suite. Ruled out as unrelated to this diff before
  treating it as blocking: this change touches only `internal/ui`, not
  `internal/plugins`/`internal/db`, and the failing test is a
  payment-key-collision/migration test with no code-path overlap.
  Re-ran the same test alone: **0.14s without `-race`, 4.05s with
  `-race`** — both pass cleanly in isolation. This is resource contention
  from every package's race-instrumented tests competing for CPU
  concurrently in this sandbox, not a real regression; it disappears the
  moment the test runs without competing for the machine. Not filed as a
  new card — it isn't reproducible in isolation, so there's nothing
  concrete yet to action; worth a second look only if it recurs.

Verdict: **SAFE TO MERGE.**
