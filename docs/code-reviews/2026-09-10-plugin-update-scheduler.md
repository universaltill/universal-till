# Background installed-plugin update discovery + language-pack auto-apply

**Card:** ut-docs#1953 — "Installed language packs never update themselves".
The German pilot till was running a ~2-month-old language pack and showing
English menu labels, because plugin-update discovery was pull-only.

**Branch:** `fix/1953-plugin-update-scheduler`
**Base commit reviewed:** `85c3169` (Dev's implementation)
**Reviewer:** independent pass, fresh context, worktree
`.claude/worktrees/agent-a2d2332834a5f83b5`.

## What shipped

- `internal/pages/plugin_update_scheduler.go` (new) — `StartPluginUpdateScheduler`,
  a background goroutine (30s initial delay, then a 15-minute ticker, same
  shape as `StartBasePluginRetry`/`StartAutoUpdateScheduler`) wired into
  `internal/pages/init.go` beside the other background schedulers. Each tick
  runs `plugins.UpdateChecker.CheckForUpdates`, auto-applies every update
  whose `CanonicalType == "language"`, counts everything else as pending,
  and never auto-applies on a replica till.
- `internal/pages/plugin_api.go` — `handleUpdatePlugin`'s install logic
  extracted into a shared `applyPluginUpdate` with sentinel errors; the HTTP
  handler maps them back to the original status codes and messages. Both the
  manual "Update" button and the scheduler now apply an update through the
  identical path.
- `internal/plugins/pending_updates.go` (new) — an `atomic.Value`-backed
  published pending count, mirroring `internal/updates`' `Current()` pattern.
- `internal/plugins/update_checker.go` — `UpdateInfo` gained `CanonicalType`.
- `internal/httpx/httpx.go` + `web/ui/layouts/base.html` + `web/public/app.css`
  — a persistent, non-modal `.sb-plugin-update` status-bar chip linking to
  `/plugins`, styled like the existing `sb-update` pill and added to the
  480px ellipsis-cap selector list.
- `web/locales/{en,ar,fa,tr}.json` — `status.plugin_updates_available`.
- `web/help/{en,de}/plugins.md` — a new step documenting the behaviour.

## Verification performed

Beyond reading the diff:

- **TDD claim re-verified personally, two mutations.** Flipped
  `u.CanonicalType != pluginUpdateCanonicalTypeLanguage` to `==` →
  `TestPluginUpdateCheckTick_AutoAppliesLanguagePacksOnly` failed with a real
  assertion (`expected only the language pack to be auto-applied, got
  [com.test.theme]`). Separately dropped the `isReplica ||` clause →
  `TestPluginUpdateCheckTick_ReplicaNeverAutoApplies` failed
  (`a replica till must never auto-apply a plugin update locally`). Fix
  restored byte-identical after each; both pass again. The Dev's tests are
  real, not false-passes.
- **Offline-first traced to the source.** `CheckForUpdates` calls
  `catalogRepo.Get()`, not `GetOrFetch()` — cache-only, no network on the
  tick. Failures go to `log.Warnf` and return; nothing reaches a response
  writer. The kiosk templates (`web/ui/pages/self_order.html`,
  `self_order_shop.html`) do not include `base.html`'s status bar, so the new
  chip never appears on the customer-facing self-order surface.
- **Ed25519 path confirmed unchanged.** `applyPluginUpdate` still routes
  through `plugins.NewMarketplaceInstaller(...).Install(...)` — the same
  download-token + signature-verified path as install-from-marketplace. The
  scheduler reaches it through the `pluginApplyUpdateFn` seam, not a shortcut.
- **Chip rendered in all four shipped locales** (throwaway render harness):
  correct markup and translated label in `en`/`tr` (`dir="ltr"`) and
  `fa`/`ar` (`dir="rtl"`). CSS uses only logical properties
  (`margin-inline-start`, `max-inline-size`); no `left`/`right`.
- **`make docs-shots` run for real** — 112 screenshot captures across 28
  topics × 4 locales, i.e. a real driven run of the app in every shipped
  locale.
- Full gate green after fixes: `gofmt -l .` clean, `go build ./...`,
  `go vet ./...`, `go test ./...` (whole repo), `golangci-lint run ./...`
  (0 issues), plus `guard-i18n`, `guard-data-access`, `guard-help-topics`,
  `guard-compliance-claims`, `guard-docs-shots` (+ its two self-tests),
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-page-http-error`,
  `guard-emoji-font`, `guard-autofill-suppression`, `guard-makefile-version`,
  `guard-e2e-fixtures-import`, `check-brand-assets`.
- Checked for this pipeline's two recurring bug classes: **no** new file-write
  handler (so no missing `os.MkdirAll`) and **no** cwd-relative path — the
  only filesystem access, `paths.Plugins()` in `applyPluginUpdate`, moved
  verbatim from the old handler. No money values touched. No raw SQL added
  outside `internal/data`. No real client/shop name used as demo data and no
  secret-shaped literal anywhere in the diff.

## Findings

### 1. Discovery matched on a heuristic that does not hold for real marketplace installs — the card was not actually fixed (HIGH, FIXED)

`UpdateChecker.CheckForUpdates` keyed installed plugins to catalog listings
by `installed.Author + "/" + installed.Name` against
`summary.DeveloperID + "/" + summary.Name`. Nothing enforces that those
agree:

- the installed `Author`/`Name` are the plugin's **own manifest.json**
  values — `installer_marketplace.go`'s `upsertCatalogEntry` persists
  `manifest.Author` verbatim and never overwrites it from the listing;
- the catalog side is the **listing's** `developer_id` (which, per
  `client.go`'s `UnmarshalJSON`, falls back to the vendor *display string*)
  and the listing's name.

The source comment admitted as much: *"For now, we'll use developer_id + name
as a simple key / In production, you'd want a proper plugin ID mapping."*
Both pre-existing tests had to hand-patch the installed row
(`UPDATE plugins SET author = 'dev-1'`) to make the match succeed — the smell
that led me here.

This mattered because the new scheduler is built entirely on top of it. I
proved it with a throwaway probe shaped like the real pilot case (manifest
author `Universal Till GmbH`, listing `developer_id` `dev-ut-42`, listing
name differing from the manifest name, joined by a valid install-status
record): `CheckForUpdates` returned **zero** updates. So on the pilot till the
scheduler would discover nothing, auto-apply nothing and show no chip — the
exact ut-docs#1953 symptom, with the fix shipped and CI green.

Worse, it disagreed with the UI: `/plugins` computes its "Update available"
badge from the **install-status listing mapping** (`plugins_page.go`), and
`applyPluginUpdate` resolves the listing the same way. The scheduler was the
one place using the weak matcher.

**Fixed** in `internal/plugins/update_checker.go`: the catalog is now indexed
by listing id as well as by author+name, and each installed plugin is
resolved through the install-status store's listing↔plugin record first,
falling back to the old author+name heuristic only for plugins with no such
record (file imports). Strictly more discovery, no path lost.
`TestCheckForUpdatesMatchesByInstallStatusListing` is the regression test;
I mutation-checked it myself (disabled the listing branch → it fails with
`updates = [], want the listing-matched update`).

### 2. The background tick had no `recover()` — a panic would kill the till, mid-sale (MEDIUM, FIXED)

`pluginUpdateCheckTick` drives `installer.Install` and `d.ReloadPlugins` from
a goroutine with no panic guard. An unrecovered panic in a goroutine takes
down the **whole process**, not just the loop — on a merchant's counter,
during checkout. `syncPullPlugins` (the other background job running that
same install-and-reload path) already carries exactly this guard, with a
comment spelling out the reason. **Fixed**: same `defer recover()` +
error-log-and-retry-next-tick, in `plugin_update_scheduler.go`.

### 3. The chip contradicted the merchant's own action for up to 15 minutes (MEDIUM, FIXED)

The pending count was only ever republished by the 15-minute tick. A merchant
who tapped the chip, landed on `/plugins` and applied the one pending update
kept being shown a green "Plugin updates available (1)" for the rest of the
interval — the status bar insisting on work they had just done. That directly
undercuts acceptance criterion 2.

**Fixed**: `plugins.NotePendingUpdateApplied()` (compare-and-swap, floored at
zero) called from `handleUpdatePlugin`'s success path. CAS rather than
load-modify-store because a scheduler tick can publish concurrently;
`TestNotePendingUpdateAppliedIsRaceSafe` covers that under `-race`.
`SetPendingUpdates` now also clamps negatives. Worst case this under-counts by
one until the next tick — the right direction to be wrong in on a till.

### 4. `guard-docs-shots` — a CI-blocking guard — was red on the branch as committed (MEDIUM, FIXED)

The commit message lists the guards it ran; `guard-docs-shots.sh` is not among
them, and it is in `ci.yml`'s `build` job. It failed on `85c3169` for two
independent reasons: the app surface changed (`base.html`, `app.css`,
`internal/pages/**.go`) and `en/plugins`'s topic markdown changed after the
screenshots were taken. **Fixed** by running `make docs-shots` and committing
the result (`web/help/img/manifest.json`; `web/help/img/ar/sell.png` also
re-rendered with a byte difference — capture nondeterminism, not a content
change). The guard and both its self-tests now pass.

### 5. New help step not translated into ar/fa/tr, widening existing drift (LOW, FIXED)

The commit's claim that `ar`/`fa`/`tr`'s `plugins.md` "already lagged en by
one older item before this change" is **true** — I verified it: before the
change `en` had 8 numbered steps and `ar`/`fa`/`tr` had 7. But the change took
`en`/`de` to 9 and left the other three at 7, doubling the gap, on a card
whose entire subject is non-English merchants being underserved by stale
translations. The repo's own precedent (`82e31d8`, "translate it into
ar/fa/tr/de") is that new manual content ships translated.

**Fixed**: the new step is translated into `ar`, `fa` and `tr`, matching each
file's existing vocabulary (`جهاز البيع` / `صندوق` / `kasa`) and quoting each
locale's own `status.plugin_updates_available` string so the manual names the
chip the merchant actually sees. The *pre-existing* missing item (the
48-hour download-staging step) is deliberately **not** backfilled here — it is
unrelated to this card and belongs in its own change.

### 6. Status-chip template funcs and the chip itself had no test at all (LOW, FIXED)

Every sibling chip (`sb-update`, `sb-power`) has a base-layout render test in
`internal/httpx/template_helpers_test.go`; the new one had none, so nothing
covered the wiring from the scheduler's published count to the rendered pixel.
**Fixed**: added `TestBaseLayoutPluginUpdateChipRendersWhenPending` (asserts a
real `<a href="/plugins">`, the translated label and the count) and
`TestBaseLayoutPluginUpdateChipAbsentWhenNonePending`, both driving the real
`plugins.CurrentPendingUpdates()` state rather than a stubbed func.

### 7. Tests leaked a process-global into the rest of the package (LOW, FIXED)

`TestPluginUpdateCheckTick_NoCatalogRepo_NoOp` parked the global pending count
at 99 and `..._CatalogReadError_...` at 7, neither restored. Harmless today,
but any future `internal/pages` test that renders `base.html` would grow a
phantom status chip and fail for unrelated reasons. **Fixed** with a
`resetPendingUpdatesAfterTest` helper on all five scheduler tests, and the
equivalent in the new `internal/plugins` tests.

## Accepted / not fixed (with reasons)

- **No backoff or circuit-breaker on a permanently-failing listing.** The
  scheduler will retry a broken language-pack auto-apply every 15 minutes
  forever, each attempt a full marketplace download. **Accepted**: it mirrors
  `StartBasePluginRetry`, which retries far more aggressively; the failure is
  logged and counted as pending rather than swallowed; and 15 minutes is a
  negligible load. Adding a bespoke backoff here and nowhere else would make
  the scheduler family less consistent, not more correct.
- **No lock between the scheduler's auto-apply and a concurrent manual update
  of the same plugin.** Two `installer.Install` calls for one plugin id could
  in principle overlap. **Accepted**: the window is narrow (language packs
  only, 15-minute cadence, requires the merchant to press Update in the same
  second), `ReloadPlugins` — the part that actually corrupts shared state if
  interleaved — is already properly serialised under `PluginMu`, and the
  pre-existing background installers (base-plugin retry, replica sync-pull)
  carry no such cross-lock either. Introducing a plugin-install mutex is a
  separate, wider change.
- **`CurrentPendingUpdates()` before the first tick.** Returns the zero value,
  so a freshly booted till shows no chip for its first 30 seconds. This is the
  *safe* direction (silence, not a wrong count) and is now covered by
  `TestBaseLayoutPluginUpdateChipAbsentWhenNonePending`. No change.
- **`.sb-plugin-update` repeats `.sb-update`'s literal hex** rather than
  sharing a selector or a token. **Accepted**: no design token exists for that
  green, `sb-update` and `sb-power` both use literal hex in this same block,
  and the ink/fill pair is the one already reviewed for WCAG AA on
  `sb-update`. Consistent with the file's own precedent.
- **Statusbar width in a long-label locale.** With both `sb-update` and
  `sb-plugin-update` visible in `fa`, the bar could crowd at ~600px. The
  ellipsis cap only applies ≤480px. **Accepted as pre-existing** — the same is
  true of the existing chip pair, and the breakpoint was confirmed live at
  360px in ut-docs#413. Worth a follow-up only if a real till reports it.
- **`README.md` not touched.** Nothing the README claims is made false by this
  change; the plugin-marketplace section describes install, not update
  cadence. Judged not stale.

## Deferred

- **Acceptance criterion 5 — verification on the real pilot tablet — is NOT
  done and cannot be done from this session.** No hardware access. Given
  finding 1 (discovery silently matching nothing on a real marketplace
  install, with green CI), this is the criterion that matters most: the first
  thing to check on the pilot till is that the chip and/or an auto-applied
  language-pack version bump actually appear, and that
  `[PluginUpdateScheduler]` lines show up in its log. Recorded as an explicit
  residual gap on the card, not claimed as met.

## Verdict

**Safe to merge**, after the six fixes above. The design is sound and fits the
repo's existing background-scheduler, status-chip and verified-install
patterns; the Dev's tests are genuine (mutation-verified twice by me). The
serious issue was finding 1 — the feature would have shipped, passed CI and
changed nothing on the very till it was written for — and it is fixed with its
own mutation-verified regression test. Merge only after CI confirms green;
close the card only after the pilot-tablet check above.
