# Review: armv7 Android 9 seccomp SIMULATOR (ut-docs#6300, ADR-0188)

**Scope:** uncommitted diff on `test/6300-seccomp-simulator` (base
`fix/6300-armv7-statx-seccomp`): new `scripts/armv7sim/` (host launcher
`main.go`, `cpio.go`, `verdict.go`, guest PID 1 `guestinit/`, `policy/` with the
vendored AOSP `arm_app_policy.cpp` / `arm64_app_policy.cpp` + `NOTICE`, tests and
a green console fixture), `scripts/ci/sim-armv7-seccomp.sh`,
`.github/workflows/ci.yml` (job `armv7-seccomp`: qemu-system-arm, kernel cache,
simulator step, log upload on failure), `README.md`, `RELEASING.md`.

**Reviewer:** Reviewer agent (Opus; the code was written by Sonnet). Formed its own
view from the code, then read the Tester report (`03-tester.md`, PASS, one low item).

Pre-flight: platform claim → `README.md` Android bullet updated (no new platform;
names the simulator), ut-website n/a (claims Android in general), help topic n/a
(test tooling, nothing shop-owner-visible) · till user-visible behaviour n/a ·
`en.json` n/a · ADR n/a (ADR-0188 already covers the guard; this adds a stronger
proof) · release procedure → `RELEASING.md` updated · review record: this file.

## What shipped

A qemu-system-arm VM boots a pinned Debian stretch 4.9 armhf LPAE kernel (https,
sha256 of the `.deb` and of the extracted `vmlinuz`, cached by the deb hash) with an
uncompressed initramfs: a Go PID 1 plus the pristine-libc and fork builds of
`scripts/armv7smoke`. The guest runs three scenarios: pristine without filter
(control, must `OK 200`), pristine under the filter (must die by SIGSYS,
`sigcode=1`, `r7=0x18d` statx), fork under the filter (must `OK 200`, no SIGSYS).
The filter is assembled in Go from the byte-exact AOSP android-9.0.0_r61 tables
the way `seccomp_policy.cpp` `_set_seccomp_filter(APP)` does on `__arm__`, and
installed by `init apply BIN` (drop to uid 10000, `LockOSThread`,
`PR_SET_NO_NEW_PRIVS`, `PR_SET_SECCOMP`, raw `execve`). The host parses the console
(`verdict.go`) and is red on anything but the full green pattern.

## Faithfulness to AOSP (checked independently)

Re-fetched `arm_app_policy.cpp`, `arm64_app_policy.cpp` and `seccomp_policy.cpp`
from `android.googlesource.com` at `android-9.0.0_r61`: sha256 `9718688c…a358`,
`dee2d8d6…e815`, `76e189f0…dc66`, equal to the `NOTICE`/test pins. `policy.Assemble`
matches the C++ line for line: `ValidateArchitectureAndJumpIfNeeded` (LD arch,
JEQ AARCH64 2/0, JEQ ARM 1/0, RET_TRAP, offset = size-2), `ExamineSyscall` (LD nr),
arm64 table, `Disallow` (= `SECCOMP_RET_TRAP` in 9.0), `SetValidateArchitectureJumpTarget`
(`size - offset - 1`, fatal over 255), LD nr, arm table, RET_TRAP. `sock_fprog` /
`sock_filter` layout on 32-bit ARM matches the Go structs (len at 0, pointer at 4;
8-byte instructions). Difference from a device: AOSP installs as root with
`CAP_SYS_ADMIN` in the forked app process and needs no `NO_NEW_PRIVS`; the
simulator drops uid first and therefore sets `NO_NEW_PRIVS`. Neither changes which
syscalls trap.

## Findings

| # | Sev | Finding | Status |
|---|---|---|---|
| R1 | Low (tester) | `TestAssemble` checked only `Jt`/`K` of the secondary arch compare. The Tester's M3 mutant (jf on the placeholder `{jeqK,1,0,ArchARM}`) is an equivalent mutant (the placeholder is overwritten by the patch), which is why the golden hash did not move; the golden hash is also self-derived. | Fixed: the first five instructions are asserted field by field against the C++, the jump target must be `RET_TRAP` then `LD nr`, and both tables must appear verbatim at their offsets. Mutating the real patch line (`jf=1`) goes red (`prog[2]`, golden, `TestEvalArch` x86_64 allowed). |
| R2 | Low | `checkFirstDump` accepted any `PC=… sigcode=1` line anywhere in the block, and the SIGSYS text anywhere. `sigcode=1` is also `SEGV_MAPERR`/`ILL_ILLOPC`; a SIGSEGV dump plus a stray SIGSYS line would pass. | Fixed test-first: `TestVerdictSigcodeMustFollowSIGSYS` red on the old code ("went green"), green after requiring `SIGSYS: bad system call` immediately before the `PC=… sigcode=1` line. |
| R3 | Low (honesty) | No comment or doc said what the simulator does not prove; `guestinit` said it installs the filter "like the zygote does". | Fixed: `sim-armv7-seccomp.sh` header lists the limits (AOSP filter, Fire OS may filter more; static exe exec'd vs c-shared in an ART process; TCG cortex-a15 + Debian kernel); `guestinit` comment corrected; `RELEASING.md` says it does not replace the Fire OS 7 hardware check. README already says the hardware check is pending. |
| R4 | Info | The control scenario runs as root, the filtered ones as uid 10000. | Accepted: the kill is identified by `sigcode=1` + `r7=0x18d`, not by the exit alone, so uid cannot fake it. |
| R5 | Info | `READY filter_sha256` is assembled in PID 1, not read back from the installed filter (4.9 has no `SECCOMP_GET_FILTER` for unprivileged use). | Accepted: same binary assembles both; installation is proven by the pristine kill itself (see the stub run below). |
| R6 | Info | `apt-get install qemu-user qemu-system-arm` is not version-pinned. | Accepted: Ubuntu's signed archive, same convention as the existing `qemu-user` step; the kernel (the only download) is https + sha256 pinned twice. Action refs `cache@v6` / `upload-artifact@v7` follow the repo's existing tag usage. |

## Verified by running it

- `go test -count=1 ./scripts/armv7sim/...` ok; `go vet` (host and
  `GOOS=linux GOARCH=arm`) ok; `golangci-lint run ./scripts/armv7sim/...` 0 issues;
  `gofmt -l` clean; `shellcheck scripts/ci/sim-armv7-seccomp.sh` clean;
  `actionlint` only the two pre-existing warnings (ci.yml 573, 952);
  `guard-ci-tests-sandboxed.sh` ok.
- Full simulator, `/opt/homebrew/bin/qemu-system-arm` 11.1.3, cached kernel:
  **GREEN, qemu 14 s, script 16 s wall.**
  - `UTSIM READY kernel=4.9.0-13-armmp-lpae filter_len=194 filter_sha256=a07ae561…bf1a`
  - control: `OK 200`, `END control-pristine-nofilter exited=true code=0`
  - pristine under filter: `SIGSYS: bad system call` / `PC=0x29f14 m=3 sigcode=1` /
    first dump `r7 0x18d`, `END pristine-filtered exited=false signaled=true signal=6`
  - fork under filter: `OK 200`, `END fork-filtered exited=true code=0 signaled=false`
- **Red-first re-verified personally:** `PR_SET_SECCOMP` replaced by a no-op
  `prctl(39,0,0)` in `guestinit` → rc 1, **RED**: "pristine-filtered: the pristine
  build SURVIVED the filter; the simulator cannot kill, so it proves nothing" (+ no
  signal, no SIGSYS, no sigcode=1); `END pristine-filtered exited=true code=0`.
  File restored (`cmp` identical to the backup). A first attempt whose stub returned
  EINVAL also went red (`UTSIM FATAL PR_SET_SECCOMP`).
- No qemu-system-arm left running; the UTM Windows VM untouched. No customer or
  personal names in the diff.

## Verdict

**Approve** for commit with R1-R3 fixed in the working tree.

Second independent review (Opus, launched from the orchestrating session for #6300, plus a separate
Tester re-run: real run GREEN, both mutations red): **Approve**, no blocking or medium findings; AOSP
sources and the Debian kernel .deb provenance re-verified by hash. Low findings: (1) log upload on a
job timeout, **fixed** (`if: failure() || cancelled()`); (2) relative `--fork/--pristine/--log` paths
break after the `cd`, (3) one `rm -f` of a cached kernel file outside mktemp, (4) `--log` directory not
created, (5) Apache-2.0 text not vendored beside NOTICE: **accepted, deferred** as a follow-up (the
CI job passes absolute paths).

## Not verified / deferred

- The `armv7-seccomp` job on ubuntu-latest (apt qemu-system-arm with
  `--no-install-recommends`, archive.debian.org reachability, TCG time on x86):
  needs the push. Tester estimate about 3 min first run, 2 min cached; timeout 25.
- Real Fire OS 7 device (AC6-AC8): `blocked:env`, local lane, unchanged.
