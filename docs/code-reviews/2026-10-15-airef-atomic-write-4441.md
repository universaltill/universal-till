# 2026-10-15 — ai_ref photos appear only once complete (ut-docs#4441)

## What shipped

`internal/itemimages.StoreAIRef` used to open the final `<nanos>.png` with
`O_EXCL` and `png.Encode` straight into it. Until `Close`, a half-written
PNG sat under its final name. Any reader could catch it mid-write:
- `LatestAIRef` and the AI identify request (via `Ref`/`item_image_open`);
- `PruneAIRefs` racing a second store;
- `TestPluginIdentify_PickAddsLineThenStoresPhoto_4006`, which polls for
  `*.png` and then reads it. This is the CI flake ("stored ai_ref: format
  "png", err unexpected EOF") seen on universal-till#1975.

Now:
- The PNG is encoded to a hidden temp file in the same directory,
  `.<n>-*.png.tmp`. `refImageNames` only takes `.png`/`.jpg`, so it ignores
  the temp. The temp is `chmod 0644`, like the photos always were.
- `publishAIRef` gives the complete file its final name with `os.Link`.
  The link fails rather than replace an existing name; on `ErrExist` it
  bumps the name, as the old `O_EXCL` did, to separate stores on a coarse
  clock.
- Where hard links are unsupported, it falls back to stat-then-`Rename`.
  It joins the two errors if both fail.
- The deferred `Remove` drops the temp on every path.

## Tests

- `TestStoreAIRef_NoPartialFileVisible_4441` uses a slow-encoding image
  (~130 ms) and polls the folder while the store runs. Every `*.png` (and
  `LatestAIRef`'s pick) it sees must decode fully, and no temp file is left
  behind.
  - Written first. It failed against the old code ("…png visible before
    StoreAIRef returned"; after the review fix, "half-written", 5/5 runs).
  - It passes 150/150 under `-race`.
- `TestLatestAIRef_IgnoresTempFiles_4441`: crash residue is never taken
  for a photo.
- `go test -race -count=50 -run PickAddsLineThenStoresPhoto ./internal/pages/`
  passes 50/50.

## Review (independent, Sonnet)

Fable was refused (out of usage credits), so the review ran on Sonnet, per
MODEL-ROUTING. The author was Opus 5.5.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The first version of the new test failed whenever it saw the *complete* file between publish and return (7/150 runs). It asserted "not visible until return" instead of "never partial". | Fixed: the test asserts that any visible file decodes fully. 150/150 pass. |
| 2 | minor | The test only has teeth while the encode is slow. | Accepted: documented in the test comment. Old code fails 5/5. |
| 3 | nit | `os.CreateTemp` makes 0600, so photos would change from 0644. | Fixed: `tmp.Chmod(0o644)`. |
| 4 | nit | When the Rename fallback fails, the original Link error is lost. | Fixed: `errors.Join`. |
| 5 | info | `.tmp` crash residue is never pruned. | Accepted: needs a crash mid-encode, and the files are ignored by every reader. |

Rules checked: `os.MkdirAll` runs before the write, and the path comes from
`paths.Data` via `AIRefDir`. No SQL, strings, money or UI are touched.

## Gate

- `gofmt` and `go build ./...` clean; `golangci-lint` reports 0 issues.
- `go test ./...` is green apart from `internal/plugins`, which hit the
  default 600 s timeout in a parallel local run. CI runs that package on
  its own with a wider timeout (ut-docs#643). It was re-run that way, and
  it only reads ai_refs through `Ref`.
- Guards pass: data-access, netaccess, core-neutral,
  deadcode-baseline-growth.

## Verdict

Safe to merge. No user-facing surface, so no manual or help change.
