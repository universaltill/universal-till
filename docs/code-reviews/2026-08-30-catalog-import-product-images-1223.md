# Code review: catalog import brings product images across from `.bkp` backups (ut-docs#1223)

**Date:** 2026-08-30
**Branch:** `feat/1223-catalog-import-product-images`
**Author:** Farshid Mirza (autonomous pipeline cycle, lane `cloud-54`)
**Reviewer:** independent Opus subagent, isolated worktree

## What shipped

A `.bkp` (speedy kasse / pepperm cashbox) backup's `Products` table can
carry a `ProductImagePath` column referencing a photo stored inside the
same archive's `documents.zip` member. Previously nothing read that
column at all — every imported item fell back to the ut-docs#1189
placeholder category icon, even when the source genuinely had a photo.

- `internal/data/bkp_products_repo.go`: `ReadBkpProducts` now reads
  `ProductImagePath` (optional column, same backward-compatible
  introspection as the existing tax/category columns — an older
  `backup.db` without it just reads blank, no error).
- `internal/catimport/bkp.go`: `ParseBkp` resolves a non-empty
  `ProductImagePath` against `documents.zip`'s own members — exact
  normalized-path match, falling back to a basename match, both
  case-folded. `documents.zip` is extracted to a bounded temp file the
  same zip-bomb-safe, authoritatively-capped way `backup.db` itself is
  streamed (`bkpMaxDocsZipSize`), and each resolved image is capped
  individually (`bkpMaxImageSize`, matching the manual upload handler's
  own 10MB decode cap) *and* in aggregate across the whole file
  (`bkpMaxTotalImageSize`) — the aggregate cap is a fix from this
  review's own finding, see below. A dangling reference (no
  `documents.zip`, no matching member, an over-cap entry) sets a
  non-blocking `ImageIssue`/`ImageIssueRaw`, mirroring the existing
  `BarcodeIssue`/`TaxIssue`/`SKUIssue` pattern — the row still imports.
- `internal/catimport/catimport.go`: new `ImportItem.ImageData`/
  `ImageIssue`/`ImageIssueRaw` fields, `ImageIssueUnresolved`/
  `ImageIssueTooLarge` reason codes.
- `internal/pages/import_page.go`: at commit time, a resolved image is
  decoded/re-encoded and written to disk exactly the way a manual
  item-photo upload is (`POST /api/catalog/item/image`), then recorded
  via `CatalogRepo.SetItemThumbnail` — the unconditional-overwrite
  sibling of `EnsureDefaultThumbnail`, appropriate here because this
  genuinely is "a real photo." Falls back to the existing placeholder
  path whenever there's no image, it fails to decode, or it fails to
  save (each surfaced as a distinct, translated per-row warning — never
  silently dropped, same ut-docs#293 defect class the codebase already
  guards elsewhere in this file).
- `web/locales/{en,ar,fa,tr}.json`: three new keys —
  `import.status.image_unresolved`, `image_too_large`,
  `image_undecodable`, plus `image_save_failed` added after review.
- `web/help/en/catalog.md`: one new bullet documenting the behavior.
- `../ut-docs/architecture/catalog-import.md`: architecture doc updated
  in the same session (separate repo).
- `web/help/img/**`: `make docs-shots` re-run (CI-blocking
  `guard-docs-shots.sh` — see review finding 1 below).

**Explicitly out of scope for this increment** (documented in
`ImportItem.ImageData`'s own doc comment and the commit message, not a
defect): the CSV import path carries no image column yet. The ticket's
own wording was "consider," not an acceptance criterion.

## Independent review

Spawned as an `Agent` (Opus, `isolation: "worktree"`, per this card's
`complexity:medium` label — Model routing by complexity, `scrum-master`
skill). Given the exact diff scope, the relevant `CLAUDE.md` rules
(repository pattern, i18n), and explicit instructions to run
build/vet/tests/guards itself and check for this pipeline's two
recurring bug classes (missing `os.MkdirAll`, a cwd-relative path
instead of `paths.Data(...)`).

**Verdict: safe to merge, with fixes.** One CI-blocking finding, six
non-blocking. All fixed in this branch except one, which is deferred
with a documented reason (see below).

### Findings and outcomes

1. **BLOCKING — `guard-docs-shots.sh` would fail on push.**
   `web/help/en/catalog.md` changed but its screenshot manifest entry
   wasn't refreshed. **Fixed**: ran `make docs-shots` (all 92
   topic×locale screenshots, 1.7 min), re-verified the guard passes.
   The other 91 PNGs (every topic besides `catalog`) also changed, but
   only from this session's reused-Chromium build
   (`headless_shell-1194`, v141) differing from the pin (v149) —
   `docs-shots.sh`'s own documented, explicitly non-fatal warning
   (ut-docs#622) — confirmed by `manifest.json`'s actual diff: exactly
   one line changed (`topics.catalog.en`'s hash), `surface_sha256`
   itself unchanged, meaning no *source* surface the guard tracks
   actually moved besides the topic markdown. Pixel/rasterization
   drift, not a content change.

2. **Aggregate image-memory budget** — `bkpMaxImageSize` bounds each
   row individually, but every resolved row's `ImageData` is retained
   live on `Result.Items` simultaneously, and `ParseBkp` runs on both
   the preview and the commit request (no separate "don't resolve
   images" mode). A source with hundreds of near-cap-sized photos could
   hold ~1GB+ resident on the low-memory Android/Pi hardware this
   importer targets. **Fixed**: added `bkpMaxTotalImageSize` (200MB), a
   running total checked after each resolution — once exceeded, that
   and every later row falls back to `ImageIssueTooLarge` the same way
   an individually oversized entry does. New regression test
   `TestParseBkp_AggregateImageBudgetCapsLaterRows`, TDD-reverified by
   this reviewer (temporarily disabling the check, confirming a
   meaningful failure, restoring).

3. **Partial `thumb.png` left on disk on an encode/close failure.**
   `self_order_shop.go`'s `ImageURL` resolves `thumb.png` by path
   *convention*, not via `item_images` — a truncated file there would
   render broken on the self-order kiosk even though `item_images`
   correctly still points at the placeholder. **Fixed**: the write path
   now removes the partial file on either failure before falling back.

4. **Write failures after a successful decode only logged, no operator
   signal.** A resolvable photo that failed to save (mkdir/create/
   encode/close/`SetItemThumbnail`) silently became the placeholder
   icon with nothing in the commit summary — same "silently dropped"
   class ut-docs#293 exists to prevent. **Fixed**: added
   `import.status.image_save_failed`, surfaced as a per-row warning on
   any of those failure paths (translated in all 4 locales).

5. **Stale doc comment** (`buildBkpProductsQuery`'s "always yields the
   same eight result columns" — it's nine since `ProductImagePath`).
   **Fixed.**

6. **No case-folding in `normalizeBkpArchivePath`.** `IMAGES/Foo.JPG`
   vs `images/foo.jpg` degraded to an unresolved warning. **Fixed**:
   folded into the same normalization, alongside the existing separator/
   leading-slash handling and basename fallback.

7. **Test comment overstated what it verifies**
   (`TestParseBkp_OversizedImageEntryRejected` claimed to prove bytes
   are "never read fully into memory," but only exercises the
   declared-size gate). **Fixed**: reworded to describe what the test
   actually covers, with a pointer to the adversarial read below for
   the stronger claim.

### Adversarial reads verified clean (recorded so they aren't
re-litigated)

- **Zip-bomb via a lying declared size**: not exploitable —
  `archive/zip` itself returns `ErrFormat` once bytes read exceed the
  declared `UncompressedSize64`, hard-bounding the read regardless of
  what the header claims (same reasoning `backup.db`'s own streaming
  already documents).
- **`openBkpDocsIndex` resource handling**: all error paths close the
  fd and remove the temp file; no leak on any path. Success path closes
  via `defer docsIndex.close()`.
- **Nil safety**: `close()` has a nil-receiver guard,
  `resolveBkpImage` handles a nil index, `openBkpDocsIndex` returns nil
  on every error path.
- **Nested-zip approach is correct**: `documents.zip` is decompressed to
  a bounded temp file before `zip.NewReader` — no attempt at random
  access into DEFLATE-compressed bytes, mirroring `backup.db`'s own
  handling.
- **No zip-slip**: archive member names are only ever used as map keys;
  the on-disk write path is built from `itemID`, never a
  source-controlled name.
- **Both of this pipeline's recurring bug classes absent**:
  `os.MkdirAll` precedes `os.Create`; `paths.Data(...)` used
  throughout, no cwd-relative path.
- **Layering respected**: `catimport` still writes nothing to the data
  dir or the application DB — its only disk use is its own temp-file
  zip extraction, an existing pattern. All app-state writes stay in the
  pages layer.
- **No real customer data**: the commit adds zero binary fixtures; every
  test archive is synthesized in Go.

### Deferred (not fixed, documented reason)

`image.Decode` on a source image can allocate far more than
`bkpMaxImageSize`'s 10MB bound for a high-dimension image (a classic
"pixel bomb"), and there's no downscale before re-encoding to PNG. Not
fixed here: this is byte-for-byte the *existing* manual item-photo
upload handler's own behavior
(`internal/pages/catalog/handlers.go`'s `POST /api/catalog/item/image`)
— this PR's commit message's "exactly the way a manual upload does" is
accurate to that shared risk, not a new one this diff introduces.
Hardening belongs to both paths together, tracked as a new Backlog
card rather than scope-creeping this one.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean, repo-wide.
- `go test ./...` — full suite green (no failures anywhere), not just
  the touched packages.
- All 16 CI-blocking guards from `.github/workflows/ci.yml`'s `build`
  job re-run directly and passing: `guard-data-access`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`,
  `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `check-brand-assets`, `guard-makefile-version`.
- TDD claims independently re-verified (this reviewer, in an isolated
  worktree — never the shared orchestrator checkout, ut-docs#386):
  - `TestImport_BkpRealImageWrittenAndRecorded` — disabling the
    real-photo write path fails with exactly the pre-fix ut-docs#1189
    behavior (`recorded thumbnail path = ".../coffee.svg", want
    ".../thumb.png"`), restoring returns it to green.
  - `TestParseBkp_OversizedImageEntryRejected` — removing the
    per-entry size gate fails (`ImageData should be empty ..., got 64
    bytes`), restoring returns it to green.
  - `TestReadBkpProducts_ProductImagePathPresent` — forcing the column
    fallback to a literal `NULL` fails, restoring returns it to green.
  - `TestParseBkp_AggregateImageBudgetCapsLaterRows` (added in
    response to finding 2) — disabling the aggregate check fails
    meaningfully, restoring returns it to green.
- i18n: all 4 locale files carry matching keys at the same position,
  `%s` preserved where needed; ar/fa/tr are genuine translations, not
  English left in place.
- No real client/shop name, no secret-shaped literal anywhere in the
  diff.

## Safe-to-merge verdict

**Yes.** All blocking and non-blocking findings from the independent
review are fixed except one deliberately deferred item (shared,
pre-existing risk with the manual upload path, not introduced by this
change), documented above with its own reasoning.
