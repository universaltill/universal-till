# 2026-09-08 — Nightly rolling pre-release workflow (ut-docs#1432)

## What shipped

`.github/workflows/nightly.yml`: a daily (03:00 UTC) + `workflow_dispatch`
workflow that publishes a rolling GitHub **pre-release** tagged `nightly`
(tag force-moved to the latest `main` commit each run, assets replaced),
covering requirement 1 of ut-docs#1432's 7-requirement ask. This exists
because `release.yml`'s tag-triggered workflow was the *only* way to get a
build of `main` onto a test device, so every change under test was
shipping as a real, public, non-prerelease release that every till's
auto-updater (`internal/updates`, `internal/selfupdate`) watches for.

Deliberately does **not** use `goreleaser` (`.goreleaser.yaml`): goreleaser
parses the triggering tag as semver to populate `{{.Version}}`, and a
repeatedly-moved tag literally named `nightly` isn't one. Instead it
mirrors `release.yml`'s own `windows-installer`/`macos-app`/`android-app`
jobs: build directly, `gh release upload --clobber` onto an existing
release.

**Scope of this cut**, per BA/Architect's split (documented on ut-docs#1432
itself): the core `unitill-pos` server binary + web assets for
Linux (`amd64`/`arm64`), Windows (`amd64`), macOS (`arm64`) as plain
`.tar.gz`/`.zip` archives. Explicitly **not** in this PR: Android
(ut-docs#1809 — its version-code formula breaks on a pre-release version
string), the desktop-shell/NSIS-installer/notarized `.app`/`.dmg`/`.deb`
builds, `checksums.txt`, the beta channel (ut-docs#1807), the till's
channel setting (ut-docs#1808), and docs/website links (ut-docs#1810).

## Independent review

Opus, fresh context, isolated worktree (`Agent(isolation: "worktree")`).
**First-draft verdict: BLOCKING ISSUES FOUND (3 blockers + 9 notes).**
All three blockers and every cheap note fixed below; re-verified green
after.

**Blocker 1 (fixed):** the first draft added `internal/buildinfo.Commit`
(short SHA, stamped via a new `-X` ldflag) so a specific nightly build
could be traced back to its commit. The reviewer proved, by building the
real binary twice with the exact same ldflags string and diffing
`strings`/`go version -m` output, that `Commit` was never read anywhere
in the module and the Go linker dead-code-eliminated it — the `-X` stamp
was a **silent no-op**, and the tautological test (`Commit == "unknown"`,
a compile-time constant) passed while the feature did nothing. This is
the exact bug class `scripts/ci/guard-makefile-version.sh` and
`release.yml`'s `verify-versions` job exist to catch for `Version`, landed
fresh for a new field with no equivalent guard. **Fix**: dropped `Commit`
and its test entirely; folded the short SHA directly into
`buildinfo.Version` instead (a field that's read everywhere and therefore
provably not eliminated) — see Blocker 3's fix, same change.

**Blocker 2 (fixed):** the first draft's `prepare` job deleted every asset
on the live `nightly` release, then a *separate*, later `build` job matrix
(no `fail-fast: false`, so one failing leg cancelled the others)
uploaded new ones. A single platform failure could leave the public
nightly release with zero or a partial asset set, silently, for as many
nights as it took someone to notice or push again (the schedule-skip
check would then say "main unchanged" and refuse to retry). **Fix**:
restructured into `check` (compute version, touches nothing) → `build`
(matrix, `fail-fast: false`, uploads to a build *artifact*, not the
release) → `publish` (needs both, runs only if every build leg succeeded
— no `if: always()` — moves the tag, creates/updates the release, uploads
all assets, then prunes anything not in this run's set). Mirrors
`release.yml`'s own `publish-release` job, which undrafts only once every
platform succeeds, for the identical reason (the v0.8.3 incident cited in
that job's own comment).

**Blocker 3 (fixed):** no `concurrency:` guard, so a scheduled run
overlapping a manual `workflow_dispatch` (or two dispatches) could race
the same tag/release — worse, since the version string was date-only, two
same-day runs from different commits produced **identically-named**
assets, so the release could end up with e.g. `linux_amd64` built from one
commit and `windows_amd64` from another, both claiming the same version,
with no way to tell (compounded by Blocker 1 — no live commit field to
disambiguate). **Fix**: added `concurrency: { group: nightly,
cancel-in-progress: false }`, and the version string now includes the
short SHA (see Blocker 1) so two same-day builds are never
indistinguishable even if something still overlaps.

**Note (fixed) — version base caused a real ordering bug, not just a
style nit:** the first draft based the nightly version on a *guessed*
next patch (e.g. `0.12.15-nightly.<date>` when latest stable was
`0.12.14`). `internal/updates.Newer` compares dotted numeric components
positionally; once the real `v0.12.15` shipped, `Newer("0.12.15",
"0.12.15-nightly.<date>")` returned **false** (verified: the reviewer's
own test, reproduced independently here — see below) — a till on that
exact nightly would never be offered the stable release with the same
number, only the one after it. **Fix**: version is now
`<current-stable>-nightly.<date>.<short-sha>`, keeping every numeric
component through the patch identical to a real *older* release, which
`Newer` ranks correctly in both directions. Re-verified with a standalone
test exercising the real `Newer` function: `Newer("0.12.14",
"0.12.14-nightly.20260908.abc123def456")` = false (same base, not yet
superseded), `Newer("0.12.15", ...)` = true (real newer stable shipped),
`Newer("0.12.13", ...)` = false (older stable) — all as expected.

**Note (fixed):** the tag-exclusion pipeline (`grep -vE ... | sed | sort -V
| tail -1`) died under `set -e`+`pipefail` on a repo with zero matching
tags — `grep` exits 1 on no match, killing the step before the
`${LATEST:-0.0.0}` fallback ever applied. Fixed with `{ grep ... ||
true; }`; re-verified against a from-scratch repo (0 tags → `0.0.0`,
accepted) and against this repo's real 24-tag history (→ `0.12.14`,
unchanged from before the fix).

**Note (fixed):** no validation on the computed `LATEST` value — a stray
non-semver `v*` tag (verified with a synthetic `vNext` tag) would have
silently propagated a garbage string into the version, the ldflags, the
release title, and every asset filename. Fixed with the same
`^[0-9]+\.[0-9]+\.[0-9]+$` gate `release.yml`'s own `prepare` job already
uses, failing loudly instead.

**Note (fixed):** the Linux archive was missing `unitill-kiosk-setup.sh`
and `unitill-kiosk-launch.sh` (present in `.goreleaser.yaml`'s real linux
archive) despite the step's own comment claiming full parity minus the
desktop shell. Added both.

**Note (fixed):** dropped the redundant `--target` flag on `gh release
edit` — `target_commitish` is a no-op once the tag already exists (it
does, by that point — moved by the same job moments earlier), so the flag
read as load-bearing without being so.

**Notes accepted as-is (informational, no code change):**
- `gh release view nightly >/dev/null 2>&1` treats any error (including a
  transient API failure) as "release doesn't exist yet" — a real
  precedent for this exact trap already exists in `release.yml`. Residual
  risk here is low and self-correcting: a false negative would try `gh
  release create` against an existing release and fail loudly (the job
  fails; nothing silently corrupts), not misbehave quietly.
- Cannot verify from this sandbox whether a repository ruleset targets
  tags with a broad pattern in a way that would block `git push --force`
  on `refs/tags/nightly` — if one exists, the `publish` job fails loudly
  on that step, which is a visible, diagnosable failure, not silent data
  loss.
- The asset-cleanup loop's `for name in $(gh release view ...)` won't trip
  `set -e` if that command substitution itself fails — worst case is a
  stale asset lingering, not a broken release.
- No injection risk in the workflow: every `${{ }}` expression that lands
  inside a `run:` block is either a fixed GitHub enum (`github.event_name`)
  or a matrix literal defined in the same file; every git-derived value
  is passed through `env:`, never interpolated directly into a script
  string.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean; `go test
  ./internal/buildinfo/... ./internal/updates/... ./internal/selfupdate/...`
  all pass; `guard-data-access.sh` and `guard-i18n.sh` both pass (no SQL
  or user-facing string touched).
- Reviewer independently reproduced the TDD red→green state for the
  (since-reverted) `buildinfo.Commit` test by removing the var and
  confirming the compile failure, then restoring it.
- Every `run:` block in `nightly.yml` validated with `bash -n`; the file
  parses as valid YAML.
- The version-computation logic was extracted and run against three real
  scenarios: this repo's actual 24-tag production history, a from-scratch
  repo with zero tags, and a synthetic repo carrying a stray non-semver
  tag (`vNext`) alongside a real one — all three now produce the correct
  or correctly-rejected result (see notes above).
- The `Newer()` ordering fix was verified with a standalone Go test
  against the real function (not just reasoned about), then removed
  (scratch verification, not a permanent addition — `internal/updates`'s
  own test suite doesn't need a nightly-specific case; the behavior under
  test is a property of `Newer` itself, already covered by its existing
  tests, exercised here only to confirm this workflow's version scheme
  interacts with it correctly).

**Accepted gap, stated plainly:** this sandbox has no `gh` binary and
cannot trigger or watch a live GitHub Actions run, so the workflow's
actual execution against real GitHub infrastructure (tag-push
permissions, `gh release` command behavior against the live API, artifact
download/upload between jobs) is verified by static analysis, documented
API semantics, and this repo's own proven-in-production precedent
(`release.yml`'s identical `gh release upload --clobber` pattern), not by
an end-to-end run. First scheduled/dispatched run in production is the
remaining real-world check.

## Safe to merge

Yes, with the above fixes applied and re-verified. No UI surface touched
(no manual/help topic needed). `RELEASING.md` updated with a new "Nightly
builds" section so the doc doesn't go stale.

## Explicitly deferred (tracked separately)

ut-docs#1807 (beta channel + tag-filtering fix), ut-docs#1808 (till
channel setting + cloud sync), ut-docs#1809 (Android version-code formula
+ Android in the nightly matrix), ut-docs#1810 (docs + website links).
