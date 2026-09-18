# 2026-09-18 — image-upload test fixture is brittle across Go versions (ut-docs#2302)

## What shipped

`internal/pages/catalog/image_upload_test.go`'s `oversizedPNG` fixture helper
builds a 7000×6000 uniform gray PNG (42M pixels, over `imaging.MaxPixels`) to
prove the item/variant image-upload handlers reject an oversized image via
the cheap `image.DecodeConfig` dimension check rather than some unrelated
failure. The helper carried a self-check that the fixture stays "modest" —
previously a hardcoded `buf.Len() > 200_000` assertion, which broke on Go
1.27 (PNG encoder output isn't guaranteed stable across Go versions): the
fixture compressed to 280,281 bytes there vs. ~50KB on Go 1.25 (CI's
version, still green throughout). A local developer on a newer toolchain
saw four failing tests indistinguishable from a real regression.

Replaced the single hardcoded byte count with two checks tied to the real
invariants that matter:

1. `buf.Len() >= maxUploadBytes/2` (`maxUploadBytes = 10 << 20`, matching
   `handlers.go`'s two `ParseMultipartForm(10 << 20)` call sites exactly) —
   proves the fixture stays comfortably under the real upload-size cap, so
   the test is exercising the pixel-dimension guard and not the size cap.
2. A compression-ratio floor (`decodedBytes / buf.Len() >= 20`,
   `decodedBytes = 7000*6000` at 1 byte/pixel for `image.Gray`) — tolerates
   Go-version-to-version PNG encoder differences while still catching a
   fixture that stopped being a meaningful pixel-bomb case.

Both failure messages now include `runtime.Version()` for diagnostics, per
the issue's own suggestion.

## Independent review

Fresh-context Sonnet subagent (isolated worktree, `complexity:easy` routing
— this is the one case where "different model" relaxes to "different
instance," since the change is mechanical). **No findings** — verdict: safe
to merge.

Verified independently: the arithmetic actually resolves the reported
failure (280,281 bytes → ratio 149, both checks pass with comfortable
margin); a hypothetical worse-compressing fixture (3,000,000 bytes → ratio
14) correctly still fails; `buf.Len()==0` isn't reachable given the
preceding `png.Encode` error check; the `10 << 20` constant matches
`handlers.go`'s real cap at both call sites (no drift); ran
`gofmt`/`go build`/`go vet`/`golangci-lint`/the 4 affected tests plus the
full `catalog` package — all clean. Also considered and rejected the
issue's alternative suggestion (pinning `toolchain go1.25.x` in `go.mod`)
as less durable — it suppresses this one fixture's symptom while pinning
every other developer's local toolchain, rather than fixing the test to
assert on the property that actually matters.

Two nits noted, neither requiring action: the ratio calc's
division-by-zero is real in principle but unreachable in practice; the
toolchain-pin alternative was a legitimate design choice, not a defect.

## Verified beyond automated tests

- `go build ./...`, `go vet ./internal/pages/catalog/...`,
  `golangci-lint run ./internal/pages/catalog/...` — clean.
- `go test ./internal/pages/catalog/... -count=1` (full package) — green.
- Cross-checked the `10 << 20` cap literal against both real call sites in
  `handlers.go` (lines 1695, 1880) by hand.
- No real client/shop name, no credential-shaped literal (test-only Go
  code; no UI/i18n/money surface — those checklists don't apply).

## Safe-to-merge verdict

Yes. Test-only change, no production code touched, no behavior change to
what ships.

## Explicitly deferred

None — this was a self-contained, single-file fix.
