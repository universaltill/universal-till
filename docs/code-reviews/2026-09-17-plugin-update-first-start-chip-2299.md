# Code review — plugin-update scheduler first-start trigger + language-pack chip (ut-docs#2299)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2299 (`complexity:medium`, `infrastructure`), split from
  the ut-docs#2296 epic (German UI falling back to English while a language
  pack lagged core).
- **Branch:** `feat/2299-plugin-update-first-start-chip`
- **Reviewer:** independent pass, fresh-context Opus subagent working in an
  isolated worktree (per this card's `complexity:medium` routing — never saw
  the implementation reasoning).
- **Verdict: SAFE TO MERGE** after one fix applied in response to the
  review's own finding (see below).

## What the card actually needed

ut-docs#2299 asked to confirm what `StartPluginUpdateScheduler`
(`internal/pages/plugin_update_scheduler.go`, ut-docs#1953) triggers on
today — the parent epic assumed it might be nightly-only — and, if it
doesn't already react to a core version change, add a trigger that does.

**Finding: the premise was already false.** `StartPluginUpdateScheduler`
ticks ~30s after every process start and every 15 minutes thereafter, with
no core-version gating anywhere in `plugins.UpdateChecker.CheckForUpdates`.
`selfupdate.Apply` (`internal/selfupdate/selfupdate.go`) re-execs the binary
after a core self-update, and that re-exec is an ordinary process start —
`app.Run`/`init.go` wire the scheduler unconditionally on every boot. So a
pack update is already found within ~30s-15min of any restart, self-update
included; there was never a separate "wait for tonight" path to remove.

What genuinely was missing, closing the card's three acceptance criteria:

1. **No test proved the "first start" timing** — only `pluginUpdateCheckTick`
   itself (the per-tick logic) had coverage; `StartPluginUpdateScheduler`'s
   goroutine/timer behavior had none.
2. **The status chip couldn't say "language pack"** — a language pack
   normally auto-applies silently, so the existing generic "Plugin updates
   available (N)" chip never covers the one case that actually matters for
   a merchant staring at English fallback text: a joined till, which never
   applies a language pack locally (ut-docs#460) and waits for the main
   till, or a rare failed auto-apply.

## What shipped

- `internal/pages/plugin_update_scheduler.go`: `pluginUpdateCheckInitialDelay`/
  `pluginUpdateCheckInterval` turned into vars, and a new `pluginUpdateTickFn`
  seam (same shape as the existing `pluginApplyUpdateFn`), so a test can
  observe scheduler timing without a real DB/catalog. `pluginUpdateCheckTick`
  now tracks a `languagePending` bool alongside the existing `pending` count
  and calls `plugins.SetPendingUpdates(pending, languagePending)`.
- `internal/plugins/pending_updates.go`: `PendingUpdateStatus` gains a
  `LanguagePending` field. `SetPendingUpdates` takes a second bool param
  (clamped to `false` whenever `count` clamps to 0, so the two fields can
  never disagree about there being anything pending). `NotePendingUpdateApplied`
  preserves `LanguagePending` across a decrement that leaves `Count > 0`,
  clears it at 0.
- `internal/httpx/httpx.go`: new `languagepackupdateavailable` template func.
- `web/ui/layouts/base.html`: a new `.sb-language-pack-update` status-bar
  chip, separate from the existing `.sb-plugin-update` (both can be true at
  once), using the new `status.language_pack_update_available` i18n key.
- `web/locales/{en,ar,fa,tr}.json`: the new key, translated consistently
  with the sibling `status.plugin_updates_available` key's register in each
  locale.
- `web/help/{en,de,fa,ar,tr}/plugins.md`: one added sentence to the existing
  numbered item describing the background update check, covering the new
  chip — no new heading/step/bullet, so `guard-help-drift.sh`'s structural
  baseline is unaffected.
- New/updated tests: `TestStartPluginUpdateScheduler_RunsOnFirstStart`
  (proves the tick fires within the initial delay, not a full interval);
  `TestNotePendingUpdateAppliedPreservesLanguagePendingUntilZero`,
  `TestSetPendingUpdatesClampsLanguagePendingAtZeroCount`; existing
  `TestPluginUpdateCheckTick_*` tests extended to assert `LanguagePending`;
  `TestBaseLayoutLanguagePackUpdateChipRendersWhenLanguagePending` +
  its absent-case counterpart.

## What the independent review found

Ran the full gate itself: `gofmt -l .` clean, `go build ./...` clean,
`go test ./... -count=1` full suite green, `golangci-lint run` (touched
packages) 0 issues, `guard-i18n.sh`/`guard-help-topics.sh`/
`guard-help-drift.sh`/`guard-docs-shots.sh`/`guard-compliance-claims.sh`/
`guard-data-access.sh` all pass.

**TDD re-verification, done independently by the reviewer**: deleted the
`pluginUpdateTickFn(ctx, d)` call before the ticker loop — the new
first-start test failed with exactly the predicted message ("no tick
within 500ms of start"); restored, passes again. Separately disabled the
replica `languagePending = true` branch — `TestPluginUpdateCheckTick_ReplicaNeverAutoApplies`
failed with "LanguagePending = false, want true" while the sibling
auto-apply test stayed green (proving the two assertions are independent);
restored, passes again. Confirmed the new timing test's bounds are safe
against flakiness (20ms delay vs. 500ms wait, 25x margin; Go tickers only
ever fire late, never early, so the "no second tick" check's risky
direction is unreachable) — 30 runs under `-race -cpu=1` all green.

**One real finding, MEDIUM: the new chip shipped with zero CSS.**
`.sb-language-pack-update` had no rule in `web/public/app.css` at all — it
inherited only the base `.sb-item` style (no green pill, no separator
margin) and was missing from the `@media (max-width: 480px)` ellipsis-cap
rule that ut-docs#413 added specifically because the status bar's longest,
most locale-length-sensitive string overflows a phone viewport at 360px.
"Language pack update available" is longer than the existing
"Plugin updates available" string that rule was written for, so this would
have silently reintroduced the exact bug ut-docs#413 fixed and documented
at length. **Fixed in a follow-up commit** (`9d19890d`): added the matching
pill styling (identical treatment to `.sb-plugin-update`, its own class so
both chips can render together) and added `.sb-language-pack-update` to the
480px ellipsis-cap selector list. Re-ran `make docs-shots` after the CSS
fix to confirm empirically (not just by reasoning) that no screenshot pixel
actually changed — the chip is hidden by default (`LanguagePending` false)
in every demo/docs-shots fixture, so only the surface hash in
`web/help/img/manifest.json` moved; verified this with a real diff, not an
assumption.

Two non-blocking notes recorded, not fixed (both explicitly deferred, per
the "several honestly-scoped commits beat one commit claiming more than it
verified" standing rule — neither is this card's stated scope):

- `NotePendingUpdateApplied` preserves `LanguagePending` across a decrement
  that leaves `Count > 0`, even if the specific plugin just manually applied
  was the language pack itself (the caller doesn't pass which plugin was
  applied) — self-healing within one scheduler tick (≤15 min), explicitly
  tested as the intended tradeoff (`TestNotePendingUpdateAppliedPreservesLanguagePendingUntilZero`).
  Worth a future card if a merchant ever reports the stale window as
  confusing in practice.
- `status.language_pack_update_available` was inserted next to its sibling
  key rather than in strict alphabetical order in all four locale files —
  not CI-enforced, consistent across all four files, cosmetic only.

**Non-findings checked and cleared**: the `web/help/img/manifest.json` key
reordering matches the generator's own key order (`e2e/tests-docs/lib.js`),
not a hand-edit; `de` is legitimately absent from that manifest (it only
tracks en/fa/ar/tr); translations were checked for register consistency
against sibling keys in each locale and found accurate; no money or
repository-pattern surface touched; backend change surface confirmed
(`internal/pages`, `internal/plugins`, `internal/httpx`) plus the
UI/i18n/help surfaces the card's own acceptance criteria call for.

## Merge status

**Merged** — CI green on the branch head after both commits, no open
Admin Review question applies to this card (ut-docs#2277's auto-push
question was resolved by the product owner earlier this cycle: "no real
users yet," auto-push stands).
