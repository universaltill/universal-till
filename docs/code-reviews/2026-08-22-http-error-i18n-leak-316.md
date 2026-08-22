# Code review: `http.Error` sites leaking raw/untranslated text to the operator (ut-docs#316)

**Card:** universaltill/ut-docs#316 (p3, complexity: medium)
**Branch:** `feat/316-http-error-i18n-leak`
**Date:** 2026-08-22
**Scope:** `internal/pages/common/errors.go` (new), `internal/pages/*.go` (~26 files), `internal/pages/catalog/handlers.go`, `internal/data/catalog_repo.go`, `scripts/ci/guard-i18n.sh` (comment only), `web/locales/{en,ar,fa,tr}.json`, plus `ut-plugin-language-{de,es}` (external repos — separate PRs, see below).

## What shipped

The ticket's own "What to decide and build" section scoped the concrete
deliverable to two specific classes, deliberately narrower than the raw
title's "~45 sites" — the acceptance criteria's "no `http.Error` call in
`internal/pages` renders raw text" is the long-term goal, not what one
card's diff should carry (see "Explicitly deferred" below):

1. **New shared helpers**, `internal/pages/common/errors.go`:
   - `LocalizedError(w, r, status, key)` — `http.Error` with the key
     translated via `httpx.T(httpx.ResolveLocale(w, r), key)`.
   - `LogAndLocalizedError(w, r, status, key, logTag, err)` — logs the raw
     `err` server-side (never lost, just kept off the operator's screen),
     then calls `LocalizedError`. For errors that can carry SQL/driver
     text or an internal ID.
2. **~40 call sites** across `internal/pages/*.go` doing
   `http.Error(w, "manager or admin required"/"manager or admin role
   required", http.StatusForbidden)` → `common.LocalizedError(w, r,
   http.StatusForbidden, "common.error.manager_or_admin_required")` (the
   two English variants deliberately consolidated into one key/message —
   no test asserted on either literal, verified before merging them).
3. **`http.Error(w, "invalid upload", http.StatusBadRequest)`** (4 files)
   → `common.LocalizedError(w, r, http.StatusBadRequest,
   "common.error.invalid_upload")`.
4. **`catalog/handlers.go`**: 22 of its 26 `http.Error(w, err.Error(),
   status)` sites — the ones wrapping `pos.*`/`data.*`/`modRepo.*`
   domain-layer calls, which can leak real SQL constraint text (e.g.
   `constraint failed: UNIQUE constraint failed: items.sku`) or, in the
   file-upload handlers, an absolute server filesystem path — now go
   through `common.LogAndLocalizedError` (`catalog.error.server` for 500s,
   `catalog.error.invalid_request` for 400s). **Deliberately untouched**:
   the 4 sites wrapping `parseItemInput`/`validateLookups` — clean,
   hand-written, bounded validation strings (`"name and price required"`,
   `"invalid categories id"`, …), never SQL/ID-bearing, and pinned verbatim
   by `TestItemCreate_InputValidation` — left as raw `err.Error()` with an
   explanatory comment, out of this class entirely.
5. **Duplicate-SKU stays specific, not generic** (added during review, see
   below): `data.ErrSKUExists` (new sentinel, mirrors the existing
   `ErrTaxCodeNameExists`/`isUniqueViolation` pattern in
   `catalog_repo.go`) from `CreateItem`/`UpdateItem`/`CreateVariant`/
   `UpdateVariant`, surfaced via a new `skuAwareError` helper in
   `catalog/handlers.go` as `catalog.error.sku_exists` ("That SKU is
   already in use — choose a different one.") instead of falling into the
   generic `catalog.error.invalid_request`.
6. **6 new locale keys** (`common.error.manager_or_admin_required`,
   `common.error.invalid_upload`, `catalog.error.server`,
   `catalog.error.invalid_request`, `catalog.error.sku_exists`) in all of
   `web/locales/{en,ar,fa,tr}.json`, **and** in the external
   `ut-plugin-language-{de,es}` packs (separate PRs — see below) — core's
   `lang-pack-drift.yml` blocks on push to `main` if these packs drift.
7. `scripts/ci/guard-i18n.sh`'s comment updated to justify why its
   `http.Error` exemption isn't narrowed yet (the much larger remaining
   sweep — ~86 more `err.Error()` sites plus a long tail of one-off
   literals — is ut-docs#893, not this card).
8. Tests: `internal/pages/common/errors_test.go` (translation actually
   happens with the real translator, locale resolution from `?lang=`, the
   raw error reaches the log but never the body — not just "a body gets
   written"), `internal/data/catalog_repo_sku_conflict_test.go` (all four
   `ErrSKUExists` paths), plus body assertions added to the three existing
   `TestItem{Create,Update}_DuplicateSKUIs400`/
   `TestVariantUpdate_DuplicateSKUIs400` tests.

## Review

Independent review via a fresh-context Opus subagent (complexity: medium),
isolated worktree, ~600s wall-clock. Verdict: **yes-with-fixes-needed**.
Every required finding fixed in this diff before merge:

1. **Blocker — language-pack drift.** The 4 (now 5, after finding 3 added
   a 5th key) new base keys were missing from `ut-plugin-language-de`/`-es`,
   which `check-lang-pack-drift.sh` (blocking on push to `main`, advisory
   on PR) would have caught the moment this merged. Fixed: cloned both
   pack repos, added translated keys, verified with each pack's own
   `scripts/check-key-drift.sh --run against a local core en.json` (0
   drift, both exit 0), opened
   universaltill/ut-plugin-language-de#64 and
   universaltill/ut-plugin-language-es#63.
2. **Real-but-minor — the translation test didn't test translation.**
   Both original tests ran with no translator wired, so `T` fell back to
   the raw key — a mutation that deleted the `httpx.T`/`ResolveLocale`
   calls entirely still passed both tests. Reproduced the reviewer's claim
   by making that exact mutation locally (3 of the file's tests correctly
   failed against it, 2 correctly still passed since they don't depend on
   real translation) before restoring. Fixed: added
   `TestLocalizedErrorActuallyTranslates` (real `web/locales` translator,
   asserts the actual English string), `TestLocalizedErrorResolvesLocaleFromRequest`
   (`?lang=tr`, asserts the Turkish string), and
   `TestLogAndLocalizedErrorLogsTheRealError` (captures `log` output,
   asserts the raw error detail reached it). Also fixed the fixture: the
   fake leak used a `pq:` (Postgres) error string in a SQLite codebase —
   replaced with the actual shape `catalog_repo.go` produces
   (`"insert item: UNIQUE constraint failed: items.sku"`).
3. **Real-but-minor — duplicate-SKU lost its actionability.** Generic
   `catalog.error.invalid_request` named nothing for the single commonest
   operator mistake on this form, where the previous raw `err.Error()`
   at least said "SKU". Fixed per "What shipped" #5 above: `ErrSKUExists`
   sentinel + `skuAwareError`, matching the `FriendlyBarcodeConflict`
   precedent ut-docs#303 already established for barcode conflicts.
   `isUniqueViolation`'s single-constraint assumption verified safe for
   `items`/`item_variants` by grepping every migration for a second
   `UNIQUE`/`CREATE UNIQUE INDEX` on either table — only `sku` (`001_init.sql`).
4. **Real-but-minor — the guard comment was spliced mid-sentence and
   wrong.** Reverted the original clause to its unmodified form and added
   a new, correctly-placed paragraph after the existing block, with the
   accurate count (22 of 26, not "26") and the real follow-up card number
   (ut-docs#893).
5. **Missing review record.** This file.

Not fixed, accepted as-is (nitpicks, reviewer's own characterization):
duplicate (harmless, identical) `Set-Cookie` on two call sites that
already resolved the locale before erroring through the helper; the
`log.Printf` vs. `internal/data`'s `logging.L().Warnf` divergence
(consistent with the existing `barcode_conflict.go` precedent, a
conscious fork not an oversight); locale-key placement inside the
`catalog.error.*` run in `web/locales/*.json` rather than a separate
`common.*` block (cosmetic, guard only checks the key *set*); a
partially-translated screen on `/api/catalog/item/image` (4 sites there
are explicitly ut-docs#893 scope, noted so it doesn't read as an
oversight).

## Independently re-verified beyond the reviewer's own checks

- Reran `go build ./...`, `go vet ./...`, `gofmt -l .` (empty),
  `go test ./internal/pages/... ./internal/data/... ./internal/httpx/...
  ./internal/pos/...` and the full `go test ./...` after every fix above —
  all green.
- Ran `go test ./... -race` once, full suite: the only failure was
  `internal/plugins.TestWasmSync_MarksMissingBinaryBrokenAndHealsOnRecovery`
  timing out at 600s inside wazero's JIT compiler backend (goroutine trace
  shows it stuck mid-`Compile`, nothing under `internal/pages`/`internal/data`
  anywhere near the trace) — a package this diff never touches. Reproduced:
  passes in under 2s without `-race`, and in ~19s *with* `-race` run in
  isolation (not under full-suite CPU contention), and the whole
  `internal/plugins` package passes in 94s under a plain `go test ./...`
  run. Classified as an unrelated, non-reproducing timeout (an environment/
  contention artifact of the race detector on a JIT compiler under full-suite
  load), not a regression from this change.
- Mutation-tested the new `errors_test.go` cases myself: stubbed
  `LocalizedError`/`LogAndLocalizedError` down to a bare `http.Error(w,
  key, status)` with no translation and no logging — 3 of 5 tests
  correctly failed (the 2 that didn't are the no-translator-wired case,
  which is correct behavior, not a gap); restored and confirmed all 5
  pass again.
- Confirmed `TestItemCreate_InputValidation`'s 6 subtests (the untouched
  `parseItemInput`/`validateLookups` class) still pin their exact original
  English strings verbatim.

## Explicitly deferred (ut-docs#893)

~86 more raw `http.Error(w, err.Error(), status)` sites outside
`catalog/handlers.go`, and a long tail of one-off hardcoded English
literals across `internal/pages`, not yet swept — filed as a follow-up
card before this PR opened, sized like this one (one focused increment),
not attempted here to avoid an unreviewable diff.

## Safe to merge

Yes. Full gate green, independent review's every required finding fixed
and re-verified, both language-pack PRs open and passing their own
drift checks locally against this branch's `en.json`.
