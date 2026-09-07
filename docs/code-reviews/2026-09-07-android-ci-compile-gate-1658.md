# android-ci.yml Kotlin compile gate (ut-docs#1658)

## What shipped

`ci.yml` — the check that gates every PR — never compiled the Kotlin/
Gradle project under `android/`. Only `release.yml`'s `android-app` job
did, and that only runs when a release is actually cut, so a Kotlin
compile error under `android/` could merge to `main` clean and only
surface at release time or on a real device. Real precedent: a first
draft of `universal-till#834` overrode `Activity.onLockTaskModeChanged`,
a method that does not exist anywhere in the Android/AndroidX API
surface — a hard compile error only caught because an independent
review happened to hand-verify it against AOSP sources.

Adds `.github/workflows/android-ci.yml`: a `compile` job that runs
`./gradlew assembleDebug` against `android/` (unsigned debug build,
no keystore needed), mirroring `release.yml`'s `android-app` job's
setup (setup-go, setup-java 17, `android-actions/setup-android`,
matching NDK/platform/build-tools versions, gomobile/gobind install) so
the same `generateAar` → Kotlin-compile-against-`.aar` path that job
exercises runs here too. Documented in `CLAUDE.md`'s "Before
committing" section.

### Design detour, found during Dev/Tester on this card, not anticipated at pick-up

The first draft used a top-level `on.pull_request.paths: [android/**]`
filter — the same convention `lang-pack-drift.yml` already uses in this
repo. TDD discipline for this specific class of change ("if I
deliberately broke the thing this claims to verify, would this test
actually fail?") meant proving both directions on a real PR
(universaltill/universal-till#860), not just reading the YAML:

1. A deliberate compile break (`onLockTaskModeChanged`, echoing the real
   `#834` bug) → the gate correctly failed with the exact expected
   error. Confirmed via the GitHub Actions API and job logs, not just
   the green/red badge.
2. Reverting the break and pushing → **the workflow never ran at all.**
   Confirmed via the Actions API: zero workflow runs registered for that
   SHA (not queued, not slow — genuinely absent), even though the pushed
   commit's diff (checked via the GitHub compare API) did touch
   `android/**`. A third push (a trivial comment-only touch) *did*
   trigger correctly, ruling out a config typo — the top-level
   `paths:`-filtered `pull_request` trigger is simply unreliable for
   this workflow, silently dropping roughly half of real synchronize
   events that should have matched.

A gate that silently doesn't run some fraction of the time is worse
than no gate at all — it reads as green because the check never
posted, not because the build passed. Redesigned: the trigger is now
unconditional (`on: push: {branches:[main]}` / `pull_request:`, no
`paths:` — the same shape `ci.yml`/`e2e.yml` use, both of which fired
reliably on all 6 pushes made to this PR during testing), and the
"don't pay the SDK/NDK/gomobile setup cost on an unrelated PR"
requirement is met by a real `git diff` computed *inside* the job
(`Fetch diff base` + `Determine whether this change touches android/**`
steps), gating just the expensive steps behind
`if: steps.filter.outputs.match == 'true'`.

No Go/Kotlin source touched in the final diff (the deliberate-break and
trigger-reliability test commits were all reverted before merge) — the
diff is `.github/workflows/android-ci.yml` (new) + `CLAUDE.md` only. No
ADR needed (a CI-tooling addition following this repo's existing
guard/workflow conventions, not a new cross-cutting architectural
decision).

## Independent review

Sonnet, fresh context (complexity:easy card — this repo's own
"different model" requirement relaxes to "different instance" at this
tier), general-purpose subagent, read-only against the pushed branch
(not a shared/mutating checkout, so no worktree isolation was needed —
nothing in this review reverts tracked files).

**Verdict: safe to merge as-is.** No blocking findings.

Checked, not just read: the diff scope (`git diff origin/main --stat`
— exactly the two files above), YAML validity, the base/diff
edge-case logic in the job-internal filter (all-zero/unreachable
`before` SHA falls toward "run the real check," never toward "silently
skip" — traced through by hand, no false-negative-skip input found),
two-dot vs three-dot diff semantics for the `pull_request` case
(confirmed correct given `actions/checkout@v4`'s default merge-commit
checkout behavior — a conflicted PR fails the checkout step itself,
red not falsely-green), version parity against `release.yml`'s
`android-app` job line-by-line (NDK, platform, build-tools, setup-go/
setup-java/setup-android versions, gomobile/gobind install — all
identical, no drift), and that `assembleDebug`'s `preBuild` dependency
on `generateAar` is unconditional in `android/app/build.gradle.kts`
(so the compile check genuinely exercises the Go→`.aar`→Kotlin path,
not just a partial build).

### Non-blocking notes (not acted on — reviewer's own conclusion, not required before merge)

1. The job-internal filter's fallback branch sets a sentinel string
   (`"android/unknown-cannot-diff"`) that happens to match the `^android/`
   grep, rather than setting `match=true` directly — works, a little
   indirect. Left as-is; a future edit to the grep pattern is the only
   thing that could silently break it, and that edit would need review
   anyway.
2. **`android-ci`'s `compile` check isn't yet in branch protection's
   required status checks** — that's a repo-settings change outside
   this diff's file scope (can't be verified or changed from the diff
   itself, and this pipeline's token doesn't carry repo-admin scope to
   check). Filed as a follow-up (see below) rather than silently assumed
   done, since "the check gating every PR" is the card's actual goal and
   an unrequired check doesn't block anything on its own.
3. No explicit `permissions:` block (defaults apply) — harmless, this
   job uses no secrets/tokens/write access. `release.yml`'s job sets one
   explicitly; a minor consistency-only nit.

## Verified beyond automated tests

- **Both directions of the job-internal filter proven on real pushes**,
  not just reasoned about: a non-`android/**` push completed the
  `compile` check in ~10s with the SDK/NDK/gradle steps never appearing
  in the job log (confirmed via `get_job_logs`, not just the check's
  conclusion); an `android/**`-touching push ran the real ~2-3min
  Gradle build and passed.
- **The gate's negative case proven for real, not asserted**: with the
  deliberate `onLockTaskModeChanged` break present, the check failed
  with `MainActivity.kt:1573:14 Unresolved reference
  'onLockTaskModeChanged'`, `compileDebugKotlin FAILED`, build failed in
  2m22s — read directly from the job log, matching the exact real-world
  bug class (`universal-till#834`) this card exists to catch.
- Trigger reliability re-verified across 6 total pushes to
  universaltill/universal-till#860 after the redesign (2 non-matching,
  1 matching, repeated) — the workflow registered a check-run on every
  single push, no further misses.
- `python3 -c "import yaml; yaml.safe_load(...)"` clean on the final
  workflow file. `gofmt -l .` clean (no Go source in this diff).
- All 7 checks on the final merged commit green: `compile` (android-ci),
  `build` (ci.yml), `e2e`, `authors` (commit-attribution), `playwright`,
  `contract`, `desktop-shell`.
- `git diff origin/main -- android/...` empty on the final commit — the
  verification commits' temporary changes left no net trace in
  `android/` source.

## Safe-to-merge

Yes. No fixes owed from review; the trigger-reliability redesign above
was found and fixed during this same card's Dev/Tester work, before
Reviewer ever saw the diff.

## Explicitly deferred (by design, not oversight)

- Adding `android-ci`'s `compile` check to branch protection's required
  status checks — a repo-admin-scoped settings change, not a file this
  diff can carry. Follow-up card opened on the board
  (see close-out comment on ut-docs#1658 for the link).
- Instrumented tests / a signed release-shaped build for this gate —
  deliberately out of scope per the card's own acceptance criteria; a
  compile-only check is the entire gap being closed.
