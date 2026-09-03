# 2026-09-03: Import progress feedback + race-safe commit (ut-docs#1510)

**Card:** ut-docs#1510 — "Import gives no progress feedback, and the
double-tap it invites can duplicate items (read-then-write dedupe, no
single-use `staged_id`)"

**Reported by:** the product owner, live on a real pilot import (217
items / 22 categories).

## What shipped

Two real defects, one UX and one data-integrity:

1. **No progress feedback.** `web/ui/pages/import.html`'s Preview/Import
   form carried no `hx-indicator`/`hx-disabled-elt` — pressing either
   button left the screen looking inert on a large file, inviting a
   second press.
2. **A double-tap was not safe.** The commit path deduped with a
   read-then-write check (`BarcodeExists`/`SKUExists` then `Insert`),
   which is not atomic. A row with **neither** barcode nor SKU had no
   dedupe at all — `items.sku` is a nullable `UNIQUE` column, and SQLite
   treats every `NULL` as distinct, so two concurrent inserts of a
   barcode-less/SKU-less row both succeed.

### The fix

- **Client-side (`web/ui/pages/import.html`):** `hx-indicator="#import-busy"`
  + `hx-disabled-elt="find button[type=submit]"` on both import forms
  (staged and direct), plus a touch-legible `#import-busy` status line
  (not a small spinner — explicit review requirement).
- **Server-side, closes the gap `takeStagedCatalogUpload`'s existing
  single-use exclusivity doesn't cover** (a DIRECT commit — "Import"
  pressed without ever previewing, so there's no `staged_id` for two
  requests to race over): `internal/pages/import_stage.go` adds
  `reserveImportCommit`/`releaseImportCommit`/`hashImportUpload` — an
  in-process, mutex-protected registry keyed by a SHA-256 hash of the
  uploaded bytes. Any commit request reserves its content hash before
  touching the DB; a second concurrent commit of byte-identical content
  is rejected with a translated 409 instead of proceeding. Wired into
  `import_page.go`'s commit handler right after the file is resolved,
  covering both the staged and direct paths, and the setup wizard's
  in-process `commitStagedImportForSetup` replay.
- **DB-layer:** `internal/data/catalog_repo.go`'s `CreateItemTx` now
  translates a `UNIQUE(sku)` violation into the distinguishable
  `data.ErrSKUExists`, mirroring what `CreateItem` (its non-tx sibling)
  already did — previously it just wrapped the raw driver error.
  `import_page.go`'s commit loop now marks that row Skipped with the
  existing "SKU already in catalog" status instead of a generic "item
  could not be created" failure — the same clean skip a sequential
  re-import already gets.
- **i18n:** two new keys (`import.busy`, `import.error.already_in_progress`)
  added to `en`/`ar`/`fa`/`tr`.
- **Manual:** `web/help/en/catalog.md` updated in the same branch with a
  new bullet describing the busy state and double-tap safety.

### Tests added

- `internal/data/catalog_repo_createitemtx_sku_conflict_test.go`:
  `TestCreateItemTx_DuplicateSKUIsErrSKUExists` (unit) and
  `TestCreateItemTxConcurrentSKURace` (real, file-backed-DB, two-goroutine
  race over 15 rounds — exactly one item lands, the loser gets
  `ErrSKUExists`, never a raw driver error).
- `internal/pages/import_commit_lock_test.go`: concurrent identical
  direct commits (one 200, one 409, exactly one item row);
  a sequential same-file re-import still works once the lock releases;
  and a genuine SKU race across two *different* files (which the
  content-hash lock does not block on its own) confirming the row-level
  `ErrSKUExists` handling kicks in and exactly one item lands.

## Independent review

Opus, fresh context, isolated `git worktree` (no shared-checkout risk per
ut-docs#386), diffed against `main`'s merge-base.

**Verdict: MERGE WITH FIXES.** No blocking issues — the two ticket
defects are genuinely fixed and the change is data-safe. The reviewer
actively tried to break it:

- Ran the full gate (`go build`, `go vet`, `gofmt -l`, the relevant
  guards — `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-data-access.sh`, `guard-help-topics.sh`) — all clean.
- Ran the new tests with `-race` and `-count=5` — non-flaky.
- **TDD re-verification** (temporarily reverted each fix locally, re-ran
  the test, confirmed it failed with the claimed error, restored):
  - Reverting the `ErrSKUExists` translation in `CreateItemTx` →
    `TestCreateItemTxConcurrentSKURace` fails with the raw
    `UNIQUE constraint failed: items.sku` error (also confirming
    `isUniqueViolation`'s string match is exactly right).
  - Reverting the reserve gate in `import_page.go` →
    `TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond` fails
    (`codes [200 200]` — both commits succeed, duplicate item).
- Traced every exit path of the commit handler to confirm
  `defer releaseImportCommit(hash)` always runs (currency-confirm
  detour, staged preserve/restage, parse failures) and that reservation
  happens before any DB write.
- Read the vendored htmx source to confirm `hx-indicator`'s plain
  selector resolves document-wide, while `hx-disabled-elt="find …"`
  resolves relative to the form (`form.querySelector`) — which is what
  surfaced the one real finding below.

### Finding fixed

**The server-rendered "repeated Import button" (after a long preview,
`import_page.go`, `form="import-form"`, living outside `<form
id="import-form">` in the swapped `#import-result` div) showed the busy
indicator but was never actually disabled** — `hx-disabled-elt="find
button[type=submit]"` on the outer form only reaches descendants of that
form, and this button isn't one. Fixed by giving that button its own
`hx-disabled-elt="this"`. The template comment that had asserted it "gets
the same treatment for free" was also corrected to state the real
(split) behavior, verified against the htmx source rather than assumed.

### Accepted, not fixed (in scope, documented)

- **Sequential re-import residual, out of this ticket's scope.** The
  content-hash lock only blocks *concurrent* duplicate commits, by
  design — a *sequential* re-import of a file whose rows have neither
  SKU nor barcode still duplicates (no natural key exists for the DB to
  dedupe on, and the hash releases once the first request finishes).
  This ticket is specifically about the double-tap (concurrent) case;
  the button-disable now covers the in-flight window. Pre-existing
  behavior, not introduced by this change — not filed as a new card, as
  it's the same known gap the ticket's own body already named.
- Manual wording note (fixed): "the button shows 'Importing…'" was
  imprecise (it's a separate status line, not the button label) and the
  staged double-tap path shows a different message ("preview expired")
  than the direct path's "already running" — both corrected in the same
  pass.

## Verified beyond automated tests

- Full repo gate: `go build ./...`, `go test ./...` (all packages,
  ~1m48s), `gofmt -l` clean, `go vet ./...` clean.
- Guards: `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-htmx-loaded.sh`, `guard-page-http-error.sh`,
  `guard-emoji-font.sh` — all pass.
- `internal/pages` and `internal/data` import-related tests re-run with
  `-race` after the post-review fixes — clean.

## Safe to merge

Yes. `merge_method: "merge"` per the standing rule (ut-docs#250) — never
squash/rebase.
