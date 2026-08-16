# Cross-till end-of-day order list (ut-docs#550)

**Date:** 2026-08-16
**Card:** universaltill/ut-docs#550
**Complexity:** medium
**Repo/area:** `universal-till` — `internal/data/pos_repo.go`, `internal/pages/journal_page.go`,
`internal/ui/journal.go`, `web/ui/partials/journal.html`, `web/public/app.css`,
`web/locales/*.json`, `web/help/{en,ar,fa,tr}/reports.md`

## What shipped

The `/journal` page (receipt/sync history) previously showed only a fixed
recent-sales list with no till provenance and no way to narrow it. Product
owner's ask (#517/#511, a real German prospect): "at the end of the day,
they will look at all sales which happened in all tills from one machine
and they should see which machine got the order."

- `internal/data.POSRepo.ListSalesJournal` — new query supporting a till
  filter (`""` = this till's local sales, `"<id>"` = one specific till,
  `AllTills` = every till) and an optional calendar-day filter, joined
  against `tills` for the till's display name. `ListRecentSales` (still
  used by the sale-screen OOB mini-widget) is refactored to call it
  internally with `AllTills: true` — behavior unchanged, single query
  implementation.
- `/ui/journal` gains `till`/`day` query params driving the filter; a Till
  column shows provenance per row; a staleness line per enrolled till
  ("Last contact from `<till>`: `<timestamp>`", or `—` when unknown).
- Default view (no `till` param) shows every till's sales — matching this
  page's actual pre-existing behavior (see Review, B3) — with "This till"
  now an explicit, opt-in filter choice.
- Architecture: per ADR-0011, only a shop's **primary** till ever
  accumulates other tills' journaled sales (replicas push their own sales
  one-way up, never receive siblings' back down), so this needed no
  primary/replica branching in the query itself — a replica's local
  `sales` table naturally only ever has its own rows. The `tills` roster
  itself *does* sync down to every replica (ut-docs#405), so the filter
  dropdown lists sibling tills there too; selecting one on a replica
  cannot be satisfied locally, handled explicitly (see Review, B2).
- i18n: 7 new/changed keys across `en`/`ar`/`fa`/`tr`; help topic
  (`reports.md`, already covering `/journal`) updated in all 4 locales.

## Independent review (Opus, fresh context)

First pass (against the initial Sonnet implementation) found the
mechanics — SQL, gating, OOB-widget compatibility, i18n, RTL — solid, but
**3 blockers**, verified with real runs (not asserted):

- **B1** — a sale from a since-**revoked** till was mislabelled "This
  till" (template branched on the joined `TillName` being empty, which is
  also true for a genuinely local sale). Fixed: branch on `TillID`
  instead; a non-empty till id with no matching name now shows "Unknown
  till", not a false "This till" attribution.
- **B2** — a code comment claimed the till roster is "always empty on a
  replica, ADR-0011"; false since ut-docs#405 (roster syncs down,
  `bearer_hash`/`last_seen_at` are what's redacted, not the roster
  itself). Consequence: selecting "All tills" or a sibling till on a
  replica silently returned an empty list with no explanation, and a
  redacted `LastSeenAt` rendered as a blank timestamp — both violate the
  ticket's explicit "never silently presenting an incomplete list as
  complete." Fixed: an honest notice ("Cross-till sales are only
  available on this shop's primary till") shown only when the operator
  *explicitly* requests cross-till data on a replica (the bare default
  view stays silent — it's honest by construction, needs no warning);
  blank `LastSeenAt` now renders `—`.
- **B3** — the new default (no `till` param → local-only) was actually a
  **regression**: pre-#550, `/ui/journal` had no till filter at all
  (showed everyone, unlabeled) — same as the sale-screen OOB widget still
  does via unchanged `ListRecentSales`. Narrowing the default silently
  would have hidden sales operators saw yesterday and desynced the two
  surfaces. Fixed: absent `till` param now means "no filter" (all tills,
  matching prior behavior and the widget); "This till" is reachable only
  by explicitly picking it (distinguished via `Query().Has("till")`, not
  `Get("till") == ""`, since both an absent param and an explicit empty
  value parse the same via `Get`).

Also fixed as should-fix, same round: **S2/S3** — "last synced" wording
overstated what `last_seen_at` actually tracks (any authenticated sync
call, not specifically the sales push — a till pinging fine while its
journal push silently fails would have shown a falsely-fresh
timestamp); reworded to "last contact" throughout UI and help docs.
**S5** — the `day` param is now validated (`YYYY-MM-DD` via
`time.Parse`) before reaching SQL; a malformed value is treated as "no
filter" rather than silently matching zero rows. **S6** — an
unrecognized `till` id (stale bookmark, tampered query string) now falls
back to the default (all-tills) instead of silently filtering to a dead
id the `<select>` couldn't even show as selected.

**Explicitly deferred** (noted by the reviewer as real but out of scope
here — tracked on universaltill/ut-docs#774):
- **S1** — `till=all&day=…` on a day with >100 sales silently truncates
  at the existing `limit=full` cap with no "showing N of M" indication.
- **S4** — the day filter buckets by UTC calendar day
  (`date(created_at)`), not shop-local time; confirmed this matches an
  **existing** inconsistency already present elsewhere in this same file
  (`SalesByDepartment`/`SalesByTill` also use bare `date()`; only
  `DayTotal` uses `'localtime'`) — not a regression introduced here, and
  fixing it repo-wide is a separate, bigger change.
- **N1** — no index on `sales.till_id` (fine at pilot scale, the query
  leans on the existing `created_at` index + scan).
- **N2** — the staleness timestamp renders as a raw unformatted/
  unlocalized ISO string.

The reviewer independently re-verified two of the new TDD tests by
reverting each fix's specific logic, confirming a real assertion failure
(not a compile error), then restoring and confirming green again — done
in an isolated `git worktree` (never on the shared orchestrator checkout,
per ut-docs#386). A second, narrower Sonnet fix pass addressed B1/B2/B3/
S2/S3/S5/S6 with new regression tests for each; I (orchestrator/reviewer)
independently re-ran the full gate afterward and re-verified B1/B2/B3
visually with a real driven browser session (see below) rather than
trusting the fix report alone.

## Verified beyond automated tests

- Full gate green: `go build ./...`, `go vet ./...`, `go test ./...`
  (whole repo, not just the touched packages), and all six guard scripts
  (`guard-data-access`, `guard-i18n`, `guard-help-topics`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-compliance-claims`).
- **Real driven browser runs** (throwaway till, Chromium via Playwright,
  not just rendered-HTML-string assertions), before AND after the fix
  round:
  - en (LTR), fa (RTL), ar (RTL) at 1280×900 — filter form, Till column,
    staleness lines all render cleanly, correctly mirrored in RTL, no
    console/page errors, no overlapping/misaligned elements.
  - Default view now shows all tills including a row from a
    since-revoked till, correctly labeled "Unknown till" (not "This
    till") — B1 confirmed visually, not just via test assertion.
  - A specific-till filter and the "This till" filter both narrow
    correctly.
  - Simulated a replica (`sync.primary_url` set, fresh server restart to
    pick it up): default view shows local data only with **no** notice;
    explicitly selecting "All tills" shows the honest replica notice
    banner, styled cleanly, with local rows still listed underneath —
    B2 confirmed visually.
  - Staleness lines show real timestamps for a till with `LastSeenAt`
    and `—` for one without.
- **Not independently checked**: dark theme (no visual regression
  expected — the new markup reuses existing table/form/card styles, no
  new hardcoded colors were introduced), Turkish locale rendered visually
  (translation key-parity was verified programmatically via
  `guard-i18n.sh` and the reviewer read the `tr.json`/`reports.md`
  strings directly, but no browser screenshot was taken in `tr`), and the
  `ut-plugin-language-{de,es}` follow-up for the new `en.json` keys
  (external repos, out of this session's scope — `lang-pack-drift` will
  flag it on `main` if not followed up).

## Safe-to-merge verdict

**Yes**, after the fix round. All blockers resolved and independently
re-verified (both via re-run tests and a fresh driven browser session);
should-fix items addressed; explicitly-deferred items are real but
legitimately out of scope and tracked separately (ut-docs#774).
