# Review: join notice warns a till that already has sales (ut-docs#3796)

**Card:** ut-docs#3796 (`complexity:medium`). The ambient join notice
(ut-docs#2721) offered "Link this till" to a standalone till however much
history it held. Linking goes through `POST /api/sync/pair-start`. On
approval, `completeJoin` stages the main till's snapshot as a pending
restore, and `db.ApplyPendingRestore` swaps it in at the next start, so the
till's whole DB is replaced: sales, catalog and settings. The old file
survives only as `backups/pre-restore-*.db`, and its sales are never sent to
the main till. Nothing warned about this.

## What shipped

- `internal/data`: `POSRepo.HasCompletedSales`. True when any sale is
  `completed` or `voided`; a void is fiscal history too. Open baskets don't
  count.
- `internal/pages/join_notice.go`: a `HasSales` flag for the partial. It
  fails closed: a DB error or no DB shows the warning.
- `web/ui/partials/join_notice.html`: when `HasSales` is set, the notice
  shows a warning paragraph, plus a `required` tick box *inside* the
  pair-start form ("Replace this till's sales and data"). The box has
  `aria-describedby` pointing at the warning and `hx-preserve`, so the tick
  survives the 30s re-poll.
- `web/public/app.css`: the warning on its own full-width row, in `--text`
  on `--warning-tint` with a `--warning` inline-start edge (#2680
  contrast). The label row is 44px. Logical properties only.
- i18n: `tills.discovery.ambient.has_sales_{warning,confirm}` in en/ar/fa/tr
  with metadata. The de/es/pt pack PRs follow in the same cycle.
- Help: `multitill.md` in en/de/tr/fa/ar.

## Review

Author: Opus 5.5. Reviewer: Fable (a different model), one round.
Verdict: **safe to merge** after the cheap fixes below. All of them are
applied.

**Fixed:**
1. (minor) Voided sales were not counted. A till whose sales were all
   cancelled still holds fiscal cancellation records. The filter is now
   `status IN ('completed','voided')`, with a test case.
2. (minor) The help said "close the day (Z report) first". The EOD row
   lives in the replaced DB, so it now says "close the day **and print**
   the Z report" in all five languages.
3. (nit) The de help said "Sicherungsordner". It now says "Backup-Ordner",
   matching the UI and `settings.backup.restore_note`.
4. (nit, a11y) The warning was not linked to the tick box. Added
   `aria-describedby`.

**Deferred to ut-docs#4507 (major, scope gap; the behaviour predates this
change):** the Tills page "Request to pair", the pairing-code join and the
setup wizard reach the same destructive join with no warning. The tick box
is enforced only by the browser: `pair-start` ignores `replace_local_data`.
That card adds server-side enforcement and the same confirm on those
paths. It also closes the ≤30s window where the first sale completes after
the notice has rendered (reviewer nit 6).

**Checked by the reviewer and found correct:**
- The required box really blocks htmx. htmx 1.9.12 triggers on `submit`,
  which the browser fires only after constraint validation. `trim-validate`
  runs later, in `htmx:beforeRequest`, and the form has no `novalidate`.
- `hx-preserve` carries the checked node across the swap.
- "Its own sales are not sent to the main till" is accurate: the push loop
  needs `sync.primary_url`, which goes live only on the restored DB.

## Verified

- `gofmt -l .` clean, `go build ./...` and `go vet` clean.
- `go test ./internal/data/... ./internal/discovery/... -race` green.
- `go test ./internal/pages/ -run TestJoinNotice` green: 15 tests,
  including 2 new. The reviewer also ran the full `internal/pages` and
  `internal/data` suites green.
- Guards green: i18n, i18n-meta, help-drift, help-topics,
  compliance-claims, competitor-naming, data-access, rtl-logical-css,
  no-inline-handlers, no-showmodal, core-neutral, kiosk-engine,
  mail-addresses, page-http-error, htmx-loaded, emoji-font, osk-loaded,
  autofill, card-data, netaccess, demo-env, plugin-menu-read,
  pipefail, readme-local-links.
- `guard-deadcode-baseline` could not run in this container: the deadcode
  tool was built with Go 1.26 and the module is 1.27. CI runs it.
- TDD re-verified by mutation:
  - The reviewer reverted the handler and template, saw
    `TestJoinNoticeUI_WarnsAndRequiresConfirmWhenTillHasSales` fail,
    restored, saw it pass.
  - I reverted the `voided` filter, saw `TestHasCompletedSales` fail with
    "a voided sale is fiscal history and must count", restored, saw it
    pass.
- Visual check: the real `GET /ui/join-notice` output, with a seeded sale,
  was rendered with the real `app.css` in Chromium at 1024×600 and 360×800
  in en, tr and fa (RTL), and the screenshots were looked at:
  - no horizontal overflow;
  - the label row is 44px;
  - the RTL edge sits on the right.
- Driven in Chromium: "Link this till" did **not** submit with the box
  unticked, and did submit once it was ticked.
- Accepted gap: this was not driven through two real tills on a LAN. The
  pairing mechanics are unchanged from #2721, which was driven that way.
