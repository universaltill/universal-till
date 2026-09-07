# /my-reports shows the GitHub ticket's state (ut-docs#1652)

**Date:** 2026-09-06
**Branch:** `feat/1652-my-reports-ticket-state`
**Card:** ut-docs#1652 (p2, `complexity:medium`, `lane:local`) — till half of the
umbrella ut-docs#1648, from a product-owner report on the test tablet.
**Pairs with:** ut-cloud#105 (ut-docs#1651), which produces the value this renders.

## Why

`/my-reports`' Status column showed the report's **delivery** lifecycle:
`pending → sent → received → transcribing → ready → filed`. `filed` is
terminal. So from the moment a manager's report became a GitHub ticket — the
moment they start caring what happened to it — the status never moved again.
The owner's words: it *"should show the status of the GitHub ticket."*

## Change

- `006_issue_reports_github_state.sql` — `github_issue_state` on
  `issue_reports_sent`. Additive, for the reason 002–005 all document:
  `verifyAppliedMigrations` hard-fails on a checksum change to `001_init.sql`,
  which would brick every already-migrated device including the pilot.
- `data.SentReport.GithubIssueState`; `UpdateStatus` takes and validates it.
- `cloudsync` decodes `github_issue_state` off the existing status pull.
- `pages.issueReportDisplayStatusKey` picks the ticket state when known and
  falls back to the delivery status otherwise.
- `issuereport.ticket.{open,closed_completed,closed_not_planned}` in
  `en`/`fa`/`tr`/`ar`, plus `ut-plugin-language-de#162` / `-es#161`.
- Help topic updated in all four shipped locales.

## Design decisions worth recording

**Empty is a value, not a gap — and it means "fall back", never "blank".**
Three innocent situations produce an empty ticket state: the report was never
filed, the cloud predates ut-docs#1651 and sends no such field, or the cloud's
refresher hasn't reached the ticket yet. Reading empty as "blank the cell"
would break `/my-reports` for every filed report the moment a till was upgraded
ahead of its cloud. This is the same correct-degradation contract
`pullIssueReportStatusesPage`'s existing `total`-field comment describes.

**Validated twice, deliberately.** The repo drops an unrecognised state before
storing it, and the page guards again on the way out. That is not redundancy:
a till can be *upgraded past* a value already sitting in its own database, so
the write-time check alone cannot protect a later render. Without the page-side
guard, `httpx.T`'s unknown-key fallback renders a raw dotted key at a shop
owner (the exact failure `issueReportStatusKey` was written to prevent).

**"Not planned" stays visibly distinct from "fixed".** GitHub separates them
only by `state_reason`. Collapsing them would tell a shop owner their report
was dealt with when it was declined.

**Still zero network calls.** The value arrives on the existing cloudsync tick
and is read from local SQLite. `/my-reports` renders identically offline, from
last-known state. The till never talks to GitHub — it holds no credential and
is offline-first.

## Review findings (self-review — see "Process deviation")

**1. The page test DB hand-rolls its schema. (found, not fixed here)**
`internal/pages`' `openPagesTestDB` does **not** run
`internal/db/migrations/` — `seedIssueReportsSent` writes its own `CREATE
TABLE`. Adding the column to the real schema failed these tests loudly (good
direction), but the reverse is silent: a column, type, default or constraint
present only in the test copy passes CI and breaks on a device. Pre-existing
and out of scope for this card; filed as **ut-docs#1657**, and the test copy
now carries a comment pointing at it so the next person to hit this doesn't
re-derive it.

**2. The ticket state must not hijack a non-filed row. (covered)**
Nothing writes that combination today, but pending/failing rows' own status is
load-bearing (ut-docs#637 — it is what distinguishes "on its way" from "this
will never arrive"). The fallback structure makes it impossible, and
`...TicketStateNeverOverridesANonFiledRow` pins it.

**3. The link survives.** Knowing the state is not a reason to remove the way
to go and read the ticket; asserted in the render test.

**4. Existing `UpdateStatus` call sites.** The signature gained a parameter;
every existing test call site passes `""`, preserving its original meaning
exactly rather than opportunistically gaining new coverage.

## Verification

- `go test ./...` — full suite.
- New tests: 3 repo (stores it, drops an unknown value while keeping the rest
  of the update, empty is fine), 2 cloudsync (carries the field; an older cloud
  sending no field leaves the delivery status intact), 3 page render (all three
  states replace `filed` and keep the link; unknown/empty falls back; a
  non-filed row is untouched), 1 direct unit test of the selector.
- `guard-i18n.sh` — 1430 keys, all locales match `en.json`.
- `guard-help-topics.sh` — all shipped locales complete.
- `check-lang-pack-drift.sh` — reports the de/es gap and will clear once
  ut-plugin-language-de#162 / -es#161 merge (it checks the packs' pinned remote
  commits, not the local working copies).

## Not verified

- **End-to-end against a real cloud.** ut-cloud#105 is still in CI; nothing has
  yet run the two halves together against a live GitHub App.
- **On a device.** Not checked on the tablet or a Pi.

## Process deviation

The pipeline's Reviewer step calls for an independent different-model subagent
review. This session was configured not to spawn subagents, so the above is a
self-review, recorded as such.
