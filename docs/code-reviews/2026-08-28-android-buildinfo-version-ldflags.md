# Code review — Android APK shipped `buildinfo.Version=dev` (ut-docs#1260)

- **Date:** 2026-08-28
- **Branch:** `fix/1260-android-version-ldflags`
- **Reviewer:** independent review pass (different model/session from the author)
- **Verdict:** **Safe to merge** — 1 low-severity finding fixed during review, 3 accepted with rationale, 1 disclosed gap.

## What shipped

Two files, no Go source touched.

1. **`android/app/build.gradle.kts`** — the `generateAar` task invokes
   `gomobile bind` to build the embedded Go server as `libgojni.so`, but never
   passed `-ldflags`, so `internal/buildinfo.Version` kept its hardcoded `"dev"`
   default in *every* Android build, signed release APKs included. The fix adds

   ```
   "-ldflags", "-X github.com/universaltill/universal-till/internal/buildinfo.Version=$goVersionForLdflags",
   ```

   reading the same `versionName` Gradle project property that
   `defaultConfig.versionName` already reads and that `release.yml`'s
   `android-app` job already passes as `-PversionName="$VERSION"`. This mirrors
   what `.goreleaser.yaml` has always done for every other platform.

2. **`.github/workflows/release.yml`** — `verify-versions` gains an Android leg:
   the `.apk` is added to the `gh release download` pattern list, `actions/setup-go@v5`
   is added to the job, `android-app` is added to `needs:`, and a new
   `checkAndroidLib` bash helper asserts each ABI's `lib/<abi>/libgojni.so`
   carries the right stamped version.

## Findings

### F1 — `checkAndroidLib` accepted a version that merely *starts with* the release version — **fixed**

*Severity: low. Fixed in this branch.*

The original helper asked whether the expected `-X` flag appeared as a **substring**:

```bash
if ! grep -qF -- "-X …/buildinfo.Version=${VERSION}" <<< "$ldflags"; then
```

A fixed-string substring test also matches any *longer* version sharing that
prefix. Reproduced against a purpose-built synthetic library: a `.so` stamped
`0.6.160`, checked against `VERSION=0.6.16`, **passed** (exit 0). A silent
version mis-stamp is precisely what this gate exists to catch, so a check that
can be satisfied by the wrong version undercuts its own purpose.

Replaced with an exact extraction and comparison:

```bash
local got
got="$(sed -n 's|.*-X github\.com/universaltill/universal-till/internal/buildinfo\.Version=\([^" ]*\).*|\1|p' <<< "$ldflags")"
if [ "$got" != "$VERSION" ]; then
  echo "::error::$label stamps buildinfo.Version='$got', expected '$VERSION': $ldflags"
```

Two secondary benefits: `$VERSION` never reaches a regex (a tag containing regex
metacharacters can no longer change what is matched — the previous code's
`-F` was safe, but a future switch to `-E` would not have been), and the error
message now reports the version actually found instead of only the one expected.

### F2 — the Gradle comment's configuration-order justification was wrong — **fixed (comment only)**

*Severity: cosmetic/documentation. Fixed in this branch.*

The comment claimed `project.findProperty("versionName")` had to be read
independently "because this Exec task is registered before that block is
guaranteed to have evaluated in Gradle's configuration order." That reasoning
does not hold: the `android { }` block sits **above** this line in the same
script and Kotlin build scripts execute top-to-bottom, and `commandLine(...)`
runs later still, inside `tasks.register`'s lazy configuration action. Either
read would work.

The **code is fine and was left as-is** — reading the project property keeps
this `Exec` task independent of AGP's extension model, which is a defensible
choice. Only the justification was rewritten, and the one real consequence is
now documented: the `"0.1.0-dev"` fallback is spelled twice and must be kept in
sync. That drift is observable only by unconfigured local builds (every CI build
passes `-PversionName` explicitly, and `verify-versions` now fails the release on
any mismatch), so de-duplicating it was judged not worth restructuring a
release-critical build file that **cannot be compile-checked in this
environment** (see "Gap" below).

### F3 — `verify-versions` still runs when `android-app` fails — **accepted**

`needs:` gained `android-app` (correct, and necessary — without it the job could
race the APK upload), but the job's `if:` is `!cancelled() && needs.prepare.result == 'success'`,
so it runs even when `android-app` fails. In that case `gh release download`
still succeeds (other patterns match), and the Android step fails at `unzip`
with a shell error rather than the friendlier `::error::missing …` message.

Accepted: this is identical to the job's pre-existing behaviour for `goreleaser`
and `macos-app`, it is deliberate per the comment above the job ("fail loudly
instead of shipping quietly"), and it still fails the release. Changing it would
be a scope increase touching the desktop legs too.

### F4 — `rm -rf extracted` runs after the previous step's `hdiutil detach … || true` — **accepted**

The new step opens with `rm -rf extracted && mkdir extracted` in `dist-check`,
where the *previous* step left a `.dmg` mounted at `extracted` and tolerated a
failed detach (`|| true`). If a detach ever failed, `rm -rf` would hit a
read-only mount and abort the step.

Accepted: fails safe (a read-only DMG mount cannot be deleted, so the outcome is
a loud step failure, never data loss), and it can only trigger on a path where
the preceding step already succeeded.

### F5 — `-ldflags` omits `-s -w` that goreleaser uses — **noted, out of scope**

Every `.goreleaser.yaml` build uses `-s -w -X …`; the Android bind passes only
`-X`. Not a correctness issue and not part of this ticket, but the two `.so`
files in the shipped APK are 61 MB and 74 MB unstripped, and the file's own
comments already flag install size as a live concern (a 180 MB install was
reported on a real device). Worth a separate size-reduction ticket; deliberately
not changed here, since it would alter release artifact contents beyond the
version fix. (Confirmed `-s -w` does not interfere with `go version -m`:
scenario C below built with `-ldflags "-s -w"` and the build-info section was
still readable.)

## What was verified beyond reading the diff

Everything below was re-derived first-hand, not taken from the author's notes.
The `checkAndroidLib` logic was **extracted programmatically from the YAML**
(`yaml.safe_load` → the step's `run:` block written to a file) and executed
verbatim — same variable names, same structure — so the thing tested is
literally the thing that will run in CI.

### 1. The real, currently-published v0.6.15 APK is genuinely unstamped

Downloaded `unitill-pos_0.6.15_android.apk` (138,370,435 bytes, HTTP 200) from
the real GitHub release and unzipped it. `go version -m` (go1.25.0, linux/amd64
host, reading android ELF `.so` files) on both ABIs:

- `path gobind/gobind`, `mod gobind (devel)`
- `dep github.com/universaltill/universal-till v0.0.0-00010101000000-000000000000 => /home/runner/work/universal-till/universal-till (devel)`
- **no `build -ldflags=` line at all**

Root cause confirmed exactly as described: the shipped library records no
`-ldflags`, and the module's own version is the null pseudo-version. Cross-platform
reading of the build-info section works as claimed.

### 2. `strings -a` + exact whole-line match really does fail here

Against the real `lib/arm64-v8a/libgojni.so`:

| test | result |
|---|---|
| `strings -a … \| grep -qx "dev"` | **NO MATCH** |
| substring count of `dev` | 301 occurrences |
| `strings -a … \| grep -qxE "v?0\.6\.15"` | NO MATCH |
| substring count of `0.6.15` | 0 occurrences |

`armeabi-v7a` behaved identically (364 `dev` substrings, 0 `0.6.15`).

The decision not to reuse the desktop `check()` helper is **justified**, and the
`dev` row is the proof: the string is present 301 times yet exact whole-line
matching finds nothing, because adjacent literals merge into one
`strings`-reported line (observed directly, e.g. `JPY?v=dirrtlltrvfs%s.dev"`).
Since `check()` treats "no whole-line `dev`" as *pass*, reusing it would have
produced a check that silently passes on the very artifact it was meant to catch.

**One correction to the author's stated reasoning:** the claim that *both* probes
"fail to find text that IS present as a substring" holds for `dev` but **not** for
`0.6.15` — that string is absent entirely (count 0), which is the correct and
expected result for an unstamped binary, not evidence of the string-merging
problem. The conclusion is unaffected; only that half of the supporting argument
was overstated. The comment as written in `release.yml` ("does not reliably
isolate a short embedded string") is accurate and needed no edit.

### 3. The extracted check fails on the real artifact and passes on a correct one

A synthetic fixture was built to mirror gomobile's actual module topology —
a separate `gobind` main module (`-buildmode=c-shared`, `CGO_ENABLED=1`)
depending via `replace` on a module whose path is the real
`github.com/universaltill/universal-till`, containing `internal/buildinfo` and a
`mobile` package. This also proves the `-X` reaches a **dependency module's
internal package**, which is the situation gomobile creates: an executable built
the same way printed `0.6.16` at runtime, and `go version -m` recorded
`build -ldflags="-X …buildinfo.Version=0.6.16"`.

Seven scenarios against the verbatim-extracted step:

| # | fixture | expected | result |
|---|---|---|---|
| 1 | real shipped v0.6.15 APK, `VERSION=0.6.15` | fail | **exit 1** — "built with no -ldflags at all", both ABIs |
| 2 | synthetic, stamped `0.6.16`, `VERSION=0.6.16` | pass | **exit 0** — "ok: … carries version 0.6.16", both ABIs |
| 3 | stamped `0.1.0-dev`, `VERSION=0.6.16` | fail | **exit 1** — reports the actual stamped value |
| 4 | stamped `0.6.160`, `VERSION=0.6.16` | fail | **exit 0 before F1 fix (false pass)** → **exit 1 after** |
| 5 | `-ldflags "-s -w"` (no `-X`), `VERSION=0.6.16` | fail | **exit 1** — `stamps buildinfo.Version=''` |
| 6 | `-ldflags "-s -w -X …=0.6.16"` (`-X` last) | pass | **exit 0** |
| 7 | `-ldflags "-X …=0.6.16 -s -w"` (`-X` first) | pass | **exit 0** |

Scenarios 5–7 confirm the extraction terminates the value correctly at either a
space or `go version -m`'s closing quote, so the check survives future
additions to the ldflags string.

### 4. `gomobile bind` really does honour `-ldflags`

Verified at source, not assumed, in the module cache — **both** the version the
repo pins (`golang.org/x/mobile v0.0.0-20260709172247-6129f5bee9d5`, per `go.mod`)
and a newer cached one:

- `cmd/gomobile/bind.go` documents `-ldflags` among the build flags `bind` shares with `go build`.
- `cmd/gomobile/bind_androidapp.go:389` calls `goBuildAt(…, "-buildmode=c-shared", …)` — the call that produces `libgojni.so`.
- That routes through `goCmdAt` in `cmd/gomobile/build.go`, which does
  `if buildLdflags != "" { cmd.Args = append(cmd.Args, "-ldflags", buildLdflags) }`
  (line 335/336 in the pinned version).

So the flag reaches the `go build` that produces the shipped `.so`. The flag
name, position (before `-o`/`./mobile`) and single-argv-element value are all
correct for `commandLine(...)`, which executes without a shell — no quoting
concerns.

### 5. Workflow and repo health

- `python3 -c "import yaml; yaml.safe_load(...)"` — **valid**, before and after my edit.
  Parsed job structure confirms `needs: [prepare, goreleaser, macos-app, android-app]`
  and step order (checkout → setup-go → wait → download → dmg checksum → desktop versions → android).
- `actions/setup-go@v5` with `go-version-file: go.mod` (`go 1.25.0`): correct and
  needed — no prior step in this job put `go` on PATH, and the job's other checks
  are pure artifact inspection. Cost is one toolchain setup on `macos-14`; the
  identical step already runs in `android-app`, so it is a known-good pattern here.
- `unzip` on `macos-14`: present, and already relied on by the pre-existing
  desktop leg of this same job for `unitill-pos_*_windows_amd64.zip` — no new
  dependency introduced.
- `-PversionName="$VERSION"` confirmed in the `android-app` job's
  `./gradlew assembleRelease` invocation, so the property the Gradle fix reads is
  the same value `verify-versions` compares against. No new source of truth.
- `gofmt -l .` — clean (no output). `go build ./...` — clean. `go test ./...` —
  clean (no failures). As expected: no Go source is touched.
- Guards run, all **PASS**: `guard-android-status-address`, `guard-android-i18n`,
  `guard-makefile-version`, `guard-i18n`, `guard-compliance-claims`,
  `guard-help-topics`, `guard-data-access`, `guard-kiosk-engine`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`.

### 6. Conventions

- **`web/help/` — not required.** This is build/versioning plumbing. It adds,
  removes and alters nothing a shop owner sees or does: no new page, no new
  route, no changed screen. No help topic pins a literal version string, and the
  manual has no Android screenshot. The user-visible *effect* is that a field the
  manual already describes generically starts showing a correct value instead of
  `dev` — the documented behaviour becomes true, so the prose stays accurate.
- **`README.md` — no claim goes stale.** Its Android claims ("Android app,
  live-verified (ADR-0023)", Android in the platform list) remain accurate and
  say nothing about version reporting.
- **No ADR needed.** Nothing here contradicts or changes an accepted decision;
  ADR-0023 (Android/iOS strategy) is unaffected.
- **No secrets, no real client or shop names** introduced. The only
  secret-shaped token in the diff is `${{ secrets.GITHUB_TOKEN }}`, and it
  appears on a pre-existing context line, not an added one.

## Acceptance criteria (ut-docs#1260)

| # | Criterion | Met | Evidence |
|---|---|---|---|
| a | `-ldflags` wired through `generateAar`'s `gomobile bind` call using the existing `versionName` property | **Yes** | `android/app/build.gradle.kts`: `"-ldflags", "-X …buildinfo.Version=$goVersionForLdflags"` added to `commandLine(...)`, with `goVersionForLdflags` reading `project.findProperty("versionName")` — the same property `release.yml` passes as `-PversionName="$VERSION"`. Flag support and plumbing verified in gomobile's source at the pinned version (§4). |
| b | `verify-versions` extended to check the APK's embedded Go binary carries the real version | **Yes** | APK added to the download patterns; new `checkAndroidLib` step; `setup-go` added; `android-app` added to `needs:`. Demonstrated failing on the real buggy artifact and passing on a correctly-stamped one, across 7 scenarios (§3). |
| c | Verify on a real device | **No — disclosed gap, not blocking** | See below. |

## Explicitly deferred / disclosed gap

**Real-device verification (criterion c) was not performed and is not achievable
in this environment.** There is no physical Android device, and no Android
SDK/NDK or `gomobile`/`gobind` toolchain available — Gradle could not even
resolve the Android Gradle Plugin (`com.android.application:8.7.3` was
unresolvable both offline and through the proxy), so no real `.aar` or APK could
be produced from this fix to install and inspect.

Two concrete consequences, stated plainly:

1. **The Kotlin DSL change could not be compile-checked.** It was reviewed by
   inspection only — comma placement, indentation (12 spaces, matching the
   surrounding `commandLine` arguments), argument ordering, and the
   `$goVersionForLdflags` string template are all correct, and the top-level-`val`
   pattern is already used elsewhere in this same file for the signing config.
   This is also why F2 was fixed as a comment-only change rather than a
   restructuring.
2. **The first real proof will be the next release run.** That is acceptable
   because the new `verify-versions` leg is exactly the mechanism that will
   provide it: if the Gradle change does not take effect, the release fails
   loudly with a named error rather than shipping a `"dev"` APK quietly — which
   is a strictly better position than the status quo, where the bug shipped
   unnoticed on every release to date.

Recommend closing #1260 on (a) and (b), with real-device confirmation folded
into the next release's verification rather than tracked as an open blocker.

Suggested follow-up ticket (not this branch): add `-s -w` to the Android
`-ldflags` and re-measure the `.aar`/APK size (F5).
