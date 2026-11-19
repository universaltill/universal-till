# Review: additional-till nightly follow-ups (ut-docs#6552)

**Scope:** branch `fix/6552-replica-update-followups` against `origin/main`
(5ef76287): `internal/pages/update_follow.go` (+ test),
`internal/pages/update_api.go`, `internal/pages/settings_page.go` (+ test),
`web/ui/pages/settings.html`, `web/locales/en.json`,
`web/i18n-meta/en.meta.json`, `web/help/en/updates.md`, regenerated
`web/help/img/` (docs-shots).

**Author:** Dev agent (Sonnet). **Reviewer:** Opus 5.5 (independent, fresh
context). **Tester:** separate agent (revert check, full `internal/pages`,
driven replica run + screenshots). Deferred findings 4 and 7 of
`2026-11-02-fleet-follow-setting.md` are what this card closes.

## What shipped

- **Replica line wording (finding 4).** The old
  `settings.update.follows_main_nightly` printed the raw `auto_time`, but the
  till acts at that time plus its own 0–29 min offset. New key
  `settings.update.follows_main_nightly_window`: "It installs it each night
  between %s and %s, once no sale is open." The two times come from new
  `followNightlyWindow` (`auto_time` + `autoUpdateJitter`, then +30 min,
  modulo 24 h). Old key removed (not in the baseline).
- **Nightly retry (finding 7).** Under `update.fleet_follow = nightly`, the
  in-memory once-per-run guard is replaced by once-per-slot-date:
  `followTick` writes the slot's opening date to `update.auto_last_attempt`
  (per-till, never synced) **before** applying. A failed install is retried
  at the next night's slot, at most one automatic try per night, also across
  a restart. `no_effect` is still never retried automatically; `next_idle`
  and Update now are unchanged (Update now inside the slot spends that
  night's attempt, outside it leaves the next slot free). New followLine
  state `failed_nightly` → `settings.update.follows_main_failed_nightly`
  tells the operator it will try again the next night.
- Help `updates.md` steps 4–5 (English only): exact window shown in
  Settings; next-night retry stated only for the nightly option.

## Findings (Opus 5.5)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Help step 5 implied next-night retry for every policy; the default doesn't retry | Fixed: scoped to "Only at the nightly update time" |
| 2 | minor | Help step 4 said a failed try always needs a manual install | Fixed: notes the nightly retry |
| 3 | minor | "stored before apply" stub check vacuous on night 2; no midnight-crossing test | Fixed: stub asserts the exact slot date per tick, extra night-2 tick asserts no extra apply, new `TestFollowTick_NightlyWindowCrossingMidnightAppliesOnce` |
| 4 | nit | `update.auto_last_attempt` shared with the main/standalone nightly path: a till changing role on the same day skips one night | Accepted: bounded to one night, fails safe (no extra download) |
| 5 | nit | Stale doc comments on `followDecision` / `followLine` | Fixed |
| 6 | nit | `followNightlyWindow` duplicates `autoUpdateSlot` arithmetic | Accepted: both parse with `time.Parse("15:04")` today; small |
| 7 | nit | A Settings negative assertion could never fail | Fixed: removed (the time field legitimately shows the raw time) |

Reviewer probes (scratch copy): 23:50 + 15 min → 00:05–00:35 and 23:50 + 7
min → 23:57 D–00:27 D+1 both apply exactly once per night, no skipped night;
DST fall-back covered by the date guard; no tight loop (date written before
apply on the single scheduler goroutine).

## Verification

- TDD: the reviewer (git-archive scratch copy) and the tester (logic
  reverted, field declarations kept) both saw every new test fail for the
  claimed reason, e.g. `followLine = "failed", want "failed_nightly"`,
  "3 applies on night 1, want exactly 1"; all pass with the diff.
- `go test ./internal/pages/ -count=1` full package: ok (447 s).
  `go test ./internal/... -run 'Follow|Update'`: 87 packages ok.
- gofmt, go vet, guard-i18n, guard-i18n-meta, guard-help-topics,
  guard-help-drift, guard-help-breadcrumbs, guard-help-terms,
  guard-rtl-logical-css, guard-fixed-dates, guard-compliance-claims,
  guard-data-access: pass.
- Driven run: real replica till (`sync.primary_url` set, nightly,
  `auto_time` 02:15): Settings → Software update reads "It installs it each
  night between 02:31 and 03:01, once no sale is open." (offset 16 min,
  checked independently). Screenshots at 1024×600 and 360 px: wraps in the
  card, no clipping, no horizontal scroll. `failed_nightly` (unreachable on a
  Linux till, where self-update shows "manual") rendered via a temporary Go
  test and checked at both sizes. Not checked: de/RTL render, dark theme.

Pre-flight: help topic updated (`updates.md`); docs-shots regenerated; en.meta.json updated (2 new keys, 1 removed); reference/ n/a (no reference page describes the replica line); README n/a (not stale); ADR n/a (fits ADR-0114 §5 and ADR-0178 §7 "next nightly check").

## Deferred

- Tills-page chip still says an update is needed after a nightly failure,
  and `next_idle` failures still retry only after a restart — both
  out of scope by design; listed on ut-docs#6552's close-out.

**Verdict:** approve; minor findings fixed and re-verified.
