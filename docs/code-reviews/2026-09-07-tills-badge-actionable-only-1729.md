# ut-docs#1729 — tills nav badge only for states that need approving

Date: 2026-09-07
Branch: `fix/1729-tills-badge-only-on-actionable`
Reviewer: independent subagent, fresh context, different model (Sonnet) —
`complexity:easy` per the scrum-master skill's model routing.

## Report

Product owner, live on the tablet: the nav-rail icon with two arrows
(up/down) that opens the tills page "all the time has an orange dot like it
has a notification on it, but there is no request in the tills. It should
only show that dot when there are some requests to approve."

## Root cause

`web/ui/partials/sync_chip.html`, primary branch:

```gotemplate
{{ if or .quarantined .pending (eq .class "warn") }}<span class="nav-badge" …>
```

and `internal/pages/sync_admin.go` set `class = "warn"` whenever **any**
enrolled till had not been seen in the last 2 minutes. So a satellite that is
switched off — overnight, or a powered-down test device — produced a
permanent dot that no operator action could ever clear. The badge stopped
carrying information the moment it could never turn off.

`class` was doing double duty: the CSS/test state hook *and* the badge
trigger. Those answer different questions — "is the fleet fully healthy?"
versus "is there something a human must action?" — and an offline satellite
answers only the first.

Introduced by ut-docs#1539 (the rail migration that replaced a whole-pill
recolour with a dot); ut-docs#1551 later added `.pending` to the same `or`.
Neither revisited whether `class == "warn"` still belonged once the badge had
become the notification affordance.

## Change

- Badge condition narrowed to `{{ if or .quarantined .pending }}` — a pending
  pairing request or a quarantined entry, both resolvable on `/tills` or
  `/sync-quarantine`.
- Replica branch (`or .offline .queued`) deliberately unchanged: an offline
  replica holding unsent sales is real backlog on that device and clears
  itself once sync drains.

## What the independent review changed

The review returned SAFE TO MERGE with one finding that was **correct and
acted on before merge**, not deferred.

**Finding 1 — the first draft's own comments were false, and the change as
first written made an offline satellite invisible.** The draft claimed the
stale state was "still conveyed by .sync-chip's own warn class, the title and
the aria-label". Verified directly and the review was right on both counts:

- `sync.chip_tills_title` is `"Enrolled tills — open the Tills page"`
  (`web/locales/en.json`) — it has never mentioned offline or stale, in any
  branch.
- `.sync-chip` is a bare wrapper with no styling of its own; `app.css`'s own
  comment says so explicitly. `class="warn"` is a test/JS hook, not a colour.

So the dot really was the **only** signal of any kind for a stale-only
satellite, and removing it would have made a satellite that had been off for
days undetectable from the rail.

Fixed in this change rather than filed as a follow-up, since the regression
would have been introduced by this card:

- `sync_admin.go` now counts stale tills instead of breaking on the first.
- The count is stated as **text** — `· 1 offline` — in the visible label, the
  `title`, and the accessible name, plus a distinct
  `sync.chip_tills_stale_title` for the title/aria when stale. Passive status
  a manager reads, not a notification affordance they cannot dismiss, which
  is exactly the distinction the card asked for.
- New keys `sync.chip_till_offline_one`/`_other` and
  `sync.chip_tills_stale_title` added to all four core locales
  (en/ar/fa/tr), and to `ut-plugin-language-de` / `-es` as the implied
  pack follow-up this lane owns.
- The false comments in the template and the test were corrected to state
  what is actually true.

**Finding 2 — working-tree contamination.** `internal/print/`,
`internal/settings/`, `internal/pages/print_api.go` and `internal/app/app.go`
changes from ut-docs#1728 shared this checkout. Acted on: this commit names
its files explicitly, no `git add -A`, and #1728 goes on its own branch.

**Finding 3 — narrowing verified sound.** `class = "warn"` has exactly three
causes: a stale till, `quarantined > 0`, `len(pending) > 0`. The last two are
already covered directly by `.quarantined`/`.pending`, so the only case the
narrowing drops is the stale-roster-alone case — the intended one, with
nothing else silently lost. The reviewer separately noted a **pre-existing**
latent gap, untouched by this change and present before it: a DB error on the
quarantine count forces `quarantined = 0`, so a real quarantine would fail to
light the badge. Not introduced here; recorded rather than fixed in this card.

**Finding 4 — TDD verified, not taken on trust.** The reviewer reverted the
template to its `main` version in a scratch copy and confirmed both
`TestSyncChip_PrimaryModeStaleTillAloneShowsNoBadge` and the modified
assertion in `TestSyncChip_PrimaryModeWithTills` genuinely FAIL against the
old code. The positive badge assertions for pending pairing
(`sync_admin_test.go:394`) and quarantine (`:447`) are untouched and still
pass, so the fix is a narrowing and not a deletion of the badge.

## Verification

- `go test ./internal/pages/ -run 'TestSyncChip_' -count=1` — all pass.
- `go build ./...` clean; `gofmt` clean.
- Pre-existing unrelated local failure `TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond`
  (ut-docs#1725, green in CI, red locally at `origin/main`) is not touched by
  this change.
- Not yet verified on the real tablet — the device takes its server build
  from a release, so the on-device check happens after this ships.
