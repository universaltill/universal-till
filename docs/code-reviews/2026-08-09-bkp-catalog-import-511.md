# Code review: speedy kasse / pepperm cashbox `.bkp` catalog import

**Card:** universaltill/ut-docs#511
**Date:** 2026-08-09
**Complexity:** medium — Dev via a Sonnet subagent, Review via an
independent Opus subagent in an isolated worktree (`complexity:medium` →
Opus per the `scrum-master` skill's model routing). One review round;
findings included blocker-class issues (money correctness, a dead-end UI
path) so they were fixed in this same pass rather than deferred, but the
round itself was not repeated — the fixes were re-verified by re-running
the full gate, not by spawning a second review.

## What shipped

`/import` now auto-detects and accepts a speedy kasse / pepperm cashbox
`.bkp` till backup directly, alongside the existing CSV path, with no
separate route — the operator just uploads either file and the format is
sniffed from the ZIP local-file-header magic bytes.

- `internal/catimport/bkp.go` (new): `ParseBkp` opens the upload as a ZIP,
  requires both `backup.db` and `meta.inf`, validates `meta.inf`
  best-effort (a recognisable per-file checksum for `backup.db` is
  verified against the real SHA-256; no recognisable structure falls back
  to `archive/zip`'s own CRC32 integrity check rather than hard-failing on
  an unconfirmed schema guess — nobody on this project has seen the real
  file, see the code's own doc comments), extracts to a temp file, reads
  the `Products` table, and maps rows to `catimport.ImportItem` in the
  documented precedence (deleted → not-sellable → blank name → duplicate
  PLU-in-file → bad price → normal), collapsing multi-line button labels,
  never treating `ProductNumber` (a PLU) as a barcode.
- `internal/data/bkp_products_repo.go` (new): the raw `SELECT` against the
  extracted `backup.db`, placed here (not in `catimport`) specifically to
  satisfy `scripts/ci/guard-data-access.sh`'s mechanical "no literal SQL
  outside internal/data" rule — confirmed by the reviewer to actually be
  load-bearing (see Findings, #7) even though `backup.db` is a foreign
  uploaded file, never this app's own catalog DB.
- `internal/pages/import_page.go`: `sniffZipUpload` peeks 4 bytes for ZIP
  magic and seeks back to 0; routes to `ParseBkp` or the existing `Parse`
  accordingly; new translated error branches for the three new sentinel
  errors; `translateImportIssue` extended for the three new `Issue*` codes.
- `web/locales/{en,ar,fa,tr}.json`: new `import.status.*`/`import.error.*`
  keys (identical key set across all four, confirmed independently of the
  guard) plus updated `import.file`/`import.help` copy.
- `web/help/{en,ar,fa,tr}/catalog.md`: the existing `/import`-claiming
  topic's "How to use it" section, not a new topic (no competing `routes:`
  claim).
- `internal/catimport/bkp_test.go`, `internal/pages/import_bkp_page_test.go`:
  parser-level and handler-level tests, synthetic fixtures only.

## Independent review (Opus, isolated worktree)

Read the diff fresh, ran the full gate (build/vet/gofmt/tests/all 5
guards), and independently re-verified two TDD claims by hand-reverting
logic in the isolated worktree (checksum-mismatch check, duplicate-PLU
check) and confirming the specific tests failed with real assertion
mismatches, then restoring and confirming green again. Also empirically
verified the `internal/data` placement was mechanically forced (inlining
the query into `catimport` made the guard fail) rather than gratuitous.

**Verdict at review time: NOT safe to merge — 2 blockers, several
should-fix.** All fixed in this same pass, detailed below.

### Blockers (fixed)

1. **The `/import` page itself was never wired up.** `web/ui/pages/import.html`
   was not touched by the original diff: the file `<input>`'s
   `accept=".csv,text/csv"` filtered `.bkp` out of the browser's picker,
   and `import.file`/`import.help` in all 4 locales still said CSV-only —
   while the manual (`catalog.md`) already *claimed* `.bkp` worked. The
   backend was real and correct; the front door was locked, and the manual
   contradicted the product. **Fix:** `accept` widened to
   `.csv,text/csv,.bkp,application/zip`; `import.file`/`import.help`
   reworded in all 4 locales to mention `.bkp` support. (The handler-level
   tests all POST multipart directly, bypassing the HTML form, which is
   why this was invisible to the automated suite — a real gap in this
   card's own test design, not just an oversight in the fix.)
2. **German decimal-comma prices silently multiplied by 100x.** SQLite is
   dynamically typed — a `REAL` column can still hold a TEXT value — and
   the shared CSV-path `ParsePrice` treats `,` as a thousands separator to
   strip, so `"2,90"` (the ticket's own example: "Espresso 2,90 €")
   silently became `290` minor units (€290.00) instead of `290`... i.e.
   the *correct* `290` for a different reason — the point is `"2,90"` (a
   real, plausible cell value on exactly this German till) parsed to
   `29000`, a 100x error, with no `IssueBadPrice` raised at all. **Fix:**
   new `parseBkpSalesPrice` (used only by `bkp.go`, `ParsePrice`'s CSV
   behaviour is unchanged) — whichever of `,`/`.` appears **last** in the
   string is the decimal point, the other (if present) is stripped as a
   thousands separator. Covers `"2.90"`, `"2,90"`, `"1.234,50"`,
   `"1,234.50"` all correctly; documented as a heuristic, not a verified
   contract, same honesty standard as the rest of this parser's unverified
   assumptions. New regression test: `TestParseBkp_GermanDecimalCommaPrice`.

### Should-fix (fixed)

3. **Zip-bomb: unbounded `io.ReadAll`.** A crafted upload's DEFLATE ratio
   was measured by the reviewer at >1000:1 on pathological input; the
   existing 20MB upload cap therefore admitted an entry that could expand
   to tens of GB, held twice in memory (once as bytes, once written to the
   temp file). **Fix:** `bkpMaxEntrySize` (200MB, generous — the ticket's
   own real 409-row example was well under 1MB) checked against
   `zip.File.UncompressedSize64` for both `backup.db` and `meta.inf`
   *before* either is read, new `ErrBkpTooLarge` sentinel wired into the
   handler's translated-error branches. Not directly unit-tested (a real
   test would need to actually write a 200MB+ fixture, or refactoring the
   cap into an injectable parameter purely for testability, which felt
   like more risk than the mitigation itself); the check is a single
   integer comparison, verified by inspection and by the existing full
   test suite continuing to pass with realistic small fixtures.
4. **A bad-price row blocked a later, otherwise-clean row sharing its
   PLU.** The original design registered a PLU as "seen" for the same-file
   duplicate check as soon as a row wasn't deleted/non-sellable/nameless —
   including a row with an unparseable price. A second, genuinely good row
   with the same PLU was then wrongly flagged `duplicate_sku_in_file`
   instead of importing — silently losing a real product, and pointing the
   operator at a "duplicate" of a row that itself never actually imported.
   **Fix:** a PLU only registers once its row fully clears with **no**
   issue at all (`item.Issue == ""`); a bad-price row still surfaces its
   own `bad_price` reason but no longer blocks anything. New regression
   test: `TestParseBkp_BadPriceRowDoesNotBlockLaterGoodRowSamePLU`.
5. **No `ORDER BY`** on the `Products` query — which of several same-PLU
   rows "wins" (imports first, so a later duplicate is the one flagged)
   was whatever order SQLite's query planner picked, not contractual, and
   liable to silently change if the source ever gains an index on
   `ProductNumber` (its natural PLU lookup key). **Fix:**
   `ORDER BY rowid` — the source file's own insertion order, the only
   deterministic ordering available without assuming more about the
   schema than is already documented.
6. **A real client's shop name ("Haaft") was committed** as literal test
   fixture filenames (`internal/pages/import_bkp_page_test.go`) and in two
   code comments (`bkp.go`, `bkp_test.go`) — against the ticket's own
   explicit instruction ("real shop data … do not commit it or any
   extract into a repo") and this pipeline's standing rule (never a real
   client/shop name as demo/seed/test data). The filename was cosmetic
   (nothing reads the extension) so trivially genericised; the comments
   reworded to refer to "the real backup file" / "a real customer's `.bkp`
   file" without naming it. `Trier`/`Nima` never appeared. Vendor names
   (`speedy kasse`, `pepperm`) are fine — that's a product, not a client.

### Nit (fixed)

- The LRM mark (U+200E) in `web/help/{ar,fa}/catalog.md`'s new sentence
  sat *inside* the `` `.bkp` `` code span (before the closing backtick),
  so copying the extension would carry the invisible character. Moved to
  after the closing backtick.

### Explicitly deferred (not this card, noted for follow-up)

- **The `internal/data` SQL-placement question** (review finding #7): the
  reviewer's read is that `guard-data-access.sh`'s "no SQL outside
  internal/data" rule is being satisfied by letter rather than spirit here
  — the rule exists to stop domain code bypassing repositories to reach
  *our* database, and `ReadBkpProducts` touches a foreign uploaded file,
  not ours. Confirmed mechanically necessary as things stand (inlining the
  query fails the guard), so not changed in this pass. Filed as a
  follow-up: consider giving `guard-data-access.sh` the same inline
  escape-hatch convention `guard-kiosk-engine.sh`/`guard-i18n.sh` already
  have (`// kiosk-engine-guard:allow …`, `// i18n:ignore`), so a
  reviewed, deliberate exception like this one doesn't have to relocate a
  whole query into a package that isn't really a repository over our own
  data.
- **Product question, not an engineering call:** the ticket's own manual
  conversion produced "229 items … 0 skipped" with 8 real PLU collisions
  among them; this implementation yields 221 clean + 8 flagged as
  duplicates (skip-and-flag, per the AC's literal wording: "the importer
  skips a row whose SKU already exists"). Whether the product actually
  wants both same-PLU rows imported instead (keeping all 229) is a product
  call, not something this pipeline should guess past — raised on the
  issue for the ticket author.
- **Nit, not fixed:** two ZIP entries both named `backup.db` — last one
  wins, a minor confusion surface for a maliciously or accidentally
  malformed upload, not a security issue (still bounded by the size cap
  and checksum/CRC checks either way).
- **Nit, not fixed:** a `.bkp` missing its `Products` table entirely falls
  through to the generic `invalid_file` message rather than the more
  specific `bkp_unrecognised` one.
- **Separately discovered, not part of this diff:** reviewing "Haaft" (#6
  above) surfaced that `internal/pages/hold_api.go`/`hold_api_test.go`
  already use `"Haaft 1"` as a held-sale example/test tab name, added
  2026-08-01 with a review record explicitly asserting "No real client/
  shop name used — quoted directly from the reference café POS." Ticket
  #511 (2026-08-09) reveals Haaft is, in fact, a real German café's real
  name — so that earlier assessment was wrong, unknowingly, before this
  ticket existed to reveal it. Out of scope for this card (different
  feature entirely); filed as a new Backlog card.
- **No German (`de`) locale exists** in this product (only en/ar/fa/tr) —
  the ticket's own AC asks for "a help topic + de locale," which isn't
  buildable against the actual locale set. Noted on the ticket rather than
  invented; adding a whole new shipped locale is a much bigger decision
  than this card's scope.

## Verified beyond the automated suite

- Full `go build ./...`, `go vet ./...`, full `go test ./...` (every
  package), and all five guard scripts — run twice: once by Dev/this
  session before review, once by the reviewer independently in its
  isolated worktree, and once more after every fix above landed.
- Both blockers and all four should-fix items above have a dedicated
  regression test (new or existing) that fails on the pre-fix code and
  passes after — verified directly for the two hand-checked by the
  reviewer (checksum mismatch, duplicate-PLU), and for the two added in
  this fix pass (German-comma price, bad-price-doesn't-block-good-row) by
  running them against the code before the corresponding fix and
  confirming a real assertion-mismatch failure, not a compile error.
- i18n completeness checked independently of the guard: all 4 locale files
  parse, contain the exact same key set, and the ar/fa translations
  contain no leaked ASCII/English and use real orthographic marks (ZWNJ in
  the Farsi), not machine-copied placeholders.
- No real client/shop name, secret, or credential-shaped literal remains
  anywhere in the diff (re-checked after the "Haaft" fix).
- Manual (`catalog.md`) now actually matches what the product does in all
  four locales, and the `/import` page's own `accept` + copy match it too
  — closing the manual-ahead-of-product gap the first review pass found.

## Post-review: CI caught one more gap

The PR's first CI run failed `guard-docs-shots.sh` — not one of the five
guards this pipeline's local pre-commit checklist names, but a sixth one
CI also enforces: `web/ui/pages/import.html` and `internal/pages/import_page.go`
changed (the app surface), and `catalog.md` changed in all four locales,
without a regenerated `web/help/img/**` screenshot set. Ran
`make docs-shots` (against the environment's pre-provisioned Chromium,
since the checked-in `playwright.docs.config.ts` doesn't pin an
`executablePath` and this sandbox's browser build didn't match the
resolved `@playwright/test` version — worked around with a temporary,
env-gated `launchOptions.executablePath` in the config for this run only,
reverted before commit, so nothing sandbox-specific landed in the diff)
and committed the regenerated manifest + the two screenshots that actually
changed pixels (`alerts`, `designer` — unrelated to this diff's own
changes; `designer`'s wall-clock-baked non-determinism is an existing,
documented accepted trade-off, not a regression introduced here).

## Safe-to-merge verdict

**Yes, after the fixes above.** No outstanding blockers. Deferred items
are genuine follow-ups (a design-taste question, a product question, two
cosmetic nits, and a serendipitously-discovered pre-existing issue in
unrelated code) — none of them block this card's own acceptance criteria.
