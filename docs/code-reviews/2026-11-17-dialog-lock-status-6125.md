# Review: catalogue/inventory dialogs keep Lock, status and exit-to-OS reachable (ut-docs#6125)

**Scope:** branch `fix/6125-dialog-lock-status` (PR #2515), effective change
`git diff origin/main` at 1c6780cf (original 03d41a98, merge 711eb34b, then
the reconcile with main's #6553 scroll and #6131 confirm): `web/public/app.css`,
`web/ui/partials/record_dialog.html`, `web/ui/pages/{catalog,inventory,import,tax_codes}.html`,
`internal/pages/{catalog/handlers.go,tax_codes_page.go}` (template file lists),
Go tests (`internal/pages/dialog_chrome_6125_test.go`,
`internal/pages/catalog/dialog_chrome_6125_test.go`, kiosk/stock/colour/banner
test updates), e2e (`record-dialog-lock-status-reachable-6125.spec.ts` new;
`catalog-item-form-1956`, `inventory-stock-dialog-2011`,
`catalog-import-dialog-scroll-6553` adjusted; `playwright.config.ts`
AUTH_ONLY_SPECS), `web/help/en/{catalog,inventory}.md`, `CHANGELOG.md`,
regenerated `web/help/img/**` (146 PNGs). Companion: ut-docs PR #6135
(`reference/list-and-dialog-pattern.md`).

**Author:** Dev agent (Sonnet). **Reviewer:** Opus 5.5 (independent).

Pre-flight: till user-visible behaviour → `web/help/en/catalog.md` +
`inventory.md` updated, `make docs-shots` regenerated (guard-docs-shots
passes); `reference/` → ut-docs `list-and-dialog-pattern.md` (PR #6135,
needs the stale-text fix in finding 1 before merge); README n/a (no
capability claim); `en.json` n/a (no new key: reuses `auth.lock`,
`settings.display.exit_to_os_btn`, both with metadata); plan/price n/a;
platform claim n/a; ADR n/a (applies coding-standards §10 / CLAUDE.md
offline-first; no new mechanism beyond the record-dialog Lock form,
ut-docs#2099); plugin/manifest n/a; review record → this file; follow-ups →
deferred items below.

## What shipped

- New `.dialog-beside-chrome` class on `#item-form-modal`, `#import-modal`,
  `#tax-codes-modal`, `#stock-dialog`: block-end stops at the measured status
  row (`--phone-sb-h`), so the real `.statusbar` is uncovered at every width;
  at ≥481px `inset-inline: var(--rail-width) 0` puts the dialog beside the
  rail (logical, mirrors under RTL). The OSK `body.osk-padded` rules
  (higher specificity, explicit block-size) still win, so keyboard space is
  unchanged.
- `dialog_status_inline` (record_dialog.html): Lock form + exit-to-OS link,
  44×44 icons, at the inline-start of each dialog's existing title line at
  every width. The Lock form and exit link are now single defines
  (`dialog_lock_form`, `dialog_exit_link`) shared with the record dialog's
  status row, so all copies stay identical to session_chip's logout form.
  Import/Tax codes render it only when `InItemsShell` (inside the dialog),
  never on the standalone page.
- Title is `tabindex="-1" data-ut-focus-home autofocus`, so `.show()` and
  stray focus land on the title, not Lock; the item editor still hands focus
  to Barcode after paint (the existing #1956 hand-off now also accepts the
  title as "neutral").
- Reconcile with main: the branch's `.dialog-page-scroll` was dropped in
  favour of main's #6553 id rules (dialog is the scroller); the `.page-head`
  sticky rule gains z 2, a divider and a shrinking h1. A
  `.catalog-form-head-titleline` wrapper (`display: contents` at ≥1024)
  keeps Lock/exit on the title line where #6138/#6176 flatten the title
  group (≤1023px); at ≤480px the item title is .9rem so the edit head stays
  under #2000's 30% budget (measured 177.9px of 640 = 27.8%).

## UX §7: replaced, not met

The UX design's §7 lines saying that at ≥481px the dialog relies on the
**rail's own Lock** are **replaced, not met**. The in-title Lock is used at
every width because the rail's Lock is not reliably reachable beside an open
dialog: under `#ut-scrim` the rail is click-through and cannot be scrolled,
and with a real manager session at 1024×600 its Lock sits at y≈674–722 of a
600px viewport (below the fold, measured). Making the rail's Lock reachable
under a scrim at 1024×600 is a separate follow-up (named in the ut-docs
pattern doc).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | Companion ut-docs PR #6135 is stale after the reconcile: it still documents a `.dialog-page-scroll` class for `#import-modal`/`#tax-codes-modal` (removed; the scroller is now main's #6553 id rules) and quotes the old 180.7px / 28.2% head measurement (now 177.9px / 27.8%). | **Open — orchestrator to fix in ut-docs PR #6135 before merge** (replace the `.dialog-page-scroll` bullet with "scroll as one box via the `#import-modal, #tax-codes-modal` rules from ut-docs#6553, with the `.page-head` sticky at z 2"; update the measurement). Not editable from this repo. |
| 2 | low | PR #2515 body is stale: names `docs/code-reviews/2026-11-12-…` (record is `2026-11-17-…`), head 180.7px (now 177.9px), and "Run make docs-shots" as a to-do (done). | **Open — orchestrator to update the PR body.** |
| 3 | low | Stale "phone" wording after the design moved to "every width": `catalog/dialog_chrome_6125_test.go` header + test name `TestItemForm_PhoneLockAndExitInTitleLine`, wrong spec filename in that header, and "phone" comments in `record_dialog.html`, `catalog/handlers.go`, `tax_codes_page.go`. | **Fixed** (comments + test renamed to `TestItemForm_LockAndExitInTitleLine`). |
| 4 | low | Lock is the first Tab stop after the title, so Tab + Enter from a just-opened dialog locks without a discard prompt. | **Accepted:** same as the record dialog's status-row Lock (#2099) and the rail Lock, which deliberately never prompt (locking is a safety action); initial/stray focus never lands on it (title is the focus home; e2e asserts it). Help prose says unsaved edits are not kept when locking. |
| 5 | low | Exit-to-OS link navigates to `/settings#settings-display` with no unsaved-edit prompt, and the PIN exit form there renders only for managers. | **Accepted:** identical to the existing record-dialog exit link (#2099 precedent) and any rail link; no `beforeunload` guard exists anywhere on these pages, so no new loss path. |
| 6 | info | At ≤480px the item title drops to .9rem to share the line with Lock/exit; a long name wraps to two lines and the head can then exceed the #2000 30% budget. | **Accepted** (Dev flagged it); the typical-name case is measured at 27.8%. |
| 7 | info | 146 PNGs regenerated across all locales, most for topics this change does not touch. | **Accepted:** sizes move by ~1–3% (rendering churn accumulated on main plus the in-title Lock on catalog/inventory/tax-codes); spot-checked `en/inventory.png`; `guard-docs-shots.sh` passes. |

Checked and found correct: kiosk isolation (#4324) — no self-order template
includes `dialog_status_inline` and `TestSelfOrderKioskTemplatesHaveNoRecordDialog`
now bans it explicitly; the dialogs only exist on operator pages, where a
staff session keeps the controls. Lock form stays outside every
`fieldset[data-main-till-lock]`. No `showModal()`, no native `confirm`, no
new i18n keys, all new CSS logical (`inset-inline`, `inset-block`,
`margin-block`). The id-scoped #6553 scroller and the class-scoped
beside-chrome rule don't conflict (the id rule sets no inset/size).
`autofocus` on the closed dialogs' titles cannot steal focus at page load (a
closed dialog is not rendered).

## Verification

- Falsifiability (reviewer, inline revert→run→restore): with only the two
  `.dialog-beside-chrome` CSS rules neutralised, the 6125 spec's
  ">=481px … hit targets" (4 dialogs) and "offline/online flips the real
  status bar" (1024 + 360) tests fail 6/6 ("dialog starts at/after the rail's
  end", "the offline chip is reachable over the dialog"); restored, they pass.
  Dev additionally reported CSS-only revert 16/21 and template-only 18/21
  failing before the reconcile.
- e2e (Chromium, preinstalled, `--trace off`):
  `record-dialog-lock-status-reachable-6125`, `catalog-import-dialog-scroll-6553`,
  `phone-record-dialog-confirm-lock-6131`, `inventory-stock-dialog-2011`,
  `catalog-item-form-1956`, `catalog-item-form-2000`, `phone-lock-over-scrim-6118`:
  **75 passed, 0 failed.**
- Go: `go build ./...`, `go vet` (pages, httpx), `gofmt -l` clean;
  `go test ./internal/pages/... ./internal/httpx/... ./web/...` all ok
  (`internal/pages` 592 s); `internal/pages/catalog` re-run with `-count=1`
  after the reviewer's rename: ok.
- CI `build`-job guards: all pass locally except four that cannot run in this
  sandbox and are unrelated to the diff — `guard-armv7-seccomp` (no
  qemu-arm), `guard-catimport-version` (catimport tags not fetched; no
  `catimport/` change), `guard-deadcode-baseline` (local Go 1.26 vs go1.27
  module), `guard-shellcheck-version` (no shellcheck binary; no shell
  change).

## Verdict

**APPROVE** for the till side, conditional on findings 1–2 being fixed by the
orchestrator (ut-docs PR #6135 text and the PR #2515 body) before merge; merge
the ut-docs PR alongside.

## Deferred

- Rail Lock unreachable under a scrim at 1024×600 (and `#category-items-modal`
  still covering the status bar) — follow-up named in the ut-docs pattern
  doc; needs a Backlog card if not already filed.
- `inventory-stock-dialog-2011` (f)/(g) pre-existing flake on main — the
  Dev's PR names a follow-up card to file.
