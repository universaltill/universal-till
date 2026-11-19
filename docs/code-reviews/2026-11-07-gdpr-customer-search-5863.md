# Review — GDPR erasure picker: literal LIKE match and snake_case rows (ut-docs#5863)

**Date:** 2026-11-07 · **Branch:** `fix/5863-gdpr-customer-search` (reviewed at `1a94db6`)
**Author model:** Sonnet (dev subagent) · **Reviewer model:** Opus 5.5 (independent subagent)

Pre-flight: web/help topic — n/a (no visible change to the erase picker beyond `%`, `_` and `\` now matching literally; the help topic doesn't describe wildcard behaviour); docs-shots — n/a (the picker renders the same); `reference/` — n/a (the endpoint is internal to settings.html and has no reference page); README — n/a; en.json/en.meta — n/a (no new keys); ADR — n/a; plan/price, platform claim, plugin taxonomy — n/a; review record — this file.

## What shipped
- `internal/data/pos_repo.go` `SearchCustomers`: the phone and email `LIKE` branches now use `likeLiteral.Replace(q)` with `ESCAPE '\'`. This follows the precedent in `SearchCustomersForPicker` (ut-docs#5589). The name branch still uses `instr(search_fold(name), SearchFold(q))`. `instr` is a literal substring match with no wildcards, and `SearchFold` trims, so passing the raw `q` there is correct.
- `CustomerSummary` gets `json:"id|name|phone|email"` tags, so `GET /api/data/customers` returns snake_case rows (CLAUDE.md "JSON snake_case").
- `web/ui/pages/settings.html` erase picker reads `c.id/c.name/c.phone/c.email`.
- Tests: `internal/data/search_customers_like_5863_test.go`, `internal/pages/data_api_test.go` `TestGetCustomers_SnakeCaseKeys_5863`.
- **Orchestrator scope correction:** the Dev also added `likeLiteral` to `SearchActiveItems` and `SearchItemsForShortcuts` without an `ESCAPE` clause. Without `ESCAPE`, SQLite treats the backslash as an ordinary character, so any item search containing `%`, `_` or `\` would have stopped matching. Both hunks were out of scope and the orchestrator reverted them before review. They are not in the reviewed diff.

## Consumers of the JSON shape
I grepped `web/`, `e2e/`, `internal/` and `demo_mode.go`. The only reader of `CustomerSummary` JSON is the settings.html erase picker, and it has been updated. `e2e/tests/settings-pos-notice-918.spec.ts` intercepts `/api/data/customers**` but only delays the request and never reads the rows. `demo_mode.go` just allow-lists the route. The promotion picker uses its own `CustomerPick` and endpoint (#5589). Go callers (`gdpr_erasure_3253_test`, `search_fold_test`, `reset_test`) use the struct fields, which the JSON tags don't affect.

## Review findings
- L1 Blank `q` returns the first 25 customers, because `instr(x,'') = 1` and the pattern becomes `'%%'`. — **accepted, pre-existing.** The change doesn't alter this. For a manager-gated erasure picker it is arguably intended ("browse"). `gdpr_erasure_3253_test`'s `""` case still passes.
- L2 The bare-wildcard part of the data test asserts `len(got) > 1` → fail, so one wrong match would slip through. — **accepted.** The first loop already tells the broken and fixed versions apart: on the old code `"100%"` and `"a_b"` each return 2 rows. The `` `C:\` `` case only fails on a half fix (`ESCAPE` present but the backslash not escaped). That is still worth keeping.
- L3 SQLite `ESCAPE '\'` inside a Go raw string: the SQL text contains a single backslash, which is the intended single-character escape. The Go raw-string test fixture `'C:\x'` is likewise a literal backslash, since SQLite string literals have no backslash escapes. — **no issue.**
- Info: an untracked `.notes-5863-dev.md` in the working tree is Dev scratch. It must not be committed.

No blocking or medium findings. The repository pattern is respected: SQL stays in `internal/data`.

## Verification
- I re-checked both TDD claims in a throwaway worktree by reverting `pos_repo.go` to `origin/main`, running the test, then restoring:
  - `TestSearchCustomers_LikeWildcardsLiteral_5863` failed on the old code with `"100%": want [c-a], got [c-a c-b]` and passes with the fix.
  - `TestGetCustomers_SnakeCaseKeys_5863` failed on the old code with `key "id" … got map[Email:… ID:… Name:… Phone:…]` and passes with the fix.
- `go build ./...` passes. `go vet ./internal/data/ ./internal/pages/` is clean. `gofmt -l` is clean.
- `go test ./internal/data/ -count=1` passes. `go test ./internal/pages/ -run 'Customer|Erase|Data' -count=1` passes.
- `guard-data-access.sh`, `guard-i18n.sh` and `guard-fixed-dates.sh` all exit 0.
- `golangci-lint run ./internal/data/ ./internal/pages/`: 0 issues.
- **Not run by the reviewer:** the erase-flow e2e. A separate Tester agent checks it.

## Verdict
Approve (pending the Tester's erase-flow e2e).

## Deferred
- None. If item search also needs literal wildcard matching, that needs its own card with `ESCAPE '\'` on every `LIKE` it touches. It must not be folded into this branch.

## Tester (separate agent) and the stop

- Mutation check repeated by the Tester: each new test fails with its half of the fix removed and passes with it restored. `go vet` is clean.
- **Open gap:** no e2e drives the erase picker with real results. `settings-pos-notice-918` only holds the request open, so nothing checks the `c.name`/`c.phone`/`c.email`/`c.id` rendering in `settings.html`. Nobody looked at the screen. The e2e harness (`npm ci`, browser, till build) didn't fit the card's budget.
- The card went over its easy-tier budget (45 min) before merge. The PR stays open for the owner, card in Admin Review + `budget-exceeded`.
