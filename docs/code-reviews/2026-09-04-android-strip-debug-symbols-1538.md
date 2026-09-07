# 2026-09-04 — Android: strip Go debug symbols from the embedded library (ut-docs#1538, partial)

## What shipped

`android/app/build.gradle.kts`'s `generateAar` task (the `gomobile bind`
invocation that produces `libgojni.so`) now appends `-s -w` to its existing
`-ldflags` string, alongside the pre-existing `-X ...buildinfo.Version=...`
value injection. `-s`/`-w` strip the Go symbol table and DWARF debug info
from the embedded library.

This is a **deliberately partial** slice of ut-docs#1538 ("Android APK is
142 MB and 95% of it is two copies of the same Go library — split per ABI
and strip symbols"). That issue bundles two independent changes:

1. `-ldflags "-s -w"` symbol stripping — **this PR**.
2. Per-ABI APK splitting (eliminating the duplicate armeabi-v7a/arm64-v8a
   `.so` — the larger of the two wins) — **not this PR**, split out to
   ut-docs#1541 (`complexity:hard`, `blocked:env`).

## Why split instead of building the whole issue

This session has no Android SDK/NDK and no `gomobile`/`gobind` on `PATH`
(confirmed before starting — matches the pre-existing gap noted in this
same file's `generateAar` comment, dated 2026-07-28). That makes:

- Any `android.splits`/product-flavor Gradle config change **unverifiable**
  locally — not even `./gradlew help` reaches past the `generateAar`
  preflight, which hard-fails without `gomobile`/`gobind` on `PATH`.
- The full issue's "a device on each supported ABI installs and updates
  end-to-end" acceptance criterion needs real hardware or an emulator,
  neither available here.
- `.github/workflows/release.yml`'s `android-app` job only runs at an
  actual release cut (`on: release`) — the wrong moment to discover a
  release-workflow mistake, given it's every till's update path.
- A genuine product/UX question (how does a *website* visitor sideloading
  fresh know their device's ABI, when only the in-app self-updater knows
  its own running ABI?) that the full issue's scope raises but this
  session shouldn't decide unilaterally.

The `-s -w` slice has none of those problems: it's a single generic Go
linker flag, fully verifiable outside Android entirely (Go's `-s`/`-w`
strip DWARF/symtab, not `pclntab` — the traceback machinery — regardless of
target OS/arch), and touches nothing release-workflow- or hardware-shaped.

## Independent review

Spawned a fresh-context Sonnet subagent (`complexity:easy` → Sonnet reviews
Sonnet's own work, per the `scrum-master`/`reviewer` skills' model-routing
table) in an isolated worktree, briefed with the diff, the claims to
verify, and told to actually run things rather than just read.

**Findings — 1 comment-accuracy issue (fixed), 1 nit (accepted as-is):**

1. **(fixed)** The new comment originally said "measured 22.5% (14.4 MB)"
   with no attribution, reading as if freshly measured in this session —
   it wasn't (no Android NDK here either). The number is real: it's from
   the issue body's own reported local `gomobile bind` build against NDK
   28.2 (`63,946,288 -> 49,535,368` bytes). Fixed the comment to cite the
   exact byte counts and explicitly note they're the issue author's
   measurement, not re-verified from this session — matching this file's
   own existing honesty convention (the `-target` restriction comment
   above it already discloses "not yet re-measured... no Android SDK/NDK
   available").
2. **(nit, not changed)** "the single biggest remaining lever after the
   per-ABI `-target` restriction above" could be misread as claiming
   per-ABI *APK* splitting is already done — it refers to the earlier,
   unrelated `-target=android/arm64,android/arm` change (dropping
   emulator-only x86/amd64 from the `.aar`; both real-phone ABIs still
   ship in one APK today). Read literally against the surrounding comment
   it's clear, left as-is.

**Independently re-verified, not taken on faith:**

- **Panic traceback survival.** Reviewer built standalone Go programs
  (recovered panic via `debug.Stack()`, and an uncaught panic) twice, with
  and without `-ldflags "-s -w"`, outside Android entirely. `file`/`nm`
  confirmed the strip took effect (unstripped: DWARF present, 2387 `nm`
  symbols; stripped: "stripped", no symbols). Both traceback outputs were
  structurally identical — full function names and `file:line` preserved
  in both the recovered and uncaught paths. I independently reproduced the
  same result myself before writing the code (recovered- and
  uncaught-panic repros, see below), and the reviewer's from-scratch
  re-run reached the same conclusion.
- **`-X` version stamping unaffected.** Reviewer added a `-X main.Version=...`
  flag alongside `-s -w` in one combined string (mirroring this diff's
  exact pattern) — the injected value printed correctly, and `go version -m`
  on the stripped binary still exposed the full `-ldflags` line (buildinfo
  survives `-s -w`, unlike DWARF/symtab). Ran the repo's actual
  `checkAndroidLib` version-extraction `sed` pattern from
  `.github/workflows/release.yml` against that output — it correctly
  extracted the version, confirming the CI version-verification gate
  (ut-docs#1260) keeps working against a stripped `.so`.
- **Gradle Kotlin DSL syntax.** Single string literal, valid `$variable`
  interpolation followed by the literal `" -s -w"` — `gomobile bind`
  shells to `go build`-style flag parsing, which takes `-ldflags` as one
  space-separated string (`go help build`), so appending to the existing
  string (not a second `-ldflags` flag) is correct.
- **Scope honesty.** Confirmed no `android.splits`/product-flavor config
  exists anywhere in the touched file — the per-ABI half of ut-docs#1538
  is genuinely untouched by this diff, and the commit message doesn't
  claim otherwise.

## What I verified myself (before the review, independently of the issue's claim)

Standalone Go repro (`/tmp` scratch, not part of this repo), built both a
recovered panic and an uncaught panic with and without `-ldflags "-s -w"`:
both produced full, readable `goroutine ... / function() / file:line`
tracebacks. Confirms the pclntab/DWARF distinction the code comment now
explains: `-s`/`-w` drop the symbol table and DWARF debug info, not the
separate `pclntab` structure Go's runtime unwinder actually reads.

## Not verified in this cycle (known gap, not a blocker for this slice)

- The real on-device/`gomobile bind` `.so` size reduction — no Android
  SDK/NDK in this session. The issue body's own NDK-28.2 measurement
  (cited above) is the only real-build evidence; worth re-confirming
  against the actual next release's published APK once this ships.
- Everything in ut-docs#1541 (per-ABI splitting, `release.yml` matrix,
  website download-page ABI compatibility) — out of scope here by design.

## Verdict

Safe to merge. `gofmt -l .` clean, `go build ./...` and `go test ./...`
green (this diff touches only `android/app/build.gradle.kts`, no Go
source), both android-specific CI guards
(`guard-android-i18n.sh`, `guard-android-status-address.sh`) pass —
neither touches this file's change, run as part of the standing
before-committing gate regardless.
