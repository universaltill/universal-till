# Code review — Android Bluetooth backend seam (ut-docs#1721)

**Date:** 2026-09-07
**Branch:** `feat/1721-android-bluetooth-backend-seam` (reviewed at WIP `d7687b0`)
**Reviewer:** independent fresh-context Opus subagent, review-only pass (no fixes
applied; findings handed back to the orchestrator to triage)
**Original verdict:** **NEEDS FIXES — do not merge as-is.** Finding 1 is a blocker: the
diff's headline deliverable, `mobile.SetBluetoothBridge`, is **silently dropped by
`gomobile bind`** and does not exist in the generated Kotlin/Java API. Finding 2
explains why no CI gate would have told anyone. Everything else in the diff is
sound, well-tested and correctly scoped; the seam's Go half is genuinely good work
undermined by a cross-language constraint the ADR assumed away.

**Post-fix status (orchestrator, same day): all 8 findings addressed — see
"Resolution" at the end of this document. Final verdict: SAFE TO MERGE.**

## What shipped

Per ADR-0080, a second backend for `internal/bluetooth.Client` on Android, where
there is no D-Bus and no `bluetoothd` (ut-docs#1643):

- **`internal/bluetooth/android_bridge.go`** (new, 145 lines) — the `AndroidBridge`
  interface (bind-safe types only: `string`/`int64`/`error`, device lists as JSON
  strings), `SetAndroidBridge`/`RegisteredAndroidBridge` over an
  `sync.RWMutex`-guarded package var, and `androidBridgeClient`, a `Client` that
  forwards to a registered bridge and JSON-decodes into `[]Device`.
- **`internal/bluetooth/dbus.go`** — `newDBusClientFor` gains a third outcome:
  Android + registered bridge → bridge client; Android + no bridge →
  `ErrUnsupportedPlatform`, unchanged.
- **`mobile/mobile.go`** — `SetBluetoothBridge(b bluetooth.AndroidBridge)`, intended
  as the gomobile-bind entry point Kotlin calls in ut-docs#1731.
- Tests for both packages (320 + 47 lines).

No Kotlin, no `android/**`, no `internal/pages/**` — deliberately backend-seam-only.

## Findings

### 1. [BLOCKER] `gomobile bind` silently drops `SetBluetoothBridge` — the seam does not exist in Kotlin

Not inferred from documentation — reproduced. `android/app/build.gradle.kts`'s
`generateAar` task runs `gomobile bind … "./mobile"`, binding **only** the `mobile`
package. I installed `gobind` and ran exactly that package list:

```
gobind -lang=java -outdir=… github.com/universaltill/universal-till/mobile
```

`gobind` **exits 0** and emits, in `java/mobile/Mobile.java` line 33 (and again in
`src/gobind/mobile_android.c`):

```java
// skipped function SetBluetoothBridge with unsupported parameter or return types
```

`start()`, `stop()` and `isRunning()` all bind correctly; only the new function is
dropped. The cause: `gobind` only generates types from packages named on its
command line, and the parameter type `bluetooth.AndroidBridge` lives in
`internal/bluetooth`, which is not one of them.

The consequence is the bad kind of failure — the silent kind. `gomobile bind`
succeeds, the `.aar` builds, `./gradlew assembleDebug` succeeds, every check is
green, and `Mobile.setBluetoothBridge(…)` simply is not in the API. The function's
own doc comment states the precise thing that does not happen:

> This function exists so gomobile bind generates the binding for
> `bluetooth.AndroidBridge` at all

It does not. ut-docs#1731 would begin by discovering the method it is supposed to
call was never generated. ADR-0080 decision #2 as written cannot work, and decision
#1 ("`internal/bluetooth` defines the interface, not `mobile`") is what causes it.

**Two fixes, both verified by me, not proposed on theory:**

- **(B) — recommended.** Declare the bind-safe interface in `mobile` (the *bound*
  package) and keep `bluetooth.AndroidBridge` structurally identical. Go interfaces
  are structural, so `mobile.SetBluetoothBridge(b BluetoothBridge)` can pass `b`
  straight to `bluetooth.SetAndroidBridge(b)` with no adapter and no reflection. I
  built a scratch module with exactly this shape: it compiles, and `gobind` emits

  ```java
  public static native void setBluetoothBridge(BluetoothBridge b);
  ```

  plus a `BluetoothBridge.java` for Kotlin to implement — no extra Java packages,
  nothing else widened. This keeps ADR-0080's "deliberately minimal cross-language
  surface" intent intact; the duplicated 4-method interface is the price, and it is
  the smaller one.

- **(A)** Add `./internal/bluetooth` to the `gomobile bind` argument list in
  `android/app/build.gradle.kts`. Also verified working — produces
  `setBluetoothBridge(bluetooth.AndroidBridge b)` — but it binds the *entire*
  `internal/bluetooth` exported surface into the Kotlin API (`Client`, `Device`,
  all five error sentinels, `NewDBusClient`, `NormalizeAddress`) and emits repeated
  `second result value must be of type error: … NormalizeAddress` warnings. Far
  wider than intended, and it makes an `internal/` package part of the public
  cross-language contract.

Either way **ADR-0080 needs an amendment before this lands** (ADR-0007
document-first, and the ADR is binding).

### 2. [HIGH] No CI gate covers this diff's cross-language bind, and ADR-0080 claims one does

`android-ci.yml` decides whether to do any real work with an in-job `git diff`
filtered by `grep -q '^android/'`. This diff touches only `internal/bluetooth/**`
and `mobile/**` — so `match=false`, and setup-go, setup-java, setup-android, the
NDK install, the gomobile install and `./gradlew assembleDebug` are **all skipped**.

ADR-0080 §3 asserts the opposite:

> `android-ci.yml` already runs `gomobile bind` + `./gradlew assembleDebug` on every
> push touching `android/**` or `mobile/**`, so the cross-language compile is
> genuinely checked in CI without physical hardware

`mobile/**` is not in that filter. This is exactly why finding 1 reached review
undetected: the one job that would have surfaced the skipped binding never ran.
Worth noting `CLAUDE.md` describes the guard the same way the workflow actually
behaves ("when the change doesn't touch `android/**`") — the workflow and the repo
docs agree with each other, and the ADR disagrees with both.

**Fix:** widen the filter to `^(android|mobile)/`. Even that is not sufficient for
finding 1's failure mode, because `gobind` reports a skipped function as a *comment
in generated output* and still exits 0 — a compile gate cannot see it. If the
project wants a real guard, it needs a check that greps the generated binding for
`skipped function` (or asserts the expected method is present); worth a follow-up
card either way, since the same trap will catch the next `mobile`-surface change.

### 3. [MEDIUM] `ctx` deadlines are not enforced across the bridge call

`androidBridgeClient` checks `ctx.Err()` once *before* forwarding, then blocks in a
synchronous Kotlin call it cannot cancel. Every caller in
`internal/pages/bluetooth_devices_page.go` bounds its call with a ctx deadline and
relies on it: `listBluetoothTimeout` 5s, `scanCallTimeout` 25s,
`pairBluetoothTimeout` 45s. On the D-Bus path those are real (`CallWithContext`).
On the bridge path they are decorative. A Kotlin `createBond` that never returns —
device out of range, bond dialog never answered — pins the request goroutine and
the browser request open indefinitely, with no deadline to rescue it.

Compounding it: only `Scan` carries a timeout parameter. `ListDevices`, `Pair` and
`Forget` have no timeout channel at all, and this interface is about to be frozen
as a gomobile-bound API — adding a parameter later is a breaking change to the
Kotlin surface.

Not reachable in any shipped build today (nothing registers a bridge, so Android
still returns `ErrUnsupportedPlatform`), which is why this is not itself a blocker.
But the shape is being frozen *now*, which is the argument for deciding now rather
than in #1731. The conventional fix is to run the bridge call in a goroutine and
`select` on `ctx.Done()`, returning `ctx.Err()` and discarding the late result —
that bounds the *request* even though it cannot cancel Kotlin, at the cost of a
transiently orphaned goroutine.

### 4. [MEDIUM] Bridge errors carry no sentinel, so every Android Bluetooth failure will render as a generic 500

`bluetooth_devices_page.go`'s `apiFail` classifies purely by `errors.Is` against
`ErrUnavailable` / `ErrAccessDenied` / `ErrUnsupportedPlatform` / `ErrNotFound` /
`ErrPairingFailed`. `androidBridgeClient` wraps only the raw Kotlin error and never
a package sentinel, so once #1731 lands:

| Real Android failure | Deserved | Actual |
| --- | --- | --- |
| `BLUETOOTH_SCAN` not granted | 503 `bluetooth_access_denied` | 500 `bluetooth_error` |
| Unknown address | 404 `bluetooth_not_found` | 500 `bluetooth_error` |
| Bond refused / timed out | 409 `bluetooth_pairing_failed` | 500 `bluetooth_error` |
| Adapter off / null | 503 `bluetooth_unavailable` + notice | 500 `bluetooth_error` |

On the GET page the `default:` branch sets `errKey = "bluetoothdevices.list_error"`
instead of the tailored `unavailable` / `accessDenied` notices — the same class of
"technically an error message, actively the wrong one" that ut-docs#1643 existed to
fix in the first place.

The interface gives Kotlin no way to signal *which* class of failure occurred, and
gomobile can only carry error strings. An explicit error-code result, or a
documented string-prefix convention `decodeDevices` maps back to sentinels, is far
cheaper to add now than after Kotlin binds against it. Same freezing argument as
finding 3.

### 5. [LOW] The bridge `Client` does not uphold the documented `Client` contract

Three behaviours the D-Bus implementation provides and this one does not:

- **Sorting.** `Client.ListDevices`'s own doc says "sorted by name"; `devices()` in
  `client.go` sorts (named devices before nameless, then case-insensitive
  alphabetical, then by address). The bridge returns Kotlin's order verbatim, so
  Android device lists render unsorted while Linux renders sorted.
- **`maxCandidates`.** `Scan` on the D-Bus path caps at 64, explicitly because "a
  busy shop floor can see dozens of phones advertising". The bridge path is
  uncapped.
- **Address normalization.** The D-Bus path runs every address through
  `NormalizeAddress`, dropping malformed ones and upper-casing to the canonical form
  `Device.Address` documents. The bridge path does not, so a lowercase or malformed
  address from Kotlin reaches the page as-is.

All three are a few lines in `decodeDevices` (sort, cap, normalize-or-drop), and
fixing them there would make the two backends behaviourally identical from the page
layer's point of view — which is the whole value of having a `Client` interface.

### 6. [LOW] A typed-nil bridge from a Go caller produces a live client that panics

`SetAndroidBridge` stores whatever it is handed. A Go caller passing a typed nil
(`var b *impl; mobile.SetBluetoothBridge(b)`) stores a non-nil interface holding a
nil pointer, so `RegisteredAndroidBridge() != nil` is true, `newDBusClientFor`
hands out a client, and the first method call nil-derefs. Not reachable from Kotlin
(gobind maps Java `null` to a nil interface), so this is genuinely minor — noted
only because the `b != nil` guard reads as though it prevents exactly this outcome
and does not.

### 7. [INFO] `ListDevices` nil-vs-empty asymmetry, and an ADR prose nit

`decodeDevices` returns a non-nil empty slice and
`TestAndroidClient_ListDevicesEmptyIsNonNil` pins it, reasoning that "a nil slice
would marshal as JSON null." The reasoning is sound but the hazard is already
handled: the D-Bus `ListDevices` *does* return a nil slice on empty (`var out
[]Device`), and the page layer compensates with `if devices == nil { devices =
[]bluetooth.Device{} }` at all three call sites. The new behaviour is the better
one and harmless — just be aware the two backends differ here rather than that one
of them needed the guard.

Separately: ADR-0080's prose says the interface uses "string/bool/error" while the
code (correctly) says `string`/`int64`/`error` and uses no bool anywhere. The ADR's
own code block already shows `int64`. Prose nit to sweep with the finding-1
amendment.

### 8. [INFO] Test ordering coupling on package-global state

`dbus_test.go`'s pre-existing `TestNewDBusClientFor_AndroidIsUnsupportedPlatform`
now depends on no other test having leaked a bridge into the package global.
`installBridge` uses `t.Cleanup` so it holds today, and nothing in the package calls
`t.Parallel()`; it passed clean under `-race -count=1`. A one-line
`SetAndroidBridge(nil)` at the top of that test would make it order-independent,
and the new tests would become racy the moment anyone adds `t.Parallel()` here.

## What I verified independently (rather than trusting prior claims)

**The worktree I was handed was at `main` (48c048f), not the feature branch — the
diff was not present.** I checked out `d7687b0` detached and re-derived the diff
myself before reviewing anything.

- **The new tests are not vacuous — proved twice, two ways.**
  - Reverted `internal/bluetooth/dbus.go` to `HEAD~1` (removing only the Android
    bridge branch) and ran the package: **13 top-level tests FAIL**, including
    `TestSetAndroidBridge_NilUnregisters` and every `TestAndroidClient_*`, all with
    `newDBusClientFor("android") … bluetooth: not supported on this platform`.
  - Separately moved `android_bridge.go` out of the tree entirely: the build breaks
    with `mobile/mobile.go:303: undefined: bluetooth.AndroidBridge` and
    `:304: undefined: bluetooth.SetAndroidBridge`.
  - Restored both; `git status` clean, `git diff HEAD` empty, tests green again with
    `-count=1` (not a cached result).
- **`gobind` behaviour, empirically.** Finding 1 and both of its candidate fixes
  were run, not reasoned about. See finding 1 for the exact generated output.
- **Concurrency is correct.** `SetAndroidBridge`/`RegisteredAndroidBridge` lock and
  unlock symmetrically with `defer`, write under `Lock`, read under `RLock`. There
  is **no TOCTOU** between `RegisteredAndroidBridge()` returning a bridge and a
  caller using it after a concurrent `SetAndroidBridge(nil)`: the returned interface
  value is a snapshot copied out under the read lock, not a live re-read, and
  `androidBridgeClient` captures it in a field at construction. A concurrent
  un-register cannot make an in-flight client's bridge disappear — the client keeps
  serving the bridge it was built with for the life of the request, which matches
  the D-Bus client's own "one connection per request" semantics. Confirmed under
  `go test -race`.
- **No import cycle.** `go build ./...` succeeds, which rules a cycle out by
  construction — Go's compiler rejects import cycles outright, so a successful build
  *is* the proof. Stated explicitly rather than skipped. (ADR-0080 also notes
  `mobile` already reaches `internal/bluetooth` transitively via `internal/app` →
  `internal/pages`, so this is not even a new dependency edge.)
- **Error wrapping preserves `errors.Is`.** All four methods use `%w`; I confirmed
  the four `*BridgeErrorIsWrapped` tests genuinely assert `errors.Is(err, boom)`
  against the sentinel the fake returned, and they pass. Wrapping is correct *for
  the error the bridge returns* — finding 4 is about the package's own sentinels,
  which is a different problem.
- **`RegisteredAndroidBridge` is a reasonable addition.** It is read-only, returns
  the interface rather than any means of mutation, is the minimum needed for
  `mobile`'s cross-package test to observe registration, and its doc comment
  explicitly forecloses the misuse ("never a second way to call the bridge"). I
  looked for a leak and did not find one: it exposes nothing a caller could not
  already obtain via `newDBusClientFor("android")`, and it is the natural accessor a
  future "is Android Bluetooth wired?" status surface would want. Keep it.
- **Recurring bug classes checked, not skipped.** Grepped the added lines for
  `os.WriteFile`/`os.Create`/`os.OpenFile`/`os.MkdirAll`/`filepath.Join`/`paths.`:
  **zero hits**. The diff writes nothing to disk and constructs no paths, so both
  the missing-`MkdirAll` and the cwd-relative-path-instead-of-`paths.Data(...)`
  classes are genuinely N/A here.
- **Scope discipline holds.** Changed files are exactly the five named above;
  nothing under `android/**`, `internal/pages/**` or `web/**`.
- **No secrets, no real client/shop names.** Scanned added lines for
  secret/password/token/api-key/private-key/Bearer/AKIA patterns and for long opaque
  literals: zero hits. The only identifiers are documentation-style MACs
  (`AA:BB:CC:DD:EE:01/02`) and "Zebra DS2278", a scanner product model.
- **UX/help-topic scope reasoning confirmed, not assumed.** I traced it rather than
  taking it on trust: the diff registers no route, touches no template and adds no
  locale key, and `newDBusClientFor("android")` still returns `ErrUnsupportedPlatform`
  for every build (nothing registers a bridge), so the shop-owner-visible behaviour
  is byte-for-byte the ut-docs#1643 message it is today. `guard-help-topics.sh` and
  `guard-i18n.sh` both pass unchanged. `reference/ux-guidelines.md` and `web/help/`
  updates are correctly out of scope — and correctly *in* scope for #1731, which is
  the card that first makes the panel do something on Android.

## Checks run

| Check | Result |
| --- | --- |
| `gofmt -l .` | clean (no output) |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test -race -count=1 ./internal/bluetooth/... ./mobile/...` | pass |
| `go test ./...` (full suite) | pass — **no failures at all**, including `internal/pages` (the ut-docs#1725 flake did not trigger, and nothing in this diff could cause it: it touches no page) |
| `golangci-lint run ./internal/bluetooth/... ./mobile/...` | **0 issues** |
| `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./internal/bluetooth/... ./mobile/...` | pass |
| `guard-data-access.sh` | pass — no SQL anywhere in the diff |
| `guard-i18n.sh` | pass — 1453 keys resolve, all locales match, no hardcoded strings |
| `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`, `guard-compliance-claims.sh` | pass |
| `guard-help-topics.sh`, `guard-android-i18n.sh`, `guard-android-status-address.sh`, `guard-htmx-loaded.sh` | pass |
| `gobind -lang=java … ./mobile` | **`skipped function SetBluetoothBridge`** — finding 1 |

## Explicitly deferred

- **The Kotlin side (ut-docs#1731)** — the real
  `BluetoothAdapter`/`BluetoothLeScanner` implementation, the
  `BLUETOOTH_SCAN`/`BLUETOOTH_CONNECT` runtime permission flow (API 31+), the
  `TillService.kt` registration call, and on-device verification. Correctly out of
  scope here; it needs a human at real hardware, which CI cannot substitute for.
  **Findings 3, 4 and 5 should be resolved before #1731 starts**, not during it —
  all three are interface-shape decisions that get expensive once Kotlin binds
  against the generated API.
- **iOS** — ADR-0080 already records that Swift gets no bridge from this decision.
  Unchanged by this review.
- **`internal/pages` flake (ut-docs#1725)** — did not reproduce in this run; tracked
  separately, unrelated to this diff.

## Recommendation

Hold the merge. Finding 1 must be fixed — as it stands the branch adds an entry
point that does not exist in the artifact it was written for, and every gate is
green, so nothing downstream will catch it. The recommended fix (option B: move the
bind-safe interface into `mobile`) is small and verified, but it contradicts
ADR-0080 decision #1, so it needs an ADR amendment first per ADR-0007. Finding 2
should be fixed in the same branch or immediately after, or the next `mobile`-only
change walks into the same trap. Findings 3–5 are best decided now, while the
cross-language surface is still free to change.

## Resolution (orchestrator, 2026-09-07, same day)

All 8 findings addressed on this branch, in the same commit as the original diff
(no second review round — none of the fixes touch money/tax/data-loss/security, the
threshold this pipeline reserves a second round for; each fix was independently
re-verified against this review's own evidence instead):

1. **[BLOCKER] fixed — option B, as recommended.** `mobile.BluetoothBridge`
   declared locally in `mobile/mobile.go`, structurally identical to
   `bluetooth.AndroidBridge`; `SetBluetoothBridge` passes its argument straight
   through with no adapter (Go interface satisfaction is structural). **Verified
   independently, not taken on the fix's own word**: installed `gobind` fresh
   (`go install golang.org/x/mobile/cmd/gobind@latest`) and ran
   `gobind -lang=java -outdir=... ./mobile` myself — it now emits
   `public static native void setBluetoothBridge(BluetoothBridge b);` in
   `Mobile.java` plus a real `BluetoothBridge.java`, and no "skipped function"
   comment appears anywhere in the generated output. ADR-0080 amended in the same
   PR round (ut-docs PR #1732) with a "Corrected during review" section explaining
   why the original design was wrong, not just what changed.
2. **[HIGH] fixed.** `android-ci.yml`'s filter widened to `grep -qE
   '^(android|mobile)/'`; `CLAUDE.md`'s description of the workflow updated to
   match. The residual gap this finding named (a compile-only gate still can't see
   a *silent* `gobind` skip even with the right filter) is real future work, not
   fixable inside this diff — filed as ut-docs#1735 rather than scope-creeping this
   card, per this skill's own triage guidance ("note it, add a Backlog card... if
   it is real future work").
3. **[MEDIUM] fixed.** `bridgeCall[T any]` races the bridge call in its own
   goroutine against `ctx.Done()`; a lost race returns `ctx.Err()` immediately and
   leaves the goroutine to finish and be discarded (documented trade-off: the
   *request* is bounded, the underlying Kotlin call is not, same as `Scan`'s
   existing `StopDiscovery`-with-`WithoutCancel` pattern). New test
   `TestAndroidClient_ContextCancelledMidCallReturnsPromptly` holds a fake bridge
   call open via a real channel (not a sleep) and asserts cancellation unblocks the
   caller within 2s while the fake is still "in Kotlin" — this is a genuine
   mid-call race test, not just the pre-existing before-the-call fast path.
4. **[MEDIUM] fixed.** Four string-prefix tokens
   (`ACCESS_DENIED:`/`UNAVAILABLE:`/`NOT_FOUND:`/`PAIRING_FAILED:`) and
   `classifyBridgeErr`, documented as the contract ut-docs#1731's Kotlin side must
   follow (this is a Go-side contract decision, not a Kotlin implementation — no
   Kotlin code was written here). An unrecognized message still passes through as a
   real error, never silently dropped or force-mapped. New tests cover all four
   mappings, the unrecognized-message passthrough, nil, and that classification
   actually reaches the caller through `ListDevices`'s wrapping (not just
   `classifyBridgeErr` in isolation).
5. **[LOW] fixed.** `decodeDevices` now: runs every decoded address through
   `NormalizeAddress` and drops entries that don't parse (mirrors `devices()`);
   sorts via a new shared `sortDevicesByName` (extracted from `client.go`'s
   `devices()` so both backends use the literal same ordering function, not two
   copies that could drift); applies the `maxCandidates` cap to `Scan` only,
   matching the D-Bus path's own asymmetry (`ListDevices` stays uncapped on both
   backends — the paired-device count is inherently small). New tests:
   sort-order, address-normalize-and-drop, `Scan` capped at `maxCandidates`,
   `ListDevices` explicitly *not* capped.
6. **[LOW] accepted, not fixed.** A typed-nil bridge is unreachable from Kotlin
   (gobind maps Java `null` to a true nil interface) and only a Go caller could
   trigger it. Documented as a known caveat rather than guarded in code — a
   `reflect`-based nil check would add real complexity for a footgun that only
   this package's own tests could pull the trigger on, and they don't.
7. **[INFO] fixed.** ADR-0080's prose corrected from "string/bool/error" to
   "string/int64/error" (the code was already right; only the prose was stale).
   The nil-vs-empty asymmetry itself needs no fix per the review's own read — noted
   as accepted, harmless, and now cross-referenced in `decodeDevices`'s doc comment.
8. **[INFO] fixed.** `TestNewDBusClientFor_AndroidIsUnsupportedPlatform` now calls
   `SetAndroidBridge(nil)` at its own top, removing the implicit ordering
   dependency the review flagged (it passed under `-race -count=1` either way, but
   no longer relies on it).

**Re-verification after fixes** (orchestrator, independent of both the dev
subagent's and the reviewer's own runs): `gofmt -l .` clean; `go build ./...` and
`go vet ./...` clean; `go test -race -count=1 ./internal/bluetooth/... ./mobile/...`
green, including all new tests; full `go test ./...` (49 packages) green with zero
failures (`internal/pages`'s known ut-docs#1725 flake did not trigger);
`golangci-lint run ./internal/bluetooth/... ./mobile/...` 0 issues;
`GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build` clean; `guard-data-access.sh` and
`guard-i18n.sh` pass; `gobind` run directly confirms the fix for finding 1, as
above.

**Final verdict: SAFE TO MERGE.**

## Addendum: CI red after push, fixed before merge

The PR (universal-till#880) pushed clean locally but CI's `desktop-shell` job
failed: `guard-deadcode-baseline.sh`'s whole-program `deadcode` analysis
(roots `.`, `./cmd/unitill-desktop`, `./cmd/unitill-uninstall` — never
`./mobile`) flagged `SetAndroidBridge` as newly unreachable. Root-caused from
the job log, not guessed: `SetAndroidBridge`'s only production caller is
`mobile.SetBluetoothBridge`, and `./mobile` (the gomobile-bind entry point)
is never one of this guard's three analysis roots — it's never linked into
the CLI/desktop build graph at all. Confirmed via
`grep -rn "SetAndroidBridge(" --include="*.go" | grep -v _test.go`: no other
production caller exists. This is exactly the false-positive class the
guard's own error message names ("called only from a [package] this pass
doesn't [build]"). Fixed by baselining the one new entry in
`scripts/ci/deadcode-baseline.txt` (commit `06bd522`), with the reasoning
above in the commit message and as a PR comment. Re-verified clean after the
push: `gofmt`, `go build ./...`, `go test -race -count=1
./internal/bluetooth/... ./mobile/...`, `golangci-lint` (0 issues). All 4
relevant checks (`ci`, `android-ci`, `UI E2E`, `commit-attribution`) passed on
the resulting head (`06bd522`); PR merged (`merge`, not squash/rebase, per
this skill's own note) as `fd56cb9`.
