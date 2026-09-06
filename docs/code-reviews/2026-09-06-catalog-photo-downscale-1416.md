# Catalog photo pipeline: downscale accepted images, distinguish the too-large error

Card: ut-docs#1416. Found during independent review of ut-docs#1328
(the pixel-bomb hardening PR).

## What shipped

`internal/imaging`:
- New `DownscaleMaxEdge(img image.Image, maxEdge int) image.Image` and
  `MaxThumbEdge = 1600` — downscales an already-decoded image to a fixed
  longer-edge cap, preserving aspect ratio.
- New `ErrTooManyPixels` sentinel error, wrapped (`%w`) into the existing
  over-`MaxPixels` rejection so a caller can `errors.Is` it instead of
  matching the error string.

`internal/pages/catalog/handlers.go` (item-image and variant-image upload
handlers, `saveLookupImage`) and `internal/pages/import_page.go` (the
`.bkp` commit-time image write): all four call sites now downscale to
`imaging.MaxThumbEdge` before writing `thumb.png` — previously an
accepted-but-large photo (up to the full ~2828×2121 decode cap) was
written and served at native resolution everywhere the thumbnail is used
(admin Catalog table, POS sale-screen grid/basket, self-order kiosk,
search suggestions).

The two live upload handlers (item/image, variant/image) now branch on
`errors.Is(err, imaging.ErrTooManyPixels)` to return a distinct,
localized `catalog.error.image_too_large` message instead of the same
generic text a corrupt/unsupported file gets. `internal/pages/ai_api.go`'s
`loadRefJPEG` was refactored to call the new shared helper instead of its
own inline copy of the same resize math.

New locale keys `catalog.error.image_too_large` and
`catalog.error.image_invalid` in all four `web/locales/*.json`
(en/ar/fa/tr). `web/help/{en,ar,fa,tr}/catalog.md`'s Item image bullet
gained a sentence on the size limit and automatic resizing (ut-docs#324,
manual ships with the feature). `web/help/img/manifest.json` and two
incidentally-changed screenshots were regenerated via `make docs-shots`.

## Independent review (Opus, complexity:medium)

Full read of the diff, build/vet/test/lint/6 CI guards run for real,
revert-then-restore TDD verification on the two most load-bearing new
tests, and a brute-force check of the downscale math across ~4M
width/height pairs. Findings, triaged:

1. **F1 (medium, fixed)** — the help text's example ("switch your camera
   to its normal mode") was actually wrong for the common case: a phone's
   *default* camera mode (typically 12MP) already exceeds the 6-megapixel
   cap, so that advice sends an operator down a dead end. Fixed: the
   manual now states the real ~6-megapixel/2800×2100 threshold instead of
   a device-setting claim that doesn't hold.
2. **F2 (fixed)** — the sibling generic-rejection `http.Error` calls
   (read-failure and non-`ErrTooManyPixels` decode-failure, at all 4
   sites this diff touches in `handlers.go`) were left as raw English
   literals two lines from the new localized branch. `guard-i18n.sh`
   structurally cannot catch this (it exempts `http.Error` bodies by
   design — see `internal/pages/common/errors.go`'s own doc comment).
   Fixed: both now route through `common.LocalizedError` with a new
   `catalog.error.image_invalid` key, translated in all four locales.
3. **F3 (fixed)** — `DownscaleMaxEdge`'s doc comment promised the longer
   edge equals `maxEdge` exactly; computing both destination dimensions
   from the same float64 scale factor truncated the longer edge to
   `maxEdge-1` for a measured ~7.5% of real input dimensions (e.g.
   1601×2156 → 1599). Fixed: the longer edge is now set to `maxEdge`
   directly, with only the shorter edge derived (via `math.Round`, not
   truncation). Pinned with `TestDownscaleMaxEdge_LongerEdgeExactlyMaxEdge`.
4. **F4 (deferred, follow-up card)** — the `.bkp` import path reports an
   over-cap photo as "could not be read", which is a wrong diagnosis for
   a perfectly decodable 12MP photo. Reviewer agreed this is a defensible
   scope call for this card (the import path is best-effort/background,
   not the interactive upload flow ut-docs#1416's Problem 2 describes),
   but flagged it as a natural 3-line follow-up now that the sentinel
   error exists. Tracked as a new Backlog card rather than folded in here.

All four locale files verified independently to have zero key drift
(1962 keys each) and genuine, non-English-leftover translations for both
new keys. Security: downscale runs strictly after `imaging.Decode`
(so after the format allowlist and pixel-count guard) — no new
untrusted-dimension allocation path. `os.MkdirAll` + `paths.Data(...)`
confirmed present and unchanged at all 4 write sites (the two recurring
bug classes this pipeline watches for).

## Verified beyond automated tests

- Revert-then-restore: reverting just the production hunk (the
  `errors.Is`/downscale lines) in the item-image handler made
  `TestItemImageUpload_DownscalesLargeAcceptedImage` and
  `TestItemImageUpload_OversizedImageGetsDistinctError` fail with the
  exact missing-behavior assertions (native 2800px written; generic
  message served), not a compile error or unrelated panic; restoring the
  fix made the full package pass again.
- Brute-force verification of the pre-fix truncation bug and the
  post-fix exact-longer-edge guarantee across width/height 1..3000
  bounded by `imaging.MaxPixels`.
- Measured real effect on a photo-like 2800×2100 upload: served
  `thumb.png` dropped from 12.7MB to 2.5MB (19.7% of before).
- Manually re-read the corrected help-doc prose against
  `internal/imaging.MaxPixels`'s actual value to confirm the new figure
  is factually accurate, not just plausible-sounding.

## CI guards / gate

`gofmt -l .` (clean), `go build ./...`, `go vet ./...`, `go test ./...`
(all green), `golangci-lint run ./...` (0 issues), and
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-page-http-error.sh`,
`guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-help-topics.sh`, `guard-makefile-version.sh` all pass.

## Safe-to-merge verdict

Yes. All three review findings addressed; F4 tracked separately.
