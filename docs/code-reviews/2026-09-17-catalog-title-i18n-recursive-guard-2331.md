# 2026-09-17 — Catalog/Option sets hardcoded `<title>` literals + recursive guard (ut-docs#2331)

## What shipped

Two hardcoded English page `<title>` literals in
`internal/pages/catalog/handlers.go` — `"Catalog"` (the `/catalog` handler)
and `"Option sets"` (the `/catalog/option-sets` handler) — replaced with
`httpx.T(httpx.RequestLocale(r), "nav.catalog")` and
`httpx.T(httpx.RequestLocale(r), "items.option_sets.name")` respectively,
reusing the same locale keys the `/items` left rail already uses for these
two destinations (`internal/uislot/slot.go`'s `CoreItems`).

Both literals survived ut-docs#2297/PR#1189's original ~48-literal sweep
(and that sweep's own CI regression guard, `scripts/ci/guard-i18n.sh`
check 10) because the sweep's glob, `glob.glob("internal/pages/*.go")`,
is non-recursive — `internal/pages/catalog/handlers.go` lives one
directory deeper and was never scanned. Check 10's glob is now
`glob.glob("internal/pages/**/*.go", recursive=True)`, closing this class
of gap for any future handler package under `internal/pages/**`.

A repo-wide recursive grep (`internal/pages/**/*.go`, excluding
`_test.go`) confirms no other `internal/pages/**` handler has the same
gap — the only other hardcoded-looking `"title"` literal is
`internal/pages/index_page.go`'s `i18n:ignore`-tagged `"Universal Till"`
brand name (deliberate, from ut-docs#2297 itself).

## Independent review (Sonnet, fresh context — complexity:easy routing)

Spawned a fresh-context subagent (worktree-isolated) with no visibility
into the implementation reasoning. It independently:

- Re-derived the TDD claim: reverted only the glob change, confirmed the
  new `TitleLiteralNested` regression test in
  `scripts/ci/guard-i18n_title_test.sh` fails exactly as claimed, restored
  the fix, confirmed it passes again.
- Confirmed the recursive glob (448 files) vs. the old flat one (387
  files) pulls in the expected extra subdirectory files, and that the
  guard's own `_test.go` exclusion still works correctly against the
  larger file set.
- Confirmed, via an independent grep, that no other hardcoded page
  `<title>` literal exists under `internal/pages/**` besides the
  deliberately-ignored brand name.
- Ran the full gate: `gofmt -l .` (clean), `go build ./...`, `go vet
  ./internal/pages/...`, `go test ./internal/pages/catalog/...` (ok),
  `golangci-lint run ./internal/pages/catalog/...` (0 issues), both guard
  scripts (clean).
- Cross-checked `nav.catalog`/`items.option_sets.name` against
  `internal/uislot/slot.go`'s `CoreItems` rail entries for `/catalog` and
  `/catalog/option-sets` — confirmed these are the correct, already-in-use
  keys for these exact destinations, not a mismatched label.
- Confirmed no new locale key was added (`git diff main -- web/locales/`
  empty) — no `lang-pack-drift` follow-up triggered.
- Confirmed no file-write/`os.MkdirAll`/`paths.Data` concerns (no file
  writes in this diff).
- Confirmed this is a browser-tab `<title>` change only, not a
  shop-owner-visible screen/feature change — no `web/help/` manual update
  needed (both `/catalog` and `/catalog/option-sets` already have topics
  predating this change, and their content is unaffected).

**Verdict: safe to merge. No findings.**

## Verified beyond automated tests

- `nav.catalog` / `items.option_sets.name` exist in all four shipped
  locales (`ar`, `en`, `fa`, `tr`) — confirmed by grep, not by trusting
  the guard alone.
- Confirmed live against current `main` before starting that the bug was
  real (exact line/text match to the ticket) and that the dependency this
  card was originally `blocked:dep` on (ut-docs#2297/`universal-till#1189`)
  is in fact merged (check 10 and the `page.title.*` keys already exist on
  `main`).

## Deferred / out of scope

- The ticket's own acceptance criteria are fully covered by this PR (both
  literals fixed, repo-wide recursive check run, guard widened with a
  regression test) — nothing deferred.
