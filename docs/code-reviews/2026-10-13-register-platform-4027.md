# Review: send platform on device register (ut-docs#4027)

## What shipped
- `internal/enroll`: `devicePlatform()` (`runtime.GOOS + "/" + runtime.GOARCH`,
  the string the cloud heartbeat in `internal/cloudsync` reports) is sent as
  `platform` on `POST /v1/stores/register` and on the till's own
  `POST /v1/stores/devices/register`, so ut-cloud's re-pair retire guard
  (#2804) can tell two machines with one name apart at registration.
- `registerDeviceID` gains a `platform` parameter. `VouchForReplica` passes
  `""`: the main till doesn't know the replica's platform, and its own
  would be wrong on the replica's row.
- Cloud side: ut-cloud `feat/4027-register-platform` (accepts and stores it;
  older clouds ignore the extra field).
- No UI, locale, migration or help change.

## Review
Independent reviewer (Fable; code by Opus 5.5). Verdict: safe to merge.
Checked every caller of `registerDeviceID` (own device, the pre-#2730
replica self-registration fallback — its own platform, correct — and the
vouch path, `""`) and that no other till code posts to either register
endpoint. Findings were all on the cloud side (see its review record;
follow-ups ut-docs#4249, ut-docs#4250). No file I/O, so the `MkdirAll` /
`paths.Data` checks are N/A.

## Verified
- TDD re-verified by the reviewer in a throwaway worktree: with `enroll.go`
  and `replica.go` reverted, `TestRegisterSendsTillID` and
  `TestRegisterDeviceSendsReportedTillID` fail (`platform = <nil>, want
  "linux/amd64"`); restored, pass. The vouch test asserts no `platform` is
  sent for a replica.
- `go build ./...`, `go test ./...`, golangci-lint, gofmt, and the
  `ci.yml` guards run locally. Local-only gaps: `shellcheck` isn't installed
  in the container (no shell changes), and the deadcode guard can't build
  `cmd/unitill-desktop` here (no GTK headers), so it flags two
  `internal/logging` functions used only by that root — same on `main`,
  CI analyses all roots.

## Verdict
Safe to merge, after the ut-cloud side.
