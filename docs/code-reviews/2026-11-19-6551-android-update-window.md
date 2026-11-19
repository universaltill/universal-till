# Review — Android nightly download window in Settings (ut-docs#6551 slice 3)

Branch of PR universaltill/universal-till#2634 (`git diff origin/main...HEAD`,
commit 0fbf82fe0, plus the uncommitted D1 fix in the working tree), and
ut-docs PR #6731 (`architecture/packaging.md`).

## What shipped
- `internal/pages/settings_page.go`: on a main/standalone till with automatic
  updates on, the page computes this till's nightly window
  (`followNightlyWindow(update.auto_time, autoUpdateJitter)`, 30 min long,
  modulo 24h) as `autoUpdateWindowFrom/To`. D1 (working tree): computed only
  when `eodTimeRe` matches the stored time, the same gate `autoUpdateDue`
  applies, so a legacy `3:05` (which `time.Parse("15:04")` accepts but the
  scheduler ignores) no longer shows a window the tick never opens.
- `web/ui/pages/settings.html`: a muted line under the time input, inside
  the `settings-update-schedule` lock fieldset, only when
  `updateinstallbridge` (Android GOOS) and enabled and a window exists; the
  time input gets `aria-describedby` under the same condition. The whole
  block sits inside `{{ if inappupdateallowed }}`, so store builds never see it.
- `internal/pages/update_api.go`: `POST /api/settings/update-schedule` now
  refuses a non-empty, non-HH:MM time even while disabled (empty still
  allowed), so the shop-wide stored value can't become garbage.
- New key `settings.update.android.download_window` in `en.json` +
  `en.meta.json` only (placeholders described, ADR-0160).
- `web/help/en/updates.md` Android step 2 (new): overnight only downloads,
  window = update time + up to 30 min per till, app must be open on screen,
  install is always a manager's tap; later steps renumbered.
- `web/help/img/manifest.json`: surface hash bump (committed state fresh).
- ut-docs `architecture/packaging.md`: window = `update.auto_time` + 0–29 min
  offset, 30 min long, shown on the Android Settings card; install tap not
  time-gated, refused while a basket or shift close is open.

## Review (independent, Opus 5.5; code authored by Sonnet)
Verdict: **approve-with-fixes**. No blocker or major finding.

1. **minor (CI, must fix before merge)** — `web/help/img/manifest.json`
   `surface_sha256`: the uncommitted D1 edit to
   `internal/pages/settings_page.go:661-665` changes the app surface, so
   `scripts/ci/guard-docs-shots.sh` fails on the working tree (passes on the
   committed HEAD). Non-blocking on the PR (ut-docs#2794) but main's push run
   enforces it. The line is Android-only and docs-shots run on linux, so no
   pixel changes: commit D1 together with
   `scripts/ci/update-docs-shots-surface-hash.sh` and a
   `Docs-Shots-Unchanged: true` trailer. → **open (Dev)**.
2. **nit (help accuracy)** — `web/help/en/updates.md:66` (Android step 2)
   reads as if every Android till downloads overnight. An Android
   *additional* till follows its main till (`followTick`) and never runs the
   nightly prefetch, and its Settings shows no download window (the
   template's `else` branch of `.autoUpdateFollowsMain`). Suggest opening the
   step with "On a main or single till, …". → **open (Dev, one-line)**.
3. **nit (accepted)** — `web/ui/pages/settings.html:599-603`: the form saves
   with `hx-swap="none"`, so after changing the time or unticking Update
   automatically the line still shows the old window until the page is
   reloaded. Same posture as the rest of the card's read-only lines; no card.

Checked and fine:
- D1 gate matches the scheduler exactly (`autoUpdateDue`:
  `eodTimeRe.MatchString(hhmm)`), same `autoUpdateJitter` seam and
  `autoUpdateWindow` constant as `autoUpdateSlot`, so the shown window equals
  the window the tick opens, including across midnight (23:50 + 7 → 23:57–00:27).
- Replica: page uses `tillFollowsMain` (= `SyncPrimaryURL`), tick uses
  `sync.primary_url`; same source. The replica follow window (ut-docs#6552)
  has no `eodTimeRe` gate, but neither does `followTick`'s `autoUpdateSlot`,
  so it is consistent there.
- Security: no change to `POST /api/update/android-install` or the install
  path; the one-tap install keeps its manager/PIN, store-channel and busy
  (409) gates and the ADR-0179 check. The line is display-only, printf'd
  through `html/template` (auto-escaped). The schedule endpoint change only
  narrows what is accepted; `demo_mode` and the shop-wide lock still apply.
- i18n: English + meta only, `%s` placeholders described, no hardcoded
  strings, no duplicate key; muted `<p>` wraps for long locales, no new
  modal, logical CSS untouched.
- `packaging.md`: offset `fnv % 30` min = 0–29 ✓, window 30 min ✓, install
  refused while basket or shift close open (`autoUpdateBusy` incl.
  `shiftCloseInFlight`) ✓.
- Help step 2 "03:00 unless you changed it" = `autoUpdateDefaultTime` ✓;
  "closed / screen off → tries later in the window or next night" = nil
  prefetcher returns before the slot is spent ✓; busy also returns before
  the slot is spent ✓.

## Pre-flight
Pre-flight: help topic `web/help/en/updates.md` updated (finding 2 nit);
`make docs-shots` n/a (line is Android-only, not reachable on linux
docs-shots) but surface hash must be re-bumped with D1 (finding 1);
`en.meta.json` updated (English only, ADR-0160); `reference/` n/a (no
contract change; ut-docs `architecture/packaging.md` updated in #6731);
root `README.md` n/a (no platform claim changed); ADR n/a (ADR-0179 and
ADR-0194 §2.3 untouched); review record this file.

## Verification run by the reviewer
- `go build ./...`, `go vet ./internal/pages/`, `gofmt -l internal/pages/`: clean.
- `go test ./internal/pages/ -count=1` (whole package, with the D1 working
  tree): ok (357 s). `go test ./internal/httpx ./web/...`: ok.
- **D1 TDD re-verified**: reverted the `eodTimeRe` guard in
  `settings_page.go` → `TestSettingsPage_AndroidDownloadWindowHidden/legacy_non-canonical_time`
  failed ("download-window line rendered where it must be hidden");
  restored → passes. (The `25:99` case alone does not exercise D1:
  `followNightlyWindow` already rejects it.)
- Guards: i18n, i18n-meta, help-topics, help-terms, help-breadcrumbs,
  help-drift, fixed-dates, no-native-confirm, rtl-logical-css ok;
  docs-shots ok on HEAD, stale on the working tree (finding 1).

## Not verified
- On a device: the line on a sideload tablet, and an overnight prefetch
  landing inside the shown window.
- Dark theme and non-English line length (no translations yet).
