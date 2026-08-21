# Code review: enroll.go registration-timing comment reconciliation

**Date**: 2026-08-21
**Card**: ut-docs#619
**Branch**: `docs/619-enroll-comment-adr-status`
**Author**: Sonnet (Scrum Master cloud cycle)
**Reviewer**: independent fresh-context Sonnet subagent (complexity:easy routing)

## What this change does

Companion PR to `ut-docs`'s ADR-0026/ADR-0015 status reconciliation (ut-docs#619,
`docs/619-adr-0026-status`). ut-docs#619 asked to reconcile `internal/enroll/
enroll.go`'s doc comment against ADR-0026's model — either the comment is stale,
or ADR-0026's eager-registration decision was never actually implemented.

Checked against real code: it's the latter. `EnsureRegistered`/`RegisterNow` call
sites (`internal/pages/setup_base_plugins.go`, `plugin_api.go`,
`plugins_store_page.go`, `settings_page.go`, `cloudsync_wire.go`) are all still
on-demand — no boot-time or unconditional-setup-wizard call site exists. The
existing comment ("Registration is LAZY... created on the first real marketplace
interaction") was already accurate; **no code change**. Added a paragraph
explaining *why* it stays lazy (ADR-0015's deliberate 2026-07-17 decision, never
superseded, despite ADR-0026 proposing otherwise 11 days later) and clarifying one
easy-to-misread detail: `setupBasePlugins`' automatic base-plugin install for
DE/ES at setup time does call `EnsureRegistered`, but that's this package's
ordinary "first plugin download/install" trigger firing earlier than a human
browsing the marketplace would — not a deliberate eager-enrolment step, and shops
with no mapped base plugin still register only lazily.

## Independent review findings

The independent reviewer's two confirmed findings were both in the `ut-docs` half
of this change (ADR-0026/0015 wording) — see that repo's
`docs/code-reviews/2026-08-21-adr-0026-status-619.md` for detail. Nothing in this
repo's diff (the enroll.go comment) was flagged; the reviewer independently
verified the DE/ES base-plugin-install claim, the "no other call sites exist"
claim, and the lazy-behavior claim directly against this file and its callers.

## Verification

- `gofmt -l internal/enroll/enroll.go` — clean.
- `go build ./internal/enroll/...` — succeeds.
- `git diff --stat` — 14 insertions, 0 deletions, entirely inside the package doc
  comment. No behavior change; no test changes needed.
- Comment-only change; no secrets, no real client/shop name.

## Outcome

No findings against this repo's diff. Safe to merge.
