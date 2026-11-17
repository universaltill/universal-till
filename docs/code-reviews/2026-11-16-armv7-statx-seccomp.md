# Review: armv7 Android boot under the Android 8-10 seccomp filter (ut-docs#6300, ADR-0188)

**Scope:** uncommitted diff on `fix/6300-armv7-statx-seccomp`: `go.mod`/`go.sum`
(`replace modernc.org/libc => github.com/universaltill/modernc-libc`),
`scripts/armv7smoke/`, `scripts/ci/{guard-armv7-seccomp,guard-libc-fork}.sh` and
their `_test.sh`, `scripts/ci/libc-ut.patch`,
`scripts/ci/{gen-,}android9-arm-seccomp-allow*`, `.github/workflows/ci.yml`
(job `armv7-seccomp` + three build-job steps), `README.md`, `RELEASING.md`; the
public fork `universaltill/modernc-libc`; ADR-0188 (record in
`ut-docs/code-reviews/2026-11-16-adr-0188-armv7-libc-fork.md`).

**Reviewer:** independent Reviewer agent (Opus; the code was written by Sonnet),
plus a second read-only opinion from a Fable subagent on the patch, the guards and
the supply chain. Inputs: BA, Architect, Dev and Tester notes for #6300.

Pre-flight: platform claim → `README.md` Android bullet updated, ut-website n/a
(it claims Android in general, not armv7/Fire OS; no new claim to mirror), help
topic n/a (nothing shop-owner-visible) · till user-visible behaviour n/a (crash
fix, no screen) · `en.json` n/a · new ADR → `adr/README.md` row updated
(`guard-adr-index.sh` ok) · release procedure → `RELEASING.md` updated ·
review record: this file.

## What shipped

All 32-bit ARM raw syscalls in libc pass through eight functions in
`syscall_musl.go`; each now returns `-ENOSYS` first for numbers the Android 9
filter does not know (from 397, below the ARM-private range), so musl's own
fallbacks (`fstatat64`, `clock_gettime32`) run. Delivered as an org fork pinned by
`replace` + `go.sum`, with a static pin guard, a network proof that the fork is
upstream plus exactly the checked-in patch, and a qemu-arm strace guard that runs
the real `mobile.Start` boot against the AOSP Android 9 arm allow-list, with a
negative run on pristine libc.

## Findings

| # | Sev | Finding | Status |
|---|---|---|---|
| R1 | **High** | `guard-armv7-seccomp.sh` required `^OK 200`. Native `qemu-arm -strace` writes `<tid> write(1,0x..,7)` to the same file just before the program's output, so the marker always lands mid-line (`14 write(1,0x7170dc0,7)OK 200`). Reproduced with streams merged inside the container (as on the CI runner): the positive guard failed on a correct build. Dev/Tester passed only because the docker shim demuxes stdout/stderr and reorders them. CI would have been red on every run. | Fixed: marker matched as `(^\|\))OK 200`; regression fixtures for the glued form and for `NOT OK 200`. |
| R2 | Medium | The checker only matched `<tid> name(`. qemu prints a number it has no name for as `<tid> Unknown syscall N` (verified with qemu-user 10 and a probe binary calling 999), so a syscall newer than qemu's table passed silently. The `_test.sh` fixture used an invented `syscall_999(` form. | Fixed: `Unknown syscall N` is reported as `syscall_N` and always rejected; fixture replaced by qemu's real format. |
| R3 | Low (tester #1) | `utPre411Kernel` gated every `n >= 397`, including the ARM-private calls `0x0f0001..0x0f0005` (`cacheflush`, `set_tls`), which every kernel knows and Android allows. Nothing in libc v1.75.6 issues them (constants only), so latent. | Fixed: new fork tag `v1.75.6-ut.2` bounds the gate at `0x0f0000`, adds `TestUTPre411KernelRange` (red on ut.1 under qemu-arm: 0xf0002/0xf0005 true; green on ut.2; also green on linux/arm64). `go.mod`/`go.sum`, `libc-ut.patch`, ADR, fork README updated. `-ut.1` left in place (tags are never moved or deleted). |
| R4 | Low (tester #4) | New CI job inherited the default token. | Fixed: `permissions: contents: read` on `armv7-seccomp`. |
| R5 | Medium-low | `--verify-upstream` ran only when `go.mod`/`go.sum` changed, so a PR editing just `libc-ut.patch` or the guard could make the "reviewed patch" drift from the fork unseen. | Fixed: the trigger also covers `scripts/ci/libc-ut.patch` and `scripts/ci/guard-libc-fork.sh`. |
| R6 | Low | `--verify-upstream` compared trees in a scratch module without tying them to this repo's `go.sum`. | Fixed: the fork's downloaded `Sum`/`GoModSum` must equal the `go.sum` lines (mutation: a tampered `go.sum` hash is rejected). |
| R7 | Info | Pin guard interpolated the version as a regex (`.` = any char) and grepped `go.sum` by regex. Not exploitable (Go rejects such versions). | Fixed: literal match (`=~ ^"${req}"-ut`), exact awk field match on `go.sum`; new test case, verified red before the fix. |
| R8 | Info (tester #3) | The qemu proof is a syscall trace on the host kernel, not a real SIGSYS. | Accepted: that is the design; the device run (AC6) stays the final proof. |
| R9 | Low | Sub-397 numbers Android 9 also blocks (`membarrier`, `pkey_*`, `pwritev2`, `userfaultfd`, `mlock2`) are not gated. libc only has direct wrappers for `mlock2`/`pwritev2`, which SQLite never calls; the strace guard catches a regression on the boot path. | Accepted, residual risk named in the architect note §5. |
| R10 | Low | The gate applies to every linux/arm build regardless of kernel (2038 wrap for SQLite `now()` in a 32-bit process). No linux/arm release target exists. | Accepted in ADR-0188 Consequences. |
| R11 | Medium (process) | The fork has no branch protection and no `v*` tag ruleset (`rulesets` = `[]`), while the ADR claimed it was protected. Builds stay safe (go.sum + sum.golang.org), `GOPROXY=direct` builds would break on a moved tag. | ADR corrected to "must be; owner settings step, not yet applied". Owner action, not changed here. |

## Verified by running it

- `guard-armv7-seccomp_test.sh` (10 cases) and `guard-libc-fork_test.sh` (11 cases): pass.
- `guard-libc-fork.sh` static and `--verify-upstream` on the real tree:
  `v1.75.6-ut.2 == modernc.org/libc v1.75.6 + scripts/ci/libc-ut.patch`.
- qemu-arm 10 (docker), `QEMU_UNAME=4.9.190`, streams merged as on the runner,
  after `go clean -cache`: ut.2 build → `statx=0`, `clock_gettime64=0`, guard rc 0;
  pristine libc (replace dropped, tidy in a temp modfile) → `statx=74`,
  `clock_gettime64=107`, guard rc 1 naming both. The full guard end to end through
  the dev's shim: rc 0.
- `go build ./...`; `go vet ./mobile ./scripts/...`; cross-builds of
  `internal/db` + `mobile` (CGO off) for linux/arm, linux/arm64, linux/amd64,
  windows/amd64, darwin/arm64; `go test -count=1 ./internal/db ./mobile` ok.
- `actionlint`: only the two pre-existing warnings (ci.yml 573, 952);
  `shellcheck scripts/ci/*.sh`: 0 issues (fixed the dev's SC2001 style note,
  which CLAUDE.md does not allow); `guard-ci-tests-sandboxed.sh` ok;
  `golangci-lint run ./scripts/... ./mobile/...` 0 issues; `gofmt -l .` clean.
  Full `go test ./...` and e2e left to the batch run.
- No `GOPROXY`/`GONOSUMDB`/`GOPRIVATE`/`GOFLAGS` override anywhere, so the fork is
  verified against sum.golang.org (Fable check). No customer or personal names in
  the diff.

## Verdict

**Approve** for commit once the owner accepts ADR-0188 (status Proposed), with the
fixes above in the working tree.

## Not verified / deferred

- Real Fire OS 7 / Android 9 armv7 device (AC6-AC8): `blocked:env`, local lane.
  Fire OS may filter more than AOSP.
- Real `gomobile bind` with the replace (no NDK here; `android-ci.yml` covers it on the PR).
- The `armv7-seccomp` job on GitHub (Ubuntu's `qemu-user` version, network steps).
- Fork settings: branch protection on `main`, `v*` tag ruleset, org 2FA (owner).
- Upstream issue at gitlab.com/cznic/libc for the drop condition (architect §6).
