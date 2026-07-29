# Test coverage batch 23: AI camera-identify — two real production bugs found and fixed

2026-07-29

`internal/pages/ai_api.go` — the camera-identify feature ("point your
camera at an item, AI suggests a catalog match"): `POST
/api/pos/identify` and `POST /api/pos/identify/confirm`. Had partial
pre-existing coverage (`TestLoadRefJPEGDownscales`,
`TestPruneAIRefsKeepsNewest`); everything else was untested. While
building that coverage, two real bugs were found and fixed — not
reported by a user, found by re-reading the code carefully while
writing tests.

## Bug 1: cwd-relative item asset path (same class as batch 11)

`itemAssetDir = "web/public/assets/items"` was a plain constant
relative to the process's current working directory. Item images are
actually uploaded to `paths.Data("public", "assets", "items", ...)` —
the stable per-user/per-machine data directory that survives version
upgrades (see `internal/paths/paths.go`). This is the exact same bug
class already found and fixed in `internal/pages/sync_assets.go`
(batch 11, `docs/code-reviews/2026-07-29-coverage-batch-11-sync-assets-regression.md`).

**Impact if unfixed**: three call sites were affected —
`loadReferenceImages`/`loadRefJPEG` (reference photos sent to the AI
model for recognition context — would always run with zero visual
references in production, silently degrading match quality to
text-only); the `ThumbURL` existence check on identify results
(thumbnails would never show); and `POST /api/pos/identify/confirm`
(cashier-confirmed reference photos would be saved to a cwd-relative
folder never read back by anything, silently discarding every
confirmation).

**Fix**: `const itemAssetDir = "..."` → `func itemAssetDir() string {
return paths.Data("public", "assets", "items") }`, all 4 non-declaration
call sites updated.

**TDD**: `TestLoadReferenceImages_FindsCatalogThumbInTheStableDataDir`
was written first and confirmed to fail against the pre-fix code
("expected 1 reference image found in the stable data dir, got 0"),
then the fix applied, then confirmed passing.

## Bug 2: wrong URL prefix on the identify-results thumbnail (found by review)

Even after fixing the filesystem path, the emitted `ThumbURL` was
`/assets/items/<id>/thumb.png` — but the static file server is only
registered at `/public/` (`static_page.go`), matching every other
consumer in the codebase (`self_order_shop.go`,
`catalog_table.html`, both use `/public/assets/items/...`). The
identify-results thumbnail would still 404 in the browser even with
bug 1 fixed. The original test had enshrined this wrong URL as its own
assertion (a coincidental pass, not a real check). Fixed the handler
to emit `/public/assets/items/...` and corrected the test assertion to
match.

## Independent review (opus) — caught bug 2, verified bug 1's fix and TDD discipline

The review re-checked the entire file for any other cwd-relative paths
(none found), confirmed no other file in the codebase referenced the
removed `itemAssetDir` constant, confirmed `paths.DataDir()`'s
`atomic.Value` makes the const→func conversion concurrency-safe, and
independently re-derived that the regression test's pre-fix failure
message is consistent with the described bug (no `chdirRoot` call in
that specific test, so the buggy code really did resolve to a
nonexistent directory relative to `internal/pages`).

It also flagged that the original success test's single seeded AI
match wouldn't have caught a broken hallucination filter (the layer
that drops model-suggested item IDs outside the requested catalog,
already covered independently in `internal/ai`) — strengthened by
adding a fabricated non-catalog match alongside the real one, so the
"exactly one match returned" assertion now actually depends on that
filter working, not just coincidentally matching a single-item input.

## Other new coverage

- `TestLoadReferenceImages_MissingImagesAreSkippedNotErrored`
- `POST /api/pos/identify`: not-configured (404), missing/invalid
  photo (400), and the full success round trip described above through
  a `fakeIdentifyServer` (modeled on `internal/ai/ai_test.go`'s own
  Ollama test pattern, injected via the `Deps.AI` test seam).
- `POST /api/pos/identify/confirm`: not-configured (404),
  path-traversal-shaped `item_id` (400), unknown item (404), and a
  real success case that writes an actual reference image file under
  the (now-fixed) stable data dir and an `ai_identify_confirmed` audit
  row.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
