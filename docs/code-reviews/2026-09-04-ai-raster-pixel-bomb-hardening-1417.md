# Code review: AI-identify / receipt-logo pixel-bomb hardening (ut-docs#1417)

## What shipped

`internal/imaging`'s pixel-bomb guard (ut-docs#1328) covered the catalog
image-upload/import call sites but not two more: `internal/pages/ai_api.go`
(the camera-identify photo upload and its persisted `ai_ref` reference
images) and `internal/print/raster.go`'s `RasterLogo` (the receipt-logo
upload/print path). Both called raw `image.Decode`/`image.DecodeConfig`
with no bound on the decoded pixel count — the same defect class #1328
fixed, at call sites #1328 didn't reach.

**The ticket also undersold `RasterLogo`'s real risk**: it claimed the
function "has no production caller" and suggested a comment would do. That
was wrong — `internal/pages/receipt_designer.go`'s logo-upload handler
validates an untrusted upload directly through `RasterLogo`, then persists
the (previously unvalidated-for-dimensions) bytes verbatim, which
`internal/pages/print_api.go`'s `receiptLogoRaster` then re-decodes via
`RasterLogo` on **every subsequent receipt print** — the identical
"hostile file persisted once, re-decoded repeatedly" shape the ticket
flagged as the worse case for `ai_ref`. Brought fully into scope rather
than left as a comment.

**Fix** (`internal/imaging/decode.go`):

- Added `DecodeBoundedFormats(raw []byte, maxPixels int64, formats
  map[string]bool) (image.Image, string, error)` — the existing
  `DecodeBounded` mechanism (DecodeConfig-first dimension check, then the
  full decode, then a second format recheck) generalized over a
  caller-supplied format set instead of a hardcoded one. `Decode`/
  `DecodeBounded` become thin wrappers with their existing 2-return
  signature and png/jpeg-only behavior unchanged — the 3 existing catalog
  call sites needed zero changes.
- `internal/print/raster.go`'s `RasterLogo` now decodes via
  `imaging.DecodeBoundedFormats(imgBytes, imaging.MaxPixels,
  rasterFormats)` where `rasterFormats = {png, jpeg, gif}` — GIF kept
  deliberately: `web/ui/pages/receipt_designer.html`'s logo upload
  advertises `accept="image/png,image/jpeg,image/gif"`, a real,
  user-facing supported format. Signature unchanged (`[]byte) []byte`).
- `internal/pages/ai_api.go`: `readPhoto` now decodes via the bounded
  helper (png/jpeg only, matching its existing error text); the
  `/api/pos/identify/confirm` handler re-encodes the already-decoded
  photo to a fresh `.png` file instead of persisting the raw upload bytes
  verbatim (same convention as the catalog handlers' `thumb.png`);
  `loadRefJPEG` — which re-decodes stored `ai_ref`/`thumb.png` files on
  **every** identify call, for up to `maxReferenceImages` (60) items —
  now also decodes via the bounded helper, which is what protects any
  hostile file already on disk from before this fix ships (the
  confirm-handler re-encode only protects files written from now on).

## Review

Independent review by a different model (Opus, `general-purpose` subagent,
`isolation: "worktree"`, fresh context that never saw the dev reasoning).

**Verdict: no blockers. Four should-fix items, all fixed in this pass.
Four follow-ups filed for a later card rather than folded into this one's
scope.**

### Should-fix — fixed: comment overclaimed a security property

`ai_api.go`'s confirm-handler comment said re-encoding at write time
"closes the gap... since every stored `ai_ref` file is now guaranteed to
be a normal, in-bound PNG." False on both counts — re-encoding a NEW
upload cannot retroactively touch a file already on disk, and nothing
migrates old files. Directly contradicted the (correct) comment on
`loadRefJPEG` a few lines below in the same file. Risk: a future reader
could conclude the on-disk tree is sanitized and remove `loadRefJPEG`'s
bound, which is what actually protects pre-existing files. **Fixed**: the
comment now states plainly that the re-encode protects only files written
from now on, and names `loadRefJPEG`'s bounded decode as the thing
protecting files already on disk.

### Should-fix — fixed: confirm handler decoded the same photo twice

`readPhoto` fully decoded the upload to validate it, discarded the image,
returned only the raw bytes; the confirm handler then called
`imaging.Decode` again on those same bytes to get an `image.Image` to
re-encode. Peak memory during confirm could reach ~2× a single decode —
undercutting the ~100MB single-decode budget `MaxPixels` was derived from
in #1328, on the same low-memory Android/Pi hardware this product
targets. **Fixed**: `readPhoto` now returns a small `decodedPhoto{Raw,
MediaType, Img}` struct carrying the already-decoded image; both handlers
(`/identify` uses `.Raw`/`.MediaType` for the AI backend call, `/confirm`
reuses `.Img` directly) now share the one decode `readPhoto` already does.

### Should-fix — fixed: a failed re-encode could leave a poisoned file on disk

If `png.Encode`/`out.Close()` failed after `os.Create` succeeded, the
partial/corrupt file was left under `ai_ref/`. Unlike the catalog
handlers' fixed `thumb.png` name (self-heals on the next upload), this
handler's filename is a unique nanosecond timestamp — it would persist,
and `latestAIRef` picks the lexically newest name while
`loadReferenceImages` has no fallback to `thumb.png` when that decode then
fails. Net effect of one failed write: silently zero reference images
contributed to every future identify call for that item, until another
confirm for it happened to succeed. **Fixed**: `os.Remove` the partial
file on an encode/close failure before returning the error.

### Should-fix — fixed: the new format allowlist was an exported mutable map

`DefaultFormats` shipped as an exported `map[string]bool` package
variable — any package could execute `imaging.DefaultFormats["gif"] =
true` and silently widen the allowlist for every `Decode`/`DecodeBounded`
caller, reopening exactly the silent-widening risk the allowlist itself
exists to prevent (the mechanism #1328 built specifically to stop
`internal/print`'s incidental `image/gif` blank-import from leaking into
an unrelated call site). The old unexported `allowedFormats` this
replaced could not be reached this way. **Fixed**: reverted to an
unexported `defaultFormats`, with an exported `DefaultFormats()` function
returning a fresh copy — safe for a caller to build its own wider/narrower
set from, impossible to mutate the package's own default through.

### Verified, no change needed

- **GIF widening is memory-safe.** Checked against the Go 1.25 stdlib
  source directly (`image/gif/reader.go`): `gif.DecodeConfig` reports the
  logical screen size, `gif.Decode` decodes only the first frame, and the
  decoder itself rejects any frame whose bounds exceed the screen
  (`"gif: frame bounds larger than image bounds"`) — so `DecodeConfig`'s
  size genuinely bounds what `Decode` can allocate; a small declared
  screen can't hide a huge frame. GIF also decodes to `*image.Paletted`
  (~1 B/px), well under the ~15 B/px worst case `MaxPixels` was sized
  against.
- **GIF scoping is properly contained.** `rasterFormats` is package-local
  to `internal/print`; `imaging`'s own default set is unaffected (and,
  post-fix, unreachable from outside the package); the #1328 regression
  `TestDecode_RejectsGIF` still passes; a new test explicitly asserts
  `Decode` still rejects a GIF even after `RasterLogo`'s wider set exists
  elsewhere.
- **Repo-wide sweep for a missed 3rd call site**: `grep -rn
  "image\.Decode\|image\.DecodeConfig\|png\.Decode\|jpeg\.Decode\|gif\.Decode\|webp\.Decode\|bmp\.Decode\|tiff\.Decode"
  --include="*.go" .` (excluding comments/tests) returns zero raw stdlib
  decode call sites outside `internal/imaging` itself. Only png/jpeg
  (imaging's own blank imports) and gif (`internal/print`'s) are
  registered anywhere in the binary — no webp/tiff/bmp — so the allowlist
  is exhaustive. `internal/pages/import_page.go`'s image write was
  already on `imaging.Decode` from #1328, not missed.
- **The two recurring bug classes**: `os.MkdirAll` runs before the write
  in the confirm handler; `itemAssetDir()` uses `paths.Data(...)`, not a
  cwd-relative path. Both clean.
- File-handle hygiene: `out.Close()` runs unconditionally after a
  successful `os.Create`, and both the encode and close errors are
  checked — stricter than the pre-existing catalog convention this
  handler was modeled on (which uses `defer out.Close()` and drops the
  close error).

## Follow-ups (filed as future work, not this card's scope)

1. **Storage amplification**: a ≤4MB uploaded photo is now persisted as a
   PNG re-encode (typically ~15-25MB for a 6MP source), on hardware this
   product targets for low storage — and the only consumer,
   `loadRefJPEG`, immediately downscales it to ≤160px/JPEG q70 anyway.
   Storing the already-downscaled JPEG instead would avoid the waste.
   Natural fit for ut-docs#1416 (downscale-instead-of-reject for oversized
   photos), which already owns this general area.
2. **Error message clarity for an oversized-but-legitimate photo**: a
   12MP phone capture (routinely above `MaxPixels`) now gets "photo must
   be a valid JPEG or PNG," which is misleading — the photo is valid, just
   too large. Also tracked under ut-docs#1416's clearer-error-message
   item.
3. `internal/print/raster.go` dropping its own `image/jpeg`/`image/png`
   blank imports (now covered transitively via `internal/imaging`'s) is
   correct today and commented, but makes `internal/print`'s own
   format support an implicit dependency on `internal/imaging` never
   dropping those imports. Low risk, noted for whoever next touches
   either file.
4. `DecodeBoundedFormats`'s "unsupported format" error no longer says
   which formats a given call site's OWN error text already claims are
   accepted (it did before this generalization, when the set was always
   the same). Now renders the accepted set into the message directly
   (`formatNames` helper) — done in this pass rather than deferred, since
   it was a one-line fix once flagged.

## Verified beyond automated tests

- **TDD re-verification, done independently by both Tester and Reviewer**:
  each of the 4 new call-site fixes (RasterLogo's decode bound, RasterLogo's
  GIF-permitting format set, readPhoto's decode bound, the confirm
  handler's re-encode-not-verbatim-write, loadRefJPEG's decode bound) was
  reverted locally one at a time, its corresponding new test re-run and
  confirmed to fail with a real assertion error (not a compile error
  masking the signal), then restored and confirmed green again. Full
  transcripts of both passes exist in this cycle's session; representative
  example (Reviewer's independent run, `internal/print`):
  ```
  $ go test ./internal/print/ -run 'TestRasterLogoRejectsPixelBomb' -v
  --- FAIL: TestRasterLogoRejectsPixelBomb (0.21s)
      raster_test.go:114: expected a pixel-bomb-sized PNG to be rejected (nil), got a raster
  $ git checkout -- internal/print/raster.go
  $ go test ./internal/print/ -run 'TestRasterLogoRejectsPixelBomb' -v
  --- PASS: TestRasterLogoRejectsPixelBomb (0.12s)
  ```
- Repo-wide grep sweep (above) for any other untrusted-input raw decode
  call site, confirming this closes the class rather than one more
  instance of it.
- Confirmed no other code depends on the pre-fix confirm-handler behavior
  (raw bytes + `.jpg`/`.png` extension matching the upload's format) —
  `refImageNames`/`latestAIRef` already handle both extensions via suffix
  check, so old-format files already on disk keep working unchanged.
- No UI/page route/visual surface touched by this diff (API handlers and
  internal decode logic only) — httptest-level real HTTP round trips
  through the real `mux` (already exercised by every new test) are the
  correct "real" verification layer here per this pipeline's own tester
  standard, not a screenshot check.

## Gate

`gofmt -l .` (clean), `go build ./...` (clean), `go vet ./...` (clean),
full `go test ./...` (all packages green, run independently by Dev,
Tester, and Reviewer at different points across this cycle — including a
`-race` run on the two most-touched packages by Reviewer), and every
CI-blocking guard relevant to this diff's surface: `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-plugin-menu-read.sh`,
`guard-page-http-error.sh`, `guard-docs-shots.sh` — all pass. This diff
touches no SQL/data layer, no money, no i18n strings, no kiosk-engine
routes, no plugin-signing path, and no real client/shop name or
secret-shaped literal appears anywhere in it.

## Safe to merge

Yes.
