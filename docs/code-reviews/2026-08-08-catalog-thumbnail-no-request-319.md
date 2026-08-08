# Catalog thumbnails: no request for a missing photo (ut-docs#319)

## What shipped

`/catalog` rendered every row (and, it turned out, the item-detail panel
alongside it) with an unconditional `<img src=".../thumb.png">`. For an
item with no uploaded photo, the browser still issued a real GET that
404'd — hidden visually via `onerror`, but a real, logged, wasted request
on every page view. The seeded demo catalog ships a real `thumb.png` for
every demo item, which is exactly why this stayed invisible until an e2e
spec (#303) left photo-less items behind on the shared till server.

Fix, in `internal/httpx/httpx.go`:

- New template func `imgExists(url string) bool`, checking the same three
  tiers `internal/pages/static_page.go`'s `fallbackFS` actually serves
  `/public/` from, in order: the stable per-user data dir (`internal/paths`),
  the CWD-relative `web/` release tree, then the binary's embedded default
  assets (`github.com/universaltill/universal-till/web`'s `FS embed.FS`).
- The existing `assetVersion`/cache-busting helper is refactored to share a
  `statAsset` helper with the new code, but **deliberately does not** gain
  the embed tier itself — cache-busting only needs a value that changes when
  a file changes, and an embedded default can't change until the next
  build/boot anyway, so falling back to `bootTime` there is still correct.
  Existence is a different question and needs the embed tier, or a
  bundled default asset gets reported as missing whenever the process isn't
  running from the repo/install root — which real packaged installs
  routinely aren't (that CWD-independence is `static_page.go`'s whole reason
  for embedding in the first place).

Template changes:

- `web/ui/partials/catalog_table.html`: the thumbnail cell renders `<img>`
  only when `imgExists` is true; otherwise a `<div class="thumb small"
  aria-hidden="true">` placeholder, reusing `.thumb`'s existing themed
  `background: var(--surface-2)` box — no new CSS needed there.
- `web/ui/partials/catalog_variants.html`: the item-detail panel (opened by
  clicking a catalog row) has the identical pattern for the item thumb, plus
  a two-level fallback (variant thumb → item thumb → hidden) for each
  variant row. Both are now resolved server-side via `imgExists` instead of
  relying on the browser's `onerror` chain to hide the failure after the
  request already happened. Added during review (see below) — the original
  diff only covered the table.
- `web/public/app.css`: one-line selector extension (`.vg-img img` →
  `.vg-img img, .vg-img .thumb-ph`) so the new variant-row placeholder gets
  the same sizing as the `<img>` it replaces.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → same-model, fresh-instance
review per the `reviewer` skill), worktree-isolated. Actually built, vetted,
ran the full test suite, ran all four standing guards, and — going beyond a
trivial compile-error check — independently re-verified the TDD claim by
stripping *just* the embed-tier fallback (keeping `imgExists` compiling) and
confirming `TestImgExistsTrueForEmbeddedDefaultWhenDiskAndStableMiss` then
fails on its own, proving that test is a real behavioral guard. Also ran the
new e2e spec for real against a live till server (a genuine "process not
running from the repo/install root" scenario, since `run-till.sh` execs the
binary from a temp data dir) and traced `imgExists`'s three-tier path
resolution end-to-end against `static_page.go`'s actual serving order to
confirm no path-shape mismatch.

**Verdict: safe to merge as-is.** No blocking correctness, build, test, or
guard failures.

Findings and how each was handled:

- **MINOR (accepted, fixed before commit)** — `catalog_variants.html` had
  the identical unconditional-`<img>` pattern, on the same `/catalog` page,
  left uncovered by the original diff. The ticket's own acceptance
  criterion ("`/catalog` must issue zero network requests for a thumbnail
  when an item has no uploaded photo") is part of that same page, so this
  was folded into the same change rather than deferred — see "Template
  changes" above. Re-ran the full gate + a new e2e assertion (clicking the
  photo-less row and checking the resulting panel) after the fix; still
  green.
- **MINOR (accepted, not fixed)** — `catalog_table.html` now calls
  `statAsset` twice for an item that *does* have a photo (once via
  `imgExists`, once via `imgv`→`assetVersion`). Cheap (a couple of extra
  `os.Stat` calls per row, not per request), and not worth the API
  reshaping to avoid — noted here rather than actioned.
- **Procedural** — flagged that no review record existed yet at review
  time; this file is that record.
- **Confirmed non-issues**, checked and ruled out by the reviewer: no
  path-traversal exposure (item/variant IDs are server-generated
  `uuid.NewString()`, never client-supplied, so `rel` can't carry `../`
  from an attacker); no leftover diff from the reviewer's own revert/
  restore experiments.

## Verified beyond automated tests

- New Go unit tests in `internal/httpx/asset_version_test.go`: `imgExists`
  true when present in the stable dir, false when missing everywhere, true
  for an embedded-only default when disk *and* stable both miss (the
  behavioral guard above), and false for a URL with no leading slash.
- New e2e spec `e2e/tests/catalog-thumbnail-no-request.spec.ts`, driven
  against a real till server + real Chromium: creates a real photo-less
  item via `POST /api/catalog/item`, watches every page network request,
  and asserts zero requests reference that item's thumbnail — for both the
  catalog table row and, after clicking it open, the item-detail panel.
  Also asserts an existing seeded item (`Sparkling Water 500ml`, itm003)
  is unaffected: still a real `<img>` at the correct `src`, still loads
  once scrolled into view (rows use `loading="lazy"`).
- `go build ./...`, `go vet ./...`, full `go test ./...`, and all four
  standing guards (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`) all green.
- Manually confirmed a pre-existing, unrelated e2e failure
  (`catalog-image-to-till.spec.ts`, a `loading="lazy"`/headless-viewport
  timing issue on the same seeded item, in this sandbox's Chromium build)
  reproduces identically on the pre-change baseline — not a regression
  introduced here.

## Deferred / non-goals

- No user-manual update: the previous behavior for a photo-less item was
  already `visibility:hidden` (a shop owner saw blank space, not a broken-
  image icon); the new placeholder is a themed box in the same spot — a
  cosmetic change, not a new action or a changed workflow, so
  `web/help/en/catalog.md` (which doesn't document per-row thumbnail
  rendering today) isn't updated for it.
- Money/i18n/offline-first/repository-pattern: not applicable — no SQL, no
  money, no new user-facing strings (the placeholder has no text).

## Safe-to-merge verdict

Yes. Independent review passed with only minor, already-addressed or
deliberately-accepted findings; the full gate is green; the fix is
regression-tested at both the unit and real-browser layers.
