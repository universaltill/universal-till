# Code review: http.Error raw-leak sweep, increment 2 (ut-docs#944)

## What shipped

ut-docs#944 ("sweep raw `http.Error(err.Error())` leaks in
pos_api/refund_page/buttons_api" — the second slice of ut-docs#924's
overall sweep, itself the same defect class ut-docs#316/#893/#921/#923/#929
already fixed instances of). Re-grepped the three named files at pickup:
the ticket's own estimate of 14 call sites had drifted to **13** (a prior
increment already fixed one of `pos_api.go`'s three real `err.Error()`
sites, leaving a dead comment behind referencing it).

Files touched: `internal/pages/pos_api.go` (2 sites), `internal/pages/
refund_page.go` (5 sites), `internal/pages/buttons_api.go` (6 sites).
Each site now routes through `common.LogAndLocalizedError` — logging the
real error server-side and showing the operator a translated message
instead of raw Go/SQL error text.

Key choices, reusing an existing sibling key wherever the identical
underlying call already has one, adding a new one only where nothing
fit:

- `pos.error.server` (existing) — `pos_api.go`'s SimFail audit write and
  the generic branch of `UpdateSaleStatus`.
- `pos.error.sale_not_found` (new) — `UpdateSaleStatus`'s
  `data.ErrSaleNotFound` branch; previously leaked the sale ID itself
  (`sale not found: <id>`) in a 404 body, distinct enough from the
  generic DB-failure branch to earn its own key and status split.
- `refund.error.server` (new) — `refund_page.go`'s `ReturnedQuantities`
  (both the GET and POST call sites) and, after the review fix below,
  `EnsureStockLocation`/`EnsurePaymentMethod` too.
- `classifyTenderError(err)` (existing helper, same package) —
  `refund_page.go`'s `CompleteSale` call, reusing `pos_api.go`'s own
  tender-failure classifier rather than reinventing message selection for
  the identical underlying call.
- `designer.error.server` (new) — all 6 of `buttons_api.go`'s sites (4
  `ui.NewRenderer` parse failures, `UpdateOrder`, `SearchItems`), matching
  the file's existing `designer.*` locale namespace.

9 new/extended test functions cover all 13 sites (`buttons_api.go`'s two
DB-reachable sites — `UpdateOrder`/`SearchItems` — share one extended
test, `TestButtonsStoreErrorsSurfaceAs500`; the other 6 files' sites each
get a dedicated test). Each forces a **real** failure (a dropped SQLite
table under a live request, never a mock) and asserts both that the
localized message appears and the specific raw SQL/Go error fragment does
not.

## Independent review

Opus, fresh context, isolated git worktree (`complexity:medium` →
Sonnet-builds/Opus-reviews per the model-routing rubric). Verdict:
**safe to merge**, one should-fix (fixed by the reviewer, pulled into
this branch), one should-fix flagged as a separate follow-up (not fixed
here — see below), one nit (commit message corrected).

**Should-fix, fixed:** `refund_page.go`'s `EnsureStockLocation`/
`EnsurePaymentMethod` failures originally reused `pos_api.go`'s
`pos.toast.tender_failed` ("**Sale** could not be completed — try again
or ask an administrator for help"). Technically valid — same repo
method, same generic internal-DB-failure class — but a real UX defect:
the operator on the refund screen pressed **Refund**, not Tender, and
the same handler's own `ReturnedQuantities` failure two calls earlier
already showed the neutral `refund.error.server` copy, so the two
adjacent failure branches of one request contradicted each other in
tone. Same shape as the increment-1 review's "backup download reusing
'Backup failed'" finding. Repointed both call sites at
`refund.error.server`; both tests strengthened to explicitly assert the
sale-worded string does *not* appear (not just that the refund-worded
one does), so a regression back to the old key fails the test rather
than passing on a loose substring match.

**Should-fix, flagged as a separate follow-up (ut-docs#950), not fixed
here:** `refund_page.go`'s payment-provider refund gate leaks a raw
**plugin-originated** error string, untranslated —
`http.Error(w, "provider refund failed for "+method+": "+blocked.Error(), http.StatusPaymentRequired)`.
Same defect class, but a different literal shape (`err.Error()`
concatenated into a larger string, not passed alone) that this sweep's
grep didn't match, and — more importantly — a genuinely different design
question: `blocked` comes from a plugin's own response, which may
already carry operator-meaningful text a generic "something went wrong"
would actively make worse, not better. That call needs BA/Architect
input, not a mechanical key swap, so it's filed as its own card rather
than folded into this diff.

**Nit, fixed:** the original commit message claimed "13 new regression
tests, one per call site" while its own third paragraph correctly
explained 4 of the 13 sites deliberately have no forced-failure test —
a self-contradiction worth fixing before it's the thing future grep/
`git log` archaeology trusts. Corrected to "9 new/extended test functions
covering all 13 sites" (rewritten via a clean rebase before push, not an
amend-in-place, since the original had already been pushed once).

**Independently re-verified**, not taken on trust — the reviewer's own
TDD pass (not mine, since the fix's Dev step and the independent Review
step were different agents/models by design):
- 7 of the 13 sites' regression tests reverted-by-hand → re-run → the
  exact raw-leak symptom observed (paste-verified per site: table names,
  the `sale not found: <id>` string, etc.) → restored → re-run → green.
- The finer per-hunk check `buttons_api.go`'s own test needed (a single
  `t.Fatalf`-per-check test function only ever exercises its *first*
  failing branch on a whole-file revert) — isolated the `SearchItems`
  hunk specifically and confirmed its own assertion fires independently
  of the `UpdateOrder` hunk.
- The reviewer's own fix (the `tender_failed` → `refund.error.server`
  repoint) verified in both directions: reverting it back to the old key
  makes the strengthened tests fail with
  `expected the localized refund.error.server copy, got: Sale could not
  be completed…` — proof the tests actually discriminate between the two
  keys, not just match a loose substring.
- `classifyTenderError` reuse on the refund path confirmed **cannot**
  misclassify as "insufficient stock": `AllowNegativeInventory: true`
  (set unconditionally by this handler) makes the only reachable source
  of that message (`sales.go`'s in-transaction stock check) skip
  entirely for every refund, and the check's only other call site
  (`data/pos_repo.go`'s `CheckNegativeInventory`) has no production
  callers at all.
- `buttons_api.go`'s 4 untested `ui.NewRenderer` sites independently
  confirmed genuinely unreachable by any live request: `NewRenderer`
  parses a compile-time `embed.FS` via `template.ParseFS` with only
  compile-time-literal path arguments and a fixed `Funcs` key set (which
  panics rather than errors on mismatch anyway) — the call can only ever
  fail deterministically, identically, on every request or never; there
  is no live input that flips it. The reviewer also checked the
  Windows-path hazard (`stripWebPrefix` already calls `filepath.ToSlash`)
  and found no cross-platform hole either. Skipping a forced-failure test
  for these 4 sites is justified, not a coverage gap.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./internal/pages/...` — clean,
  re-run after the rebase that folded in the review fix and corrected
  commit message.
- Full test suite matching CI's actual invocation —
  `go test $(go list ./... | grep -v '/internal/plugins$')` (all
  packages green) plus `go test -timeout 20m ./internal/plugins`
  (independently, both before this diff and during the review) — green.
- All 16 CI-blocking guards from `.github/workflows/ci.yml`'s `build`
  job, including `guard-i18n.sh` (1586 keys, exact 4-locale parity) and
  `guard-docs-shots.sh` (23 routed topics × 4 locales, hash unchanged —
  this diff touches no rendered template, only `/api/` error-response
  text and `web/locales/*.json`, so no screenshot drift is expected or
  found).
- `guard-help-topics.sh` green — no manual/help-topic update owed: every
  changed handler is either under `/api/` (denylisted from route
  coverage) or, for `GET /refund/{receipt}` and `/ui/buttons`, an
  already-claimed page/fragment whose manual topic describes the screen
  itself, unaffected by an error-path message's wording.
- No secrets, no real client/shop names in any test fixture (generic
  "Apple"/`sale-refund-1`-style ids, matching the rest of this package's
  existing test fixtures).
- Each `DROP TABLE`-based test runs against its own on-disk `t.TempDir()`
  DB, not a shared DSN — no cross-test poisoning, confirmed by the full
  suite passing with `go test`'s default parallelism.

## Explicitly deferred (not fixed here, tracked separately)

1. **ut-docs#950** — `refund_page.go`'s payment-provider refund gate
   leaks a raw plugin-originated error string, untranslated. Different
   literal shape from this sweep's target pattern, and needs a BA/
   Architect design call (should the plugin's own text reach the
   operator at all?) rather than a mechanical key swap. See that card
   for the full writeup.
2. **Remaining #924 scope**, if any further raw-leak sites turn up
   elsewhere in the codebase beyond the three files #944 named.
3. Same two pre-existing, out-of-scope gaps increment 1 already noted
   and left tracked centrally rather than patched site-by-site: audit-log
   raw error text, and `common.LogAndLocalizedError`'s use of stdlib
   `log.Printf` instead of the app's leveled/structured logger.
4. `ut-plugin-language-{de,es}` packs need the standard follow-up for the
   3 new locale keys; `lang-pack-drift` is advisory-only on this PR and
   the German pack is already deep in a known, separately-tracked gap
   (ut-docs#297).

## Safe-to-merge verdict

Safe to merge. Should-fix from independent review fixed (pulled into
this branch) and re-verified in both directions; the one should-fix
genuinely out of this sweep's scope filed as its own card (ut-docs#950)
rather than silently dropped or scope-crept into this diff; commit
message nit corrected. All CI-blocking guards green; full test suite
green (matching CI's real invocation); TDD claims independently
re-verified by a different model than the one that wrote the fix,
including the reviewer's own fix verified in both directions.
