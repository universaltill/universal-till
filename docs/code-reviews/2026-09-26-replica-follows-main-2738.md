# Review: an additional till follows the main till's version (ut-docs#2738, slice 1)

## What shipped

- `GET /api/sync/ping` returns `version`. A replica stores it per-till as `sync.main_version`, or takes the link hello's `MainVersion` when linked.
- `selfupdate.ApplyVersion(ctx, v)` installs exactly release v (`/releases/tags/v<v>`) with the existing checksum, backup and rollback. It refuses non-release or not-newer strings before any network call. `Apply` = `ApplyVersion("")`.
- `selfupdate.ApplyVersionWhenIdle(ctx, v, idle)` downloads and verifies, then **waits for no open sale before swapping anything**, and re-checks before stopping plugins and restarting. The replica follow and the #2726 nightly path both use it. The manual "Update now" is unchanged.
- On a replica, `autoUpdateTick` → `followTick` (pure `followDecision`), never the nightly latest path. One attempt per target per run, recorded per-till (`update.follow_attempted`, `update.follow_error`, neither replicates).
- Chip: "Updating to v%s soon" / "Update to v%s needed" (+ hints), and `sync.link.update_waiting` removed. Core locales en/ar/fa/tr; help topic `updates` in en/de/ar/fa/tr.

## Build and review

Dev: Opus subagent (TDD). Orchestrator added the idle gate after the dev's own risk note. Independent review: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| dev note | blocker | The open-sale check ran only before the download, so a sale started during it would be destroyed by the restart, now during trading hours. | **Fixed:** `ApplyVersionWhenIdle`; test `TestFollowTick_RestartGateReadsTheLiveBasket` (fails with a nil gate: "the follow applied without a restart gate"). |
| 1 | should-fix | The chip's 5 s poll called `selfupdate.Supported()` (a temp-file disk probe) on every till. | **Fixed:** probed only when a replica is behind. `TestFollowInputsOf_ProbesSupportedOnlyWhenBehind` failed first ("probed 2 times … want 0"). |
| 2 | should-fix | `web/` was swapped before the held restart, so the old binary served new JS/CSS to the open sale. | **Fixed:** nothing is swapped until idle. `TestApplyVersionWhenIdleSwapsAndRestartsOnlyOnceIdle` failed first ("returned while a sale was open"); a cancelled wait leaves the install untouched (new test). |
| 3 | should-fix | A replica's Settings box reads off while it follows. | Help item 3 now says the main till's choice governs its additional tills (5 languages). The Settings UI change is split to ut-docs#2949. |
| nit 1 | nit | No idle re-check across the 1.5 s re-exec delay. | Fixed: unattended path re-checks idle, then stops plugins. |
| nit 2 | nit | `applyMu` was released during the held restart (a second target could re-swap). | Fixed by construction: the wait happens inside `applyMu`, before the swap. |
| nit 3 | nit | macOS path blocks the scheduler goroutine during the wait. | Accepted: that's the intended shape now on every platform; the scheduler has nothing else to do. |
| nit 4 | nit | `ApplyVersion` on a `dev` build accepts any newer version; `releaseVersionRe` is duplicated. | Accepted: `followDecision` refuses dev builds; the #2945 caller must too (noted on that card). |

The reviewer re-verified three TDD claims by reverting code (restart gate, tag-not-latest, not-newer refusal); each test failed with the expected line.

## Verified beyond tests

- The dev drove a stamped v0.26.0 replica against a fake main till reporting 0.27.0: the chip showed "Updating to v0.27.0 soon", the follow tried GitHub, got a 404 and recorded `failed:release`, and after a restart it retried once, then showed "Update to v0.27.0 needed". Screenshots at 1024×600 in de, fa and en: one line, no overflow, and fa reads right-to-left.
- Real-device follow (Pi5-1 replica, tablet main) is only possible once a release carries this: it goes on the card as the DevOps step.

## Gate

`go build`, `go vet`, full `go test ./...` green. Guards data-access, i18n, help-topics, help-drift and docs-shots (surface hash refreshed; no screenshotted page renders a replica chip) pass.

**Verdict:** safe to merge. Language-pack PRs (de/es) for the 4 new keys follow in the same cycle.
