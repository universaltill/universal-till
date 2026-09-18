# Retire the direct `gmp3` driver scaffold in plugins/tax-tr (ut-docs#2409)

**Date:** 2026-09-18
**Card:** ut-docs#2409 (child of umbrella #1280; `blocked:dep` on #2408 / ADR-0102, released today)
**Complexity:** easy — Sonnet dev, fresh-context Sonnet review
**Lane:** `lane:local`

## What shipped

- `plugins/tax-tr/okc/drivers.go`: `GMP3Driver` and the `"gmp3"` case removed;
  `DriverNames` is now `bridge, hugin-pclink, pavo-rest, token-x`. Doc comment
  on `DriverNames` records why: GİB GMP-3 v5.0 §3.3 requires PC-hosted sales
  software to link the maker's compiled GMP-3 library (browser-served software
  through a separate middleware on that PC), so a from-spec driver in a WASM
  plugin can never be built — ADR-0102 Decision 4. A maker with only that
  library is reached through a bridge process speaking bridge protocol v0.
  "Same status as GMP3Driver" comments on the Hugin/Pavo scaffolds rewritten.
- `plugins/tax-tr/okc/bridge_test.go`: `TestNewDriver_GMP3Retired` (driver
  `gmp3` → `ErrUnknownDriver`, message names the four known drivers) and
  `TestDriverNames_NoGMP3` (pins exact membership).
- `plugins/tax-tr/README.md`: `gmp3` row dropped; new "Why there is no direct
  GMP-3 driver" section.
- `internal/plugins/okc_plugin_test.go`: the "scaffold driver" subtest seeds
  `hugin-pclink` instead of the removed `gmp3` — still a real fail-closed
  scaffold refusing the tender through the real WASM runtime.

Behaviour for a till whose settings still say `okc.driver=gmp3`: `main.go`'s
existing `ErrUnknownDriver` branch logs "okc: tender refused — plugin not
ready for this device" and exits 1 — tender refused, basket kept, no crash.

## TDD evidence

Both new tests written first; against the old `drivers.go` they fail with
`DriverNames = [bridge gmp3 hugin-pclink pavo-rest token-x], must not
contain "gmp3"` / `gmp3 driver err = <nil>, want ErrUnknownDriver`; pass
after the change. Re-verified by the orchestrator by swapping the old
`drivers.go` back in and re-running the two tests (fail), then restoring
(pass).

## Independent review (fresh-context Sonnet)

- **should-fix (fixed):** the README paragraph and the `DriverNames` comment
  said *every* maker integration is a bridge process — stronger than
  ADR-0102, which explicitly leaves "does a maker's own REST/cloud API count
  as the maker's library" as a per-maker question, and contradicted the three
  maker-API scaffolds kept right next to the text. Reworded in both places to
  scope the bridge to a maker whose only interface is the compiled library
  and to name the open question.
- **nit (fixed in the same rewording):** the README now points at the ADR's
  other open item (tablet-hosted till one wired hop from the bridge, §5.3).
- Verified clean: no live `gmp3`/`GMP3Driver` reference remains (only
  historical review records and the playbook, handled by universal-till#1262);
  `main.go` trace to `ErrUnknownDriver`; both tests are real (re-adding the
  case or mistyping a kept name fails them); the runtime subtest still goes
  through the WASM host and refuses; `gofmt` clean; no secret or real shop
  name.

## Gate

`go build ./...`, `go vet ./plugins/... ./internal/plugins/...`,
`go test ./plugins/... ./internal/plugins/... -count=1`,
`GOOS=wasip1 GOARCH=wasm go build ./plugins/tax-tr`, `guard-data-access.sh`,
`guard-i18n.sh` — all green (dev run); `go test ./plugins/tax-tr/...` re-run
green after the review rewording.

## Verdict

Safe to merge. Nothing deferred; the playbook wording lands in
universal-till#1262 under the parent card.
