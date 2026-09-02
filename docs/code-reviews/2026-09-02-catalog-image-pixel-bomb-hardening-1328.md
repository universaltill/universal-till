# Code review: catalog image pipeline pixel-bomb / decode-size hardening (ut-docs#1328)

## What shipped

`image.Decode` was called on up to a 10MB source image in three catalog
handlers and one import path, with no check on the DECODED pixel dimensions
before allocating a buffer for them and re-encoding to PNG. A small,
well-formed file that declares an enormous width×height (a classic "pixel
bomb") can allocate far more memory than the byte cap suggests, on the same
low-memory Android/Pi hardware this product targets.

**Fix** (`internal/imaging/decode.go`, new package):

- `Decode(raw []byte)` / `DecodeBounded(raw []byte, maxPixels int64)` reads
  only the declared dimensions via `image.DecodeConfig` (no pixel buffer
  allocated) and rejects anything over the limit — or not PNG/JPEG — before
  the full `image.Decode`.
- Wired into all four call sites doing this decode/re-encode dance:
  `internal/pages/catalog/handlers.go`'s `POST /api/catalog/item/image`,
  `POST /api/catalog/variant/image`, and `saveLookupImage` (the
  product-database lookup thumbnail path — not in the original issue's
  listed scope, added because it shares the identical defect: fixing 3 of 4
  known call sites would leave the exact bug this card exists to close);
  `internal/pages/import_page.go`'s `.bkp` commit-time image write.

## Review

Independent review by a different model (Opus) in a fresh context that
never saw the dev reasoning (`SendMessage`-free subagent, `general-purpose`
type, model override `opus`). Findings below were fixed in this pass.

**Verdict: one blocker fixed (the size constant), two should-fix items
fixed (format allowlist, a read-error-ordering nit), three follow-ups
filed rather than folded into this card's scope.**

### Blocker — fixed: `MaxPixels` didn't bound memory the way it claimed

The first draft set `MaxPixels = 40_000_000` on a "generous headroom, ~4
bytes/pixel" assumption. The reviewer measured real per-format decode cost
on this Go toolchain rather than trusting that assumption:

- 8-bit PNG → `*image.NRGBA`, **4.04 B/px → 161MB** at a 40M-pixel cap.
- 16-bit PNG → `*image.NRGBA64`, **8.04 B/px → 322MB**.
- Progressive JPEG is worse — `image/jpeg` holds per-component coefficient
  blocks alongside the YCbCr output plane, **~15 B/px effective → ~600MB**
  at the same cap.

Failure scenario: a uniform 16-bit RGBA PNG at ~6320×6320 (39.9M px)
compresses to ~370KB — passes the 10MB byte cap and the old pixel cap —
then allocates ~320MB on a 1-2GB Android till or Pi; two concurrent
uploads OOM the process mid-shift. **Fixed** by re-deriving the constant
from the measured worst case: target a ~100MB ceiling for a single decode
(generous on any device with ≥1GB RAM, safe under 2 concurrent uploads) ÷
~15 B/px worst-case ≈ 6.6M pixels, rounded down to **`MaxPixels =
6_000_000`** (≈2828×2121) with the real math now in the doc comment instead
of the wrong 4-B/px claim. A photo above that is rejected outright rather
than silently truncated or corrupted — see Follow-ups for the better
long-term answer (downscale instead of reject).

### Should-fix — fixed: format allowlist

`image.Decode` dispatches to any format registered anywhere in the built
binary, not just what a given call site's own blank-imports suggest.
`internal/print` blank-imports `image/gif` for an unrelated reason
(confirmed live in the shipped binary via `go list -deps`), which — before
this fix — silently made GIF a 5th accepted format at all four call sites
here, with GIF's own dimension limits never audited against `MaxPixels`.
Fixed: `DecodeBounded` now captures the format string from both
`DecodeConfig` and `Decode` and rejects anything outside `{"png", "jpeg"}`
— the only two formats every caller's own error text already claimed to
accept. Regression test `TestDecode_RejectsGIF` encodes a real GIF and
confirms rejection. (The reviewer separately verified there is no
TOCTUU/format-mismatch gap between `DecodeConfig` and `Decode` for PNG/JPEG
in the stdlib — the allowlist is defense-in-depth against a *different*
format being silently accepted, not a fix for a config/decode disagreement
that doesn't exist today.)

### Nit — fixed: read-error check ran after the decode call

`handlers.go`'s two upload handlers checked `readErr != nil || err != nil`
in one branch after already calling `imaging.Decode` on a possibly
incomplete `raw` buffer. Backwards, not a correctness bug (both errors
still produced the same 400), but reordered so a read failure short-circuits
before attempting to parse a truncated buffer.

### Nit — fixed: variant-upload test lacked its sibling's file-not-written assertion

`TestItemImageUpload_RejectsOversizedImage` already asserted the rejected
upload leaves no `thumb.png` behind; `TestVariantImageUpload_RejectsOversizedImage`
didn't have the equivalent check for the variant thumbnail path. Added.

### Coverage added by a concurrent run on this same branch

An overlapping run of this pipeline (per the standing lane-ownership rules,
several runs of the same hourly cloud routine can be mid-flight at once)
picked up this same card independently and pushed to this branch while this
session was still finishing its own review-fix pass. It reconciled by
taking this session's push as the base rather than force-overwriting it,
and added exactly the one piece of coverage this pass's findings still
lacked: `internal/pages/import_bkp_image_test.go`'s
`TestImport_BkpOversizedImageWarnsAndFallsBackToPlaceholder` — the `.bkp`
commit-time image path (`internal/pages/import_page.go`) was wired onto
`imaging.Decode` from the first commit but, unlike the two catalog upload
handlers, had no test proving the guard actually fires there. It does:
a real 42M-pixel PNG resolved from the archive's `documents.zip` is
rejected, falls back to the placeholder icon, and surfaces the existing
`import.status.image_undecodable` warning — same pattern as the sibling
dangling-image-path test. Verified independently in this pass (full gate
+ guards re-run on the reconciled tree, below).

## Follow-ups filed (not this card's scope)

- **ut-docs#1416** — downscale an accepted-but-large photo instead of
  rejecting it outright. The current fix only bounds the *server's* decode;
  an accepted image is still re-encoded and served at full resolution
  (`catalog_row.html`, the sale grid, the self-order kiosk), and a
  legitimate 48MP phone-camera photo (a stock capture mode, routinely
  8-9MB — inside the byte cap, outside the new pixel cap) is rejected with
  no way for a shop owner to understand why. Downscaling to a bounded max
  edge (the pattern `internal/print/raster.go` and `internal/pages/ai_api.go`
  already use) fixes both at once and is the better long-term answer; it's
  a bigger change than this security-hardening card's scope.
- **ut-docs#1416** (same card) also covers a clearer client-facing error
  message for the reject-outright case in the meantime.
- **ut-docs#1417** — `internal/pages/ai_api.go`'s `readPhoto` decodes an
  untrusted upload with raw `image.Decode`, the exact defect this card
  exists for, on a session-gated but not otherwise scoped-down endpoint.
  Worse: the confirm path writes the raw uploaded bytes to `ai_ref/`
  unre-encoded, and a later handler `image.Decode`s those stored bytes on
  every subsequent identify call — one stored pixel bomb becomes a
  repeatable OOM. Flagged by the reviewer as the most important of the
  out-of-scope sites; filed as its own card given the persisted-bytes
  angle raises the severity above a simple "same fix, different call site."

## TDD verification (independently re-run by the reviewer, red then green)

Reproduced by reverting only the two call-site files with tests in place
(`git checkout main -- internal/pages/catalog/handlers.go
internal/pages/import_page.go`), confirming this diff's own regression
tests actually discriminate old vs. new behavior rather than merely
matching an HTTP status code both versions would produce:

```
$ go test ./internal/pages/catalog/... -run 'RejectsOversizedImage' -v
--- FAIL: TestItemImageUpload_RejectsOversizedImage    (got 200, wanted 400)
--- FAIL: TestVariantImageUpload_RejectsOversizedImage  (got 200, wanted 400)
```

The fixture matters here: an earlier draft's regression test used a
hand-crafted header-only PNG (signature + IHDR, no pixel data) declaring
huge dimensions — but that fails to fully decode under EITHER old or new
code (no IDAT chunk), so both produced 400 for unrelated reasons and the
test didn't actually prove the dimension guard was doing anything. Replaced
with `oversizedPNG`: a REAL, fully valid, fully decodable 7000×6000
solid-gray PNG (42M px, compresses to ~50KB) that old code decodes and
accepts, and new code rejects via the cheap `DecodeConfig` check — this is
what makes the red/green cycle below meaningful rather than coincidental.

Restored (`git checkout HEAD -- <same files>`, verified `git status
--short`/`git diff --stat` matched the committed diff again):

```
$ go test ./internal/imaging/... ./internal/pages/catalog/... -run 'RejectsOversizedImage|TestDecode_RejectsGIF' -v
--- PASS: TestDecode_RejectsGIF
--- PASS: TestItemImageUpload_RejectsOversizedImage
--- PASS: TestVariantImageUpload_RejectsOversizedImage
ok
```

The imaging package's own header-only-crafted-file test
(`TestDecode_RejectsCraftedPixelBomb`) is a different, still-valid check:
it proves `DecodeBounded`'s dimension guard fires from `DecodeConfig` alone
(the real attack shape — a decoder never needs valid pixel data to read
IHDR), independent of whether the bytes would ever fully decode.

## Full gate

```
gofmt -l .                                              → empty
go build ./...                                          → clean
go vet ./...                                             → clean
go test ./internal/imaging/... ./internal/pages/catalog/... -race  → ok
go test ./...                          (full suite, no -race)      → all packages ok, zero failures
bash scripts/ci/guard-data-access.sh                     → pass
bash scripts/ci/guard-i18n.sh                             → pass (1338 keys; no new user-facing strings)
bash scripts/ci/guard-compliance-claims.sh                → pass
bash scripts/ci/guard-docs-shots.sh                       → pass (re-run after `make docs-shots`; only
                                                              web/help/img/manifest.json's surface hash
                                                              moved — handlers.go/import_page.go register
                                                              screenshotted routes so the guard hashes the
                                                              whole file, but no actual UI markup changed,
                                                              so zero PNGs regenerated this time)
bash scripts/ci/guard-htmx-loaded.sh                       → pass
```

Re-run in full on the final tree after the concurrent-run reconciliation above
(new commit `598129c`) — all clean, same results.

`build` on GitHub Actions failed once on this PR (commit `4aecb5e`) with
`guard-htmx-loaded.sh` flagging `web/ui/pages/setup.html` — a file this diff
never touches. `git diff` confirmed `setup.html` unchanged across the PR's
base-commit gap, and a local `git merge origin/main` reproducing the exact
PR-merge tree passed the guard clean; re-running the failed job once (per
this pipeline's flake protocol) came back green with no further changes
needed. Recorded here since a future reader of this PR's checks history
will see one red run.

`internal/pages` full-package `-race` is known to time out in this sandbox
independent of this diff (ut-docs#1366/#1394); `-race` was scoped to the
packages this diff actually touches instead, per that precedent.

## Conventions

No SQL outside `internal/data`/`internal/db` (this change touches neither).
No new user-facing string (error text on the two multipart handlers is a
pre-existing plain `http.Error`, already an accepted `guard-i18n.sh`
exemption per its own documented ~231-site list; the `.bkp` import path's
warning reuses the existing `import.status.image_undecodable` locale key
unchanged). No money handling, no migration, no kiosk-engine reference. No
real shop/client name anywhere in the diff or this record.

## Safe-to-merge verdict

**Yes.** The size constant now bounds real per-format decode memory rather
than a wrong 4-B/px assumption; the format allowlist closes a live GIF
acceptance gap (and the latent risk of a future unrelated blank-import
silently widening accepted formats again); TDD claims were independently
re-verified red-then-green with a fixture that actually discriminates old
vs. new behavior; the full gate plus every guard this diff can affect
(including `guard-docs-shots.sh`, which this diff does invalidate via
whole-file hashing) is green on the final tree; and the three follow-ups
(downscaling, clearer rejection messaging, the `ai_api.go` site) are filed
as separate cards rather than silently left unaddressed.
