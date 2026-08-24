# Code review: http.Error raw-leak sweep, increment 3 (ut-docs#945)

## What shipped

ut-docs#924 tracks the remaining `http.Error(w, err.Error(), status)`
raw-error-leak call sites across `internal/pages` (the same defect class
ut-docs#316/#893 already fixed 26+ instances of, and increments 1/2
— ut-docs#924/#944 — continued). This is **increment 3**: 18 call sites
across `internal/pages/eod_api.go` (6), `internal/pages/plugin_api.go`
(6), and `internal/pages/tax_codes_page.go` (6).

Each site now routes through `common.LogAndLocalizedError` (or, for two
sites in `tax_codes_page.go` that are dead code by construction, still
routed defensively through the same helper) — logging the real error
server-side and showing the operator a translated message instead.

Locale keys: 12 new keys added to all four locale files (`en`/`ar`/`fa`/
`tr`) — `eod.err.{check_failed,list_failed,range_failed,
retention_save_failed,archive_export_failed}` (new namespace),
`plugins.error.{malformed_form,grant_failed,revoke_failed,
trust_update_failed}` (new namespace), `taxcodes.err.{invalid_form,
list_failed,save_failed}` (existing namespace, extended). Key-set parity
confirmed across all four files: 1595 keys, zero drift.

15 of the 18 sites got dedicated forced-failure regression tests (13 from
Dev, 1 more added by this review — see below); 3 are genuinely-unreachable
dead branches (two `parseTaxCodeForm` fallback arms, confirmed by reading
the function: it only ever returns `nil` or an already-`httpStatusError`-
wrapped error; and `eod_api.go`'s `json.Marshal(rep)` path, discussed
below) — documented in-code rather than faked.

## Independent review

Opus, fresh context, isolated git worktree (complexity:medium →
Sonnet-builds/Opus-reviews per the model-routing rubric). Verdict:
**safe to merge with fixes applied.**

**Should-fix, fixed — the "untestable" `taxcodes_list_render` site was
actually testable.** Dev's reasoning ("any DB failure that breaks the
`SELECT` would break the preceding `INSERT` first") only holds for
*schema*-level failures. It misses *data*-level failures: `SQLite`'s
dynamic typing lets a non-numeric value be stored in an `INTEGER` column
via `UPDATE ... rate_basis_points = 'not-a-number'` without the write
itself failing — the row insert/update succeeds cleanly, and the *read*
back through `rows.Scan` is what fails, cleanly separating the two calls
on a single connection with no races or triggers needed. Added
`TestTaxCodesAPI_Create_TableRenderErrorIsLocalized`, which asserts the
poisoned write actually landed (`COUNT(*) = 1`) before asserting on the
response, so it can't silently degrade into a duplicate of the sibling
create test. This closes a real gap — the leak this test catches put a
raw DB *value* on screen, not just a raw error string.

**Should-fix, fixed — a misleading code comment.** The original comment
next to `eod_api.go`'s `json.Marshal(rep)` claimed `EODReport` "is
composed only of strings/ints/int64/plain-struct slices" and therefore
can't fail marshalling. False: `data.DeptSales.Qty` is a `float64`, and
the review verified experimentally that an `EODReport` carrying a
`+Inf`/`NaN` `Qty` does fail `json.Marshal`, that `Departments` populates
on an ordinary single-day range export, and that `+Inf` actually persists
into and reads back from `sale_lines.quantity` (`REAL`). The *code* was
already correct (the branch is handled defensively); the comment asserted
a false invariant a future maintainer could act on. Rewritten to state
the real situation — this path is reachable only via a separate,
pre-existing defect (see below), not truly dead code, but forcing it here
would mean fixing a different bug as a side effect of this one, so it's
still left undemonstrated by a dedicated test, now honestly documented as
such rather than claimed impossible.

**Nit, fixed**: a stale comment in `tax_codes_page_test.go` referencing a
capture step that wasn't actually done.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./internal/pages/...` — clean
  (re-run personally after pulling the reviewer's fixes into the main
  checkout, not just trusted from the review transcript).
- Full test suite matching CI's real invocation —
  `go test $(go list ./... | grep -v '/internal/plugins$')` (all green)
  plus `go test -timeout 20m ./internal/plugins` (green) — re-run
  personally post-fix.
- `go test ./internal/pages/... -shuffle=on` — green, confirms the new
  test doesn't depend on execution order (fresh `t.TempDir()` DB, same as
  its siblings).
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job
  (16 scripts) — all pass, re-run personally, including `guard-i18n.sh`
  and a real `make docs-shots` (23 topics × 4 locales, 92 screenshots) —
  the diff to `web/help/img/manifest.json` is a single-line
  `surface_sha256` refresh with zero `.png` pixel changes (the review's
  comment-only edits to `tax_codes_page.go`/`eod_api.go` moved the
  `GET /catalog/tax-codes` surface hash a second time; re-verified after
  pulling those fixes in).
- TDD claim re-verified independently, **all 18 sites**, not a sample:
  the review reverted each fix one hunk at a time, confirmed every one of
  the 14 originally-tested sites (13 from Dev + the review's own new one)
  fails with the genuine raw-error-leak symptom the test is meant to
  catch, and confirmed the 3 claimed-untested sites are in fact untested
  — restoring and re-confirming green after each. Isolation was checked
  too: reverting one site's fix never failed a neighbouring site's test
  (notably the three `plugin_*_parseform` sites, which share one
  table-driven test with independent subtests per route).

## Explicitly deferred (not fixed here, tracked separately)

1. **The sweep's own grep pattern under-counts this defect class.**
   #316/#893/#924/#945 all scope by matching
   `http.Error(w, err.Error(), …)` literally. The review found **9 more
   sites** of the identical defect written as
   `http.Error(w, fmt.Sprintf("…: %v", err), …)` — 8 in
   `internal/pages/plugin_api.go` (a file this very increment touched)
   and 1 in `internal/pages/plugin_page.go`. Increment 4 (ut-docs#946)
   will not find these either unless the search pattern is widened.
   New backlog card needed.
2. **No finiteness guard on quantity input**
   (`internal/pages/pos_api.go:345`): `strconv.ParseFloat("inf")` returns
   `+Inf` with a nil error and passes the `> 0` check, and nothing
   downstream rejects it — sibling parsers (`tax_codes_page.go`,
   `catimport.go`) correctly check `math.IsInf`, this one doesn't. A
   non-finite quantity can be persisted, after which a single-day Z-report
   range export 500s (the exact `json.Marshal` path this increment
   documented above, now with a known real trigger). Separate defect,
   separate file — new backlog card needed, not fixed here.
3. **Locale namespace nit, not fixed deliberately**: `eod.err.*` is a
   brand-new top-level root where `reports.eod.*` is the file's own
   established namespace (and where the new keys are physically placed in
   `en.json`). Defensible under the `<subsystem>.error.*` convention used
   elsewhere (`sync.error.*`, `invoice.error.*`); renaming would touch 5
   files for a purely cosmetic gain. Flagged, not fixed.
4. **`ut-plugin-language-{de,es}` follow-up** for these 12 new keys — not
   done in this PR; will be picked up the same way increments 1/2's keys
   were (a same-cycle drift-fix once this merges and core's `main` shows
   the new keys, mirroring the fix already landed this cycle for
   increments 1/2's 6-key drift: `ut-plugin-language-de#83`,
   `ut-plugin-language-es#82`).
5. **Translation quality caveat**: the NAS Ollama translation endpoint
   (192.168.1.231) was unreachable from this cloud pipeline session
   (confirmed via timed-out connection) — same accepted fallback this
   pipeline has used before (`2026-08-23-sale-search-stranded-headers-422.md`,
   `2026-08-24-tender-error-i18n-fallback.md`). Translated directly
   instead; the review separately checked the ar/fa/tr strings for script
   correctness, register consistency with neighbouring keys, and
   terminology reuse (e.g. `taxcodes.*`'s existing "tax code" translations
   reused verbatim, not reinvented) — found them plausible and consistent,
   not gibberish. Recommend re-running these 12 keys through the
   documented NAS pipeline once reachable to confirm no drift from what
   that process would have produced.

## Safe-to-merge verdict

Safe to merge. The one real should-fix (a missing regression test for a
site that puts a raw DB value, not just a raw error string, on screen) is
fixed and independently re-verified via revert→fail→restore→pass; the
misleading-comment should-fix is corrected; TDD claims re-verified for
all 18 sites, not a sample; full gate (build/vet/test/guards, including a
real docs-shots regen) green, re-run personally after merging the
review's fixes into the base diff.
