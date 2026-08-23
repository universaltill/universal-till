# Code review: `http.Error` sites leaking raw/untranslated text — first batch (ut-docs#893)

**Card:** universaltill/ut-docs#893 (p3, complexity: medium)
**Branch:** `chore/893-http-error-i18n-sweep`
**Date:** 2026-08-23
**Scope:** `internal/pages/{audit_page,basket_page,external_api,invoice_page,journal_page,order_status,pending_pairings,receipt_designer,sync_admin,sync_assets,sync_sales}.go` (+ their tests), `scripts/ci/guard-i18n.sh` (comment only), `web/locales/{en,ar,fa,tr}.json`, plus `ut-plugin-language-{de,es}` (external repos — separate PRs, see below).

## What shipped

Continues ut-docs#316's sweep, which deferred ~86 more raw `err.Error()`
sites plus a long tail of one-off literals across `internal/pages`. This
card's own text explicitly scopes to "file-by-file or in a few batches
sized like #316 was" — this is the first such batch, covering the 11
smallest/simplest remaining files (25 `http.Error` sites) rather than the
whole remaining sweep in one diff.

1. **25 `http.Error` call sites** across the 11 files above, each triaged
   the same way #316 established: a raw error wrapping a `data`/`ui`/`os`/
   `http.Get` call (could carry SQL/driver/filesystem text or an internal
   ID) → `common.LogAndLocalizedError(w, r, status, key, logTag, err)`
   (22 of the 25); a clean hand-written validation string →
   `common.LocalizedError(w, r, status, key)` (3: `sync_assets.go`'s "bad
   path", `sync_sales.go`'s "bad journal batch" and its "apply failed at
   `<receipt>`: `<err>`" — the last of these drops the client-visible
   receipt number, since `syncPushTick` (the only caller of
   `POST /api/sync/sales`) never reads the response body, only the status
   code, and the receipt number stays in the server-side
   `logging.L().Errorf` call one line above, unchanged).
2. **A second, related leak found during independent review**: a raw
   `err.Error()` reaching the operator via `fmt.Fprintf` (not `http.Error`)
   at `invoice_page.go`'s `/api/invoices/issue` 409 branch, in a file this
   diff already touched. Fixed the same way, in the file's own established
   fragment-rendering convention (not the shared `LocalizedError` helper,
   which would have regressed this handler's styled-span response to a
   bare-text one): log via `logging.L().Errorf`, render
   `httpx.T(locale, "invoice.error.issue_failed")` in the same
   `<span class="muted">✗ %s</span>` shape the file's sibling error
   branches already use.
3. **14 new locale keys** (13 for the `http.Error` sites, 1 for the
   `invoice_page.go` finding above) in all of `web/locales/{en,ar,fa,tr}.json`
   — `audit.error.server`, `basket.error.server`, `ext.error.unreachable`,
   `pairings.error.server`, `sync.error.{server,bad_path,bad_batch,
   apply_failed}`, `invoice.error.{server,issue_failed}`,
   `journal.error.server`, `orders.err.server`,
   `designer.receipt.{logo_required,save_failed}` — and in the external
   `ut-plugin-language-{de,es}` packs (separate PRs, see below): core's
   `lang-pack-drift.yml` blocks on push to `main` if these drift.
4. **`scripts/ci/guard-i18n.sh`'s comment updated twice**: once to record
   this batch's 25 (later 26, after the `invoice_page.go` finding) sites
   as fixed; a second time, after review, to correct an inaccurate
   estimate of what's still unswept (see Review finding 1 below) — it now
   separates the ~63 remaining raw `err.Error()` leaks (fully scoped to
   the same 13 files #316's own comment already named) from ~231 more
   one-off hardcoded-literal sites, ~101 of which are in ~23 files this
   comment previously didn't mention at all.
5. **Test**: `TestAuditPage_RepoErrorNeverLeaksRawErrorToBody` in
   `audit_page_test.go` — drops the `audit_log` table (not closing the DB
   outright, which would also break the DB-backed `canPerform` permission
   check and mask the actual repo-error path under a 403) to force
   `ListAudit` to fail with a real SQLite driver error, then asserts the
   response is exactly the translated `audit.error.server` string and
   never contains `"no such table"`/`"audit_log"`.

## Review

Independent review via a fresh-context Opus subagent (complexity: medium,
matching the card's routing), isolated worktree, ~624s wall-clock.
Verdict: **safe-to-merge with minor notes**, no blocking issues. Findings
addressed:

1. **Real — `guard-i18n.sh`'s remaining-work estimate was under-inclusive.**
   The comment named exactly the 13 files carrying a raw `err.Error()`
   leak (correct — verified those 13 are the complete set) but described
   the total remaining count as "~193 sites... raw err.Error() leaks plus
   one-off hardcoded literals" as if that covered everything left. It
   didn't: ~101 more hardcoded-literal-only `http.Error` sites exist
   across ~23 further files (`users_page.go`, `self_order_shop.go`,
   `permission_settings_page.go`, and 20 more) this comment never named.
   Total remaining is ~294 across ~36 files, not ~193 across 13. Fixed:
   recounted every file in `internal/pages` (excluding the 11+1 swept and
   `catalog/handlers.go`'s 4 deliberate exceptions) with
   `grep -c 'http\.Error(w'` / `grep -c 'http\.Error(w, err\.Error()'`,
   confirmed the split (63 err-leak + 231 literal = 294), and rewrote the
   comment to separate the two classes explicitly with both file lists.
2. **Real, in-scope-adjacent — `invoice_page.go:304`'s raw `err.Error()`
   leak via `fmt.Fprintf`.** Not an `http.Error` call (so technically
   outside this card's literal title), but the identical defect class in
   a file this diff already touches — the reviewer's point that leaving
   it made the file "look swept" when it wasn't is correct. Fixed per
   "What shipped" #2 above, with a new regression-free path (no test
   added for this specific site — see "Explicitly deferred" below for why).
3. **Verified, not a defect — the `sync_sales.go` "apply failed" receipt-
   number drop.** Reviewer traced `syncPushTick` (the only caller) and
   confirmed it never parses the response body, only the status code —
   dropping the receipt number from the client-visible response is
   inert, not a regression. No change needed.
4. **Verified, not a defect — machine-to-machine sync endpoints going
   through the locale-translation helpers.** `sync_admin.go`/
   `sync_assets.go`/`sync_sales.go` serve another till's HTTP client, not
   a browser, so `httpx.T`'s locale resolution is semantically inert
   there (no client reads/string-matches the body) but not wrong — no
   change needed; noted for anyone continuing the sweep into
   machine-to-machine files.
5. **Accepted as-is (reviewer's own characterization, non-blocking)**:
   `order_status.go`'s two new `LogAndLocalizedError` calls use
   `http.Error` under the hood, which resets `Content-Type` to
   `text/plain` and skips the file's own local `fail()` HTMX-fragment
   helper its sibling error branches use — but this is byte-identical
   wire behavior to the pre-diff code (also `http.Error`), so not a
   regression, just a pre-existing inconsistency this diff didn't
   introduce. `sync_sales.go:271` discards the JSON-decode `err` instead
   of logging it (also pre-existing — the old literal didn't log it
   either). Locale-key insertion order in the JSON files isn't
   alphabetically perfect in a couple of spots (cosmetic; the guard only
   checks key *sets*, not order, per all four locale files being
   identical in structure).

## Independently re-verified beyond the reviewer's own checks

- Reran `gofmt -l .` (empty), `go vet ./...`, `go build ./...`,
  `go test ./internal/pages/... ./internal/pages/common/...`, and
  `bash scripts/ci/guard-i18n.sh` / `guard-i18n_test.sh` /
  `guard-data-access.sh` after the two post-review fixes (the
  `guard-i18n.sh` comment rewrite and the `invoice_page.go` fix +
  `invoice.error.issue_failed` key) — all clean/green.
- Recomputed the file-by-file `http.Error`/`err.Error()` split myself
  (see finding 1) rather than taking the reviewer's ~101/~294 estimate on
  faith — confirmed exact: 63 err-leak + 231 literal = 294 remaining, of
  which 130 literal + 63 err are in the 13 already-named files and 101
  literal are in ~23 newly-identified ones.
- Confirmed both language packs (`ut-plugin-language-de`/`-es`) carry all
  14 keys (13 original + `invoice.error.issue_failed` added after
  review) with real, non-English-identical translations, via each pack's
  own `scripts/check-key-drift.sh` run locally against this branch's
  `web/locales/en.json` (`UT_CORE_EN_JSON=<path>`):
  - de: `1525/1525 core keys translated, 0 known-untranslated ... 0 drift, 0 orphans`
  - es: `580/1525 core keys translated, 945 known-untranslated (pre-existing baseline, unaffected) ... 0 drift, 0 orphans`
  Opened universaltill/ut-plugin-language-de#74 and
  universaltill/ut-plugin-language-es#73. Their own CI (`key-drift`,
  checked against core's `main`) is expected to show red until this PR
  merges to `main` — same sequencing #316 hit; not a defect in either PR.
- Mutation-tested `TestAuditPage_RepoErrorNeverLeaksRawErrorToBody`
  myself in addition to the reviewer's own two mutations (reverting the
  `common.LogAndLocalizedError` call back to raw `http.Error(w,
  err.Error(), ...)`, and separately making `LocalizedError` skip
  translation) — both correctly fail the test; restored and confirmed
  green again.
- Ran the full `go test ./...` suite once, after all fixes: entirely
  green.

## Explicitly deferred (ut-docs#893, unchanged scope)

~294 more `http.Error` sites (63 raw `err.Error()` leaks, fully confined
to `backup_api.go`, `buttons_api.go`, `eod_api.go`, `import_page.go`,
`issue_report_page.go`, `pairing_api.go`, `plugin_api.go`,
`plugin_settings_page.go`, `pos_api.go`, `refund_page.go`,
`settings_page.go`, `sync_api.go`, `tax_codes_page.go`; plus ~231
one-off hardcoded literals spread across those same 13 files and ~23
further ones) not yet swept — the next batch(es), sized similarly, per
`scripts/ci/guard-i18n.sh`'s updated comment. Also deferred (new backlog
card, universaltill/ut-docs#912, unrelated defect class noticed in
passing): `external_api.go`'s plugin proxy uses `http.Get` with no
timeout and ignores `r.Context()`.

## Safe to merge

Yes. Full gate green, independent review's every finding fixed (2) or
verified as a non-issue (2) or accepted as a pre-existing, non-regressing
inconsistency (the rest), both language-pack PRs open and passing their
own drift checks locally against this branch's `en.json`.
