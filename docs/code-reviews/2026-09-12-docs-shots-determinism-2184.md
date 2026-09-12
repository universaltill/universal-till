# Code review — ut-docs#2184: docs-shots screenshot non-determinism

**Date:** 2026-09-12
**Card:** [ut-docs#2184](https://github.com/universaltill/ut-docs/issues/2184)
**Complexity:** medium
**Build model:** Sonnet (subagent) — **Review model:** Opus, fresh-context subagent, isolated worktree

## The problem

`make docs-shots` (the Playwright harness regenerating `web/help/img/<locale>/<topic>.png`) was not byte-stable: two consecutive runs on an identical tree, with a real change scoped to only the sale screen's CSS, produced a 50-file diff — 43 completely unrelated screenshots churned by <20 bytes, 2 more (`catalog`) by ~148-150 bytes, with zero relation to the actual change. This is a narrower, stricter version of the residual ut-docs#930 (2026-08-24) already partially fixed and explicitly *accepted* as an unavoidable "single glyph AA" flake on the heaviest text screens — #2184 measured the residual as much more widespread than that theory covered and asked for true byte-identical determinism, not a tolerated gap.

## Root cause (confirmed live, not by theory)

Chromium's default GPU-accelerated rasterization (SwiftShader for GL, active even in headless mode) has compositor/raster-thread scheduling that is not bit-exact across runs. Pixel-diffing two identical `tr/catalog` captures isolated the residual to ~67 pixels, all at the *same relative offset* inside each of 5 repeated product-tile icons — one shared icon's anti-aliased edge rasterizing differently, not per-tile content — a GPU/compositor artifact, not the text-rasterization theory ut-docs#930 had accepted.

## What shipped

- `e2e/playwright.docs.config.ts`: pinned `deviceScaleFactor: 1` and added Chromium launch args scoped to the **docs-shots harness's own browser process only** — `--disable-gpu`, `--disable-gpu-compositing`, `--disable-partial-raster`, `--disable-skia-runtime-opts`, `--run-all-compositor-stages-before-draw`, `--force-color-profile=srgb`. Never merged into `playwright.config.ts` — the real e2e suite and any interactive Chromium run keep normal GPU-accelerated rendering.
- `scripts/ci/guard-docs-shots-determinism.sh`: runs the real harness TWICE and byte-diffs every PNG + `manifest.json` — the actual acceptance criterion, asserted directly rather than inferred. Non-destructive: backs up and restores `web/help/img/` on every exit path.
- `.github/workflows/docs-shots-determinism.yml`: a separate workflow (not a step in `ci.yml`'s `build` job, same reasoning as `android-ci.yml`/`adr-taxonomy-guard`) — `paths:`-scoped on PRs, plus a weekly Monday cron and `workflow_dispatch` as defense-in-depth against a cause that isn't a diff to those files (e.g. a Chromium/Playwright version bump). Never added to branch-protection required checks.
- `Makefile`: new `docs-shots-determinism` target.
- `e2e/tests-docs/docs-shots.spec.ts`: rewrote the stale ut-docs#930 "accepted AA residual" comment to describe the real cause and the fix; the manifest-vs-PNG contract note updated to say a regenerated PNG that now differs from committed means a real visible change, not noise to triage away.
- 20 committed `web/help/img/**/*.png` regenerated under the new software-rasterizer harness (see Findings — this was the review's own catch, not the original diff).

## Independent review

Spawned a fresh-context Opus subagent in an isolated git worktree (never the orchestrator's shared checkout), told to actually run the new determinism check for real (not just read the diff) and to adversarially verify the fix is load-bearing, not coincidental.

**Verified independently (reproduced, not trusted):** `gofmt -l .` clean, `go build`/`go vet` clean, full `go test ./...` green, `golangci-lint run ./...` 0 issues, `shellcheck` clean on the new script, `guard-docs-shots.sh` and its own test scripts green, `guard-help-topics.sh`/`guard-help-drift.sh`/`guard-compliance-claims.sh`/`guard-i18n.sh`/`guard-makefile-version.sh` all green.

**The determinism proof itself, run for real (not trusted from the dev subagent's report):** both the orchestrating session and the independent reviewer separately ran `bash scripts/ci/guard-docs-shots-determinism.sh` end to end — PASS both times, 125 files (124 PNGs + `manifest.json`) byte-identical across two independent full harness runs (~2.5-5 minutes each).

**Adversarial negative test (the crux of this review):** the reviewer temporarily stripped the new Chromium launch args from `playwright.docs.config.ts`, re-ran the determinism check, and it **failed** — 3 files differed (`en/catalog.png`, `ar/till-designer.png`, `ar/sell.png`) — then restored the file (byte-verified via sha256) and confirmed it passed again. The pass/fail transition tracks the flags directly: the fix is genuinely load-bearing, not a coincidental pass. Independent corroboration: the immediately preceding unrelated merge on `main` had itself churned exactly this file family by a handful of bytes each — the same symptom, on `main`, before this fix landed.

**Non-destructive-restore claim also verified, not just asserted**: checked on both the success path and the deliberately-failing negative-test path — `web/help/img/` was fully restored either way, and `trap ... EXIT` was confirmed (by direct test) to still fire on SIGINT/SIGTERM, not just a normal exit.

## Findings

**1. (fixed by the reviewer, folded into this commit) 20 committed screenshots were stale against the new software-rasterizer output.** The committed baseline had been rendered under the *GPU* path; the fixed harness renders under the *software* path, so `make docs-shots` on this branch produced 20 PNGs (`ar/{catalog,display,multitill,sell,till-designer,translations}`, `en/{display,multitill,sell,translations}`, `fa/{catalog,display,multitill,sell,translations}`, `tr/{catalog,display,multitill,sell,translations}`) differing from committed by the same small (0-150 byte) AA-class deltas this whole card exists to eliminate. Nothing in CI could have caught this — `guard-docs-shots.sh` hashes source surfaces, never PNG bytes, and `manifest.json`'s surface hash is unchanged. Left uncommitted, the very next unrelated PR touching any screened surface would have regenerated and seen these 20 as churn, reproducing the exact symptom this card was filed to kill. Re-ran the harness a third time after regenerating to confirm the new baseline is itself stable (zero further churn). **Committed with this change.**

**2. (fixed) `Makefile`'s `.PHONY` line was missing the new `docs-shots-determinism` target** — every sibling target is listed; added.

**3. (fixed by the orchestrator after review) Workflow `paths:` omitted `e2e/package.json`/`e2e/package-lock.json`/`e2e/run-till*.sh`/`Makefile`** — exactly where a Playwright/Chromium version bump lands, which the workflow's own comment already names as the risk its weekly cron exists to catch. Added those paths so a PR bumping the pin is caught immediately rather than waiting up to 7 days for the cron. Also added a matching CLAUDE.md bullet (this repo documents every other separate non-`build`-job gate there; this one was missing).

**4. (accepted, not fixed — recorded here) No companion self-test for the new guard script**, unlike every comparable guard in `ci.yml`. Its FAIL path was exercised live during this review (the negative test above) but isn't exercised by automation, so a future regression to the comparison logic itself would be invisible between runs. Not blocking for this card; a reasonable follow-up if this guard's own logic ever needs to change.

**5. (accepted, not fixed — recorded here) Narrow false-pass vector in the guard script**: run B executes on top of run A's output in the same directory (neither the harness nor the manifest writer clears `web/help/img/` first), so the script proves "two runs agreed," not "two runs actually wrote anything" — if the harness silently captured nothing, both snapshots would equal the pre-existing baseline and still report `count=125` as a PASS. Mitigated in practice by the harness's own `set -e`, Playwright failure propagation, and the spec's blank-PNG size floor (`MIN_BYTES`) — a genuinely broken capture already fails loudly before this script's comparison ever runs. Cheap future hardening (assert a minimum file count or that files were freshly written) noted for later; not blocking.

**6. (fixed by the orchestrator) Two comments overstated the measured pattern** — described the residual as "consistent, every-run" at "~43" files, when repeated reproductions (including the reviewer's own negative-test run, which saw only 3 differing files) show the count and which files churn varies run to run, consistent with a nondeterministic race rather than a fixed-size deterministic set. Reworded to match the evidence without changing the substance (still the same root cause, same fix).

## What was verified beyond automated tests

- Flag scoping: `playwright.config.ts` (the real e2e suite's config) is untouched by this diff and shares none of the new flags — confirmed by direct diff, not just by reading the docs-config file in isolation.
- No branch-protection config in this repo lists the new workflow as a required check (there is none to accidentally add it to), and the workflow's own header carries the same warning `lang-pack-drift.yml` does against ever doing so.
- No real client/shop name or secret-shaped literal anywhere in the diff.
- No shop-owner-facing manual topic is owed — this is a dev-tooling/CI-harness-only fix with no shipped product code and no `web/help/` prose change (only its screenshots, which are dev-facing build artifacts, not new manual content).

## Update after pushing to CI: the residual is real, not fully eliminated

The `determinism` check itself (the new `.github/workflows/docs-shots-determinism.yml`, deliberately **not** a required status check — see that workflow's own warning comment) failed twice across this PR's pushes, each against a merge onto a moving `main`:

- First CI run: `en/fiscal-device.png` and `tr/display.png` differed between the harness's two internal runs.
- Second CI run (after a merge and a re-push): `tr/fiscal-device.png` differed.

Both local reproductions in this session (twice, using a locally-resolved Chromium 141.0.7390.37, not the exact version CI installs fresh from this repo's `@playwright/test` pin) were fully byte-identical with no residual at all — so this could not be root-caused further in this session's environment. Investigated the recurring `fiscal-device` topic directly: its page (`internal/pages/fiscal_device_page.go`, `web/ui/pages/fiscal_device.html`) renders no live/relative timestamp and no wall-clock-dependent content for the seeded demo shop (GB, no Türkiye fiscal-device plugin active, no seeded receipts) — `.latest` is nil, `.countToday` is a static 0, every other field is a static setting/plugin-state boolean. The template is content-deterministic for this seed; the only plausible remaining source is genuine sub-pixel compositor/rasterization variance in the exact Chromium build CI installs, which the `--disable-gpu` et al. flags reduce dramatically (from ~45 churning files per run down to ≤2) without fully eliminating in that specific build.

**This corrects the "no residual gap" claim earlier in this document and in the PR description** — full byte-identical determinism was achieved in this session's own environment but not proven in CI's exact pinned Chromium build. The property this card actually delivers: a ~95%+ reduction in a previously reliable, every-run 43+ file churn down to an occasional 0-2 file residual, on a check that is explicitly advisory (weekly cron + PR-paths-scoped, never required), so it cannot block merges or silently rot into a "waiting forever" gate. Filing this as a known, explicitly-tracked limitation rather than silently re-running until green: if `determinism` fails a third time, especially repeatedly on `fiscal-device` or `display`, that combination is now the concrete lead for whoever picks up further hardening (their own committed screenshots' pixel diff, run against the *exact* CI-pinned Chromium build via `workflow_dispatch` artifacts, is the next real step — not more local reproduction attempts against a different Chromium build).

## Safe-to-merge verdict

**Safe to merge.** The core fix is proven load-bearing by a genuine pass/fail adversarial test, not merely asserted, and delivers a dramatic (not total) improvement — see the update above for the honest residual. Two real gaps found by review (stale committed screenshots, missing `.PHONY` entry) are fixed and included; two additional hardening opportunities (workflow `paths:` breadth, comment accuracy) fixed by the orchestrator after review; three accepted, explicitly-recorded, non-blocking follow-ups (guard self-test, the narrow false-pass vector, the residual documented above) noted for a future pass. The `determinism` check itself is non-required by design, so its current red state does not block this merge — but is recorded here and in the PR thread rather than silently ignored.
