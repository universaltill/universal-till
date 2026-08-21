# Code review: elevation-wired settings forms invisible to non-manager sessions (ut-docs#867)

**Branch:** `feat/867-settings-elevation-visibility`
**Reviewer:** independent Opus subagent, isolated worktree (complexity:hard tier —
see `scrum-master` skill's "Model routing by complexity"; build ran on a Fable
subagent)
**Verdict:** No blockers. 2 non-blocking nits fixed before merge (see below).

## What changed

`web/ui/pages/settings.html` gated many forms behind `{{ if .isManager }}`
(`canPerform(d, r, "settings")`) — the SAME check the ADR-0052 manager-override
elevation mechanism (`checkOrElevate`/`renderElevationPrompt`, `internal/pages/
elevation.go`, #557/#796/#865) exists to soften. A denied session never even
rendered these forms, so the in-place PIN elevation dialog could only ever be
triggered by a hand-crafted POST — never through the shipped UI, for exactly
the users ADR-0052 was built for ("a manager standing next to a blocked
cashier").

Scoping found the real surface bigger than the card's own estimate: 19
`checkOrElevate` call sites across `settings_page.go`, `backup_api.go` and
`eod_api.go`, unevenly spread across 8 `isManager` gates — one gate alone
(670 lines) wrapped ~9 unrelated cards mixing elevation-wired forms with
plain manager-only content. Bumped `complexity:medium` → `complexity:hard`
mid-scoping (see the issue's own comment thread for the full site-by-site
classification table) rather than build a misjudged diff.

**13 sites un-gated** (render unconditionally now; the server-side elevation
dialog is the real authorization layer, unchanged): enrol claim/retry,
display-mode, window-mode, launch-on-startup, payments-default/fee,
backup-now, remove-demo-catalogue, dismiss-restore-prompt,
dismiss-pending-base-plugin, report-retention mode, till-name/register,
idle-lock, kiosk-idle-reset, telemetry, currency, shop-type.

**Left exactly as gated**: everything not elevation-wired (issue-report link,
update card, exit-to-os's own `AuthorizeManager` PIN flow, printer, invoice),
plus every site that displays real manager-only *content* regardless of its
own gating (backup file table, GDPR customer search/erase, catalog cleanup,
data-reset, archives, report-retention coverage + export) — and, as a
**deliberate exception**, the raw `settings.all`/upsert key-value browser,
which technically is elevation-wired but has no concrete cashier-nameable
action and is left gated on purpose (documented inline so a future review
doesn't "fix" it as a missed site).

## Independent review

Opus subagent, isolated git worktree, read-only pass over the diff plus
`internal/pages/*.go`.

**No blockers.** Verified:

- Wrote a nesting-aware Go-template parser to extract every actionable site's
  full enclosing condition stack on both `main` and this branch, diffed gate
  membership, and traced all 19 newly-un-gated endpoints to their
  `mux.HandleFunc` body — confirmed every one reaches `checkOrElevate` before
  any effect, and any pre-elevation code is validation only (never
  disclosure).
- Confirmed every ⛔ site (per the issue's table) is still gated three ways:
  the same parser, a rendered-HTML leak scan, and a mutation test (flipping
  one gate to `{{ if true }}` and confirming the test catches the leak).
- Confirmed the still-gated handlers are genuinely flat-deny
  (`print_api.go`, `invoice_page.go`, `update_api.go`, `data_api.go`,
  `backup_api.go`'s `deny()`), and that `exit-to-os` uses its own
  `AuthorizeManager` PIN flow, not `checkOrElevate`.
- Template balance: both versions parse to a fully closed block stack.
- Manager regression: rendered `/settings` as a manager from both template
  versions, diffed the HTML — identical modulo whitespace. No manager-facing
  regression.
- **Personally re-verified the TDD claim**: reverted `settings.html` alone,
  confirmed both `TestSettingsPage_HidesManagerOnlyCardsFromCashier` and
  `TestSettingsPage_ElevationWiredFormsVisibleToCashier` fail (not vacuously —
  one failure per un-gated marker), restored, confirmed both pass.
- Mutation-tested the manager-only assertions specifically (neutralized the
  backup-table gate) to rule out a vacuous "string never appears" pass — the
  test caught the leak.
- No repository-pattern, i18n, money-type, or RTL violations — no new
  strings, routes, CSS, or SQL.

### Nits (fixed before merge, not re-reviewed — scoped fixes per the
one-review-round default; neither is blocker-class)

1. **Factually wrong "flat-denied" comment.** `/api/reports/archive/export`
   (retention-export) is actually elevation-wired (`eod_api.go:580`), not
   flat-denied — the gating decision was still correct on the table's other
   stated ground (real business content: coverage stats + a sales-report
   download), but the wrong reason could mislead a future reviewer into
   "fixing" it. Corrected the comment in `settings.html` and the matching
   test comment in `settings_page_test.go`.
2. **Empty Data-management card for a cashier with nothing pending.** With
   no demo sample, no pending base plugin and no deferred restore prompt, a
   cashier got a bordered card containing only the "🧹 Data management"
   heading (the card `<div>` itself had no gate at all, only its sub-content
   did). Added `{{ if or .isManager (gt .sampleCount 0) .pendingBasePlugins
   .restorePromptDeferred }}` around the whole card, and a new regression
   test (`TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending`) —
   confirmed red against the pre-fix state, green after.

Not acted on (informational, not defects): the Payments card now shows a
cashier per-provider fee rates (percent + fixed) — correct per the ✅
classification (elevation-wired, and the operator needs to see the field to
know what they're asking a manager to approve), flagged for product-owner
awareness as the one commercially-flavored *value* newly exposed. Hardcoded
English prose markers in the test file's `managerOnly` list — pre-existing
pattern in this test file, not introduced here; a locale-copy-edit follow-up
is out of scope for this card.

## Verification (personally re-run after the review's two nit-fixes, not
just trusted)

- `go build ./...` — clean.
- `go test ./...` (full repo, all 38 packages) — green.
- `go test ./internal/pages/... -count=1` (fresh, no cache) — green, re-run
  after the two nit-fixes.
- `go vet ./...` — clean. `gofmt -l .` — clean.
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh` — all green.
- TDD re-verified personally, twice: once for the original 13-site fix
  (revert/restore), once for the empty-card nit fix (revert/restore).

## Non-goals

No new user-facing string (reusing existing labels), no new route, no
help-topic change — `web/help/en/elevation.md` already describes the
generic "try the action, get a PIN prompt" flow; this change makes that
description true for the settings page rather than changing it. No ADR
(UI-visibility fix over an already-ADR-0052-approved mechanism, zero change
to server-side authorization).
