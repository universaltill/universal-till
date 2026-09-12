# Review: compact status/lock/exit affordance in `record-dialog` (ut-docs#2099)

**Date:** 2026-09-11
**Implementer:** Sonnet (Dev subagent), `complexity:medium`
**Reviewer:** Opus (independent subagent, isolated worktree)

## What shipped

A full-screen `<dialog>` built on the `record-dialog` shared partial
(`web/ui/partials/record_dialog.html`) covers the nav rail outright, hiding
the till's existing lock/sync-status/exit-to-OS controls. `coding-standards.md`
§10 (the ut-docs#1999 decision) requires those three to stay reachable on
every surface except self-order kiosk mode, where they must be deliberately
unreachable (customer containment).

This change adds a compact, persistent affordance row
(`.record-dialog-status-row`) inside the dialog's still-pinned head, built
once in the shared partial so `/categories` and every future ut-docs#2012
screen inherit it with no per-screen work:

- **Lock** — reuses `session_chip.html`'s exact `POST /api/auth/logout` form.
- **Status** — a second instance of the footer's `#sb-conn` online/offline
  chip, own element id, same detection logic.
- **Exit-to-OS** — a link into Settings' Display card, not a duplicate of
  the PIN-gated `#exit-to-os-form` it contains.

Withheld genuinely (not CSS-hidden) via a new `httpx.InitSelfOrderMode`/
`selforder` template func, mirroring `InitKiosk`/`InitOSKMode`'s existing
boot+live-update shape, gated on the per-till `display.mode="self_order"`
setting (ADR-0020).

## Independent review — findings

One blocking finding, fixed before merge; six non-blocking, two fixed,
two filed as follow-up cards, two accepted as-is.

### Blocking (fixed)

**B1 — `newRederiveSettings` never republished `InitSelfOrderMode`.**
`internal/pages/init.go`'s shared re-derive (used by the replica-drift loop
and cloud `set_setting` directives, ADR-0018) refreshes currency, idle-lock,
translation overrides, and the live window-mode channel — but `display.mode`
is deliberately not part of `RuntimeState` (see the boot-time comment in
`Init`), so it got no free ride from `*s = st` and was never re-read here.
Concrete failure: a chain operator remotely flipping a pilot till to kiosk
mode via `set_setting display.mode=self_order` left the till still rendering
lock/status/exit-to-OS in every dialog until the process restarted — exactly
the containment §10 exists for. The inverse (a till taken out of kiosk mode
via the same path) wrongly kept withholding the controls on an ordinary
register.

Fix: `newRederiveSettings` now reads `display.mode` from the settings store
directly (same shape as `pages.Init`'s own boot read) and calls
`httpx.InitSelfOrderMode` each time it runs. Regression test added:
`TestRederiveSettings_PublishesSelfOrderMode` in
`internal/pages/rederive_settings_test.go`, mirroring the existing
`TestRederiveSettings_PushesWindowModeOntoShellChannel` (ut-docs#1039 finding
9) shape — asserts both directions (into and out of kiosk mode) against the
live `selforder` template func. TDD re-verified: reverting only the
`init.go` fix (keeping the new test) fails with the exact claimed reason
("selforder template func = false after rederive with
display.mode=self_order …"); restoring the fix passes again.

### Non-blocking, fixed

- **N1 (accessibility).** `.sb-conn.is-offline`'s `#fca5a5` color was tuned
  for the statusbar's dark `#0f172a` ground — roughly 1.9:1 against the
  dialog's white `--surface`, a WCAG 1.4.3 failure on the offline state an
  offline-first till most needs legible. Fixed with a scoped override
  (`.record-dialog-status-row .sb-conn.is-offline { color: var(--danger) }`,
  ~4.8:1) rather than a new literal.
- **N4 (nit, folded into the same edit).** One physical `min-height` in the
  new CSS block switched to logical `min-block-size` for consistency with
  the rest of the block (RTL-neutral either way; consistency only).

### Non-blocking, filed as follow-ups (not fixed in this PR)

- **ut-docs#2121** — `POST /api/settings/upsert`'s generic key/value path
  can also set `display.mode` without publishing `InitSelfOrderMode` (same
  bug class as B1, different handler) and separately already skips
  ut-docs#1259's session-revoke-on-sensitive-change behavior. Pre-existing,
  not introduced by this diff; real, worth its own card.
- **ut-docs#2122** — `record-dialog.js`'s `bindStatusRow` leaks a `window`
  online/offline listener per htmx swap (no removal on dialog replacement).
  Low impact; accumulates over a long till session.

### Non-blocking, accepted as-is

- **N5 was investigated and found not to apply** — the reviewer's read of
  `ut-docs/reference/list-and-dialog-pattern.md` was stale; the doc already
  carried the measured-height-cost figure and an accurate Follow-ups entry
  before this review. Verified directly rather than dismissed on the
  reviewer's say-so.
- **N6 (kiosk-absence coverage).** Pinned at the Go level
  (`TestCategoriesPage_RecordDialogStatusLockExitAffordance`) plus the new
  rederive-level test above; no dedicated browser-level absence assertion.
  Accepted — the Go assertions are the stronger signal for this class of
  check.
- **N7 (help topic).** No `web/help/` topic changed. Accepted: the
  affordance restores controls the operator already knows from the nav
  rail, adding no new capability or screen to document.

## Judgement call: skipping the discard-confirm guard for lock/exit-to-OS

Both the lock form and the exit-to-OS link deliberately bypass the dialog's
"discard unsaved changes?" guard. Reviewer's independent read, which I
adopt: for lock, this isn't close — `common.DefaultIdleLockMinutes = 10`
already auto-locks the same till with no prompt at all, discarding the same
unsaved edits; a manual lock gated behind a confirm would be *more*
obstructed than the automatic path, which is incoherent, and the data at
risk (a half-typed dialog field) is not sale/money state. For exit-to-OS,
the "urgency" framing doesn't actually apply (it's a plain link into
Settings, and the real exit still costs a PIN afterward), but a confirm here
would still be friction without a matching risk, and asymmetric behavior
between two controls sitting side by side would be more confusing than
either uniform choice.

## Verified beyond automated tests

- `gofmt -l .`, `go vet ./...`, `go build ./...` — clean.
- `go test ./...` — full suite green (post-fix).
- `golangci-lint run ./...` — 0 issues (reviewer's isolated-worktree run).
- `guard-i18n.sh`, `guard-page-http-error.sh`, `guard-htmx-loaded.sh`,
  `guard-emoji-font.sh`, `guard-docs-shots.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` — all green.
- `make docs-shots` regenerated for real (Chromium) after both fixes;
  the only content change is `manifest.json`'s `surface_sha256` (source
  files changed) — no topic markdown text changed, so no screenshot pixels
  legitimately differ; several incidental anti-aliasing-only PNG diffs
  (`sell.png`, `till-designer.png`, `catalog.png` across locales) were
  reverted both times (pre- and post-fix regen) as out of this PR's scope.
- `e2e/tests/categories-record-dialog-2010.spec.ts` run for real against
  Chromium: 20/20 pass, including case (j) at both 1024×600 and 360px.
- TDD claim for the original template-absence behavior independently
  re-verified by the reviewer with three mutants in an isolated worktree,
  including the specific CSS-`display:none` bypass this review was asked to
  check for — the absence assertion is genuine markup omission, not a
  visibility toggle.
- Checked against all four shipped core locales (`en`/`ar`/`fa`/`tr`, RTL
  included) at 360px with no overflow. A genuinely long-form locale (German)
  ships only as a separate `ut-plugin-language-de` pack, not installed in
  this sandbox — flagged rather than silently assumed fine.
- No real client/shop name as demo/seed/test data; no secret-shaped literal
  values.

## Safe-to-merge verdict

**Yes, post-fix.** The blocking finding (B1) is fixed and independently
re-verified via TDD revert/restore; the accessibility finding (N1) is
fixed; the two remaining non-blocking findings are filed as follow-up
cards (ut-docs#2121, ut-docs#2122) rather than silently dropped.

## Explicitly deferred

- ut-docs#2121, ut-docs#2122 (above).
- `catalog.html`'s `#item-form-modal` does not yet inherit this affordance
  — it predates `.record-dialog*` and isn't on the shared partial (tracked
  in `list-and-dialog-pattern.md`'s own Follow-ups, not a gap in this PR).
- Physical-device verification (a real pilot tablet, a genuinely long-form
  shipped locale) — no such device or pack available in this sandbox.
