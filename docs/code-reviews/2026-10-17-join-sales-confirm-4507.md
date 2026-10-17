# Review: every join path asks before replacing a till's own sales (ut-docs#4507)

Date: 2026-10-17 · Branch `fix/4507-join-sales-confirm` · lane:cloud-54
Author: Opus 5.5 (Dev subagent, `complexity:medium`) · Reviewer: Fable, fresh context, in a separate worktree

## What shipped
Follow-up to #3796, which guarded only the ambient join notice. Linking a till stages the main till's snapshot as a restore. That restore replaces the till's whole DB, sales included, and those sales survive only in `backups/pre-restore-*.db`.

- **Server-side guard.** The guard is `joinNeedsReplaceConfirm` in `join_notice.go`. It is true when the till has completed or voided sales and the form lacks `replace_local_data=1`. A DB error counts as having sales.
  - `pair-start` (`/api/sync` and `/api/setup`) refuses with the pairing error fragment. Nothing is sent to the main till.
  - `POST /api/sync/join` and `POST /api/setup/join` refuse with a 409 `text/html` fragment before `joinPrimary`. Nothing is enrolled, downloaded or staged.
- **Pair-status re-check.** `pendingJoinState.replaceLocalData` carries the confirm. If a first sale landed while the main till's manager was deciding, `pair-status` stops right before `completeJoin`.
- **Tills page.** When the till has sales, the shared join fields above both tabs show the #3796 warning and a required tick box. The box is hx-included by **Request to pair** and **Join**.
  - A new `validate:<id>` step in `inline-actions.js` cancels the request while the box is unticked.
  - It reuses `tills.discovery.ambient.has_sales_*`, so there are no new `en.json` keys and no language-pack follow-ups. Only the `en.meta.json` descriptions changed.
- **Help and tests.**
  - `multitill.md` (en, de, ar, fa, tr) now says the Tills page warns and asks for the tick too. The screenshots were regenerated (`make docs-shots`).
  - `tills-pairing-layout-1548.spec.ts` ticks the box when the shared e2e till already has sales.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A sale rung up during `completeJoin`'s own enrol and snapshot download (up to about a minute) is not re-checked, so the restore is still staged over it. | Accepted. The window is documented in `pairStatusHandler`'s comment, and the sale stays in the pre-restore backup. A post-stage re-check with unstaging is on #4560. |
| 2 | minor | If the first sale lands after the Tills page rendered, the refusal shows the warning but the page has no tick box. The manager must reload. | Accepted, because the fix needs a new key plus pack PRs. On #4560. |
| 3 | minor | The first-boot wizard has no tick box, so a first-boot till with sales (in practice a fail-closed DB error) dead-ends. | Fixed: `refuseJoinWithoutReplaceConfirm` now documents this as a backstop and points to the Tills page. Also on #4560. |
| 4 | minor | The tick box is enforced only by the native validation bubble, which may not show on WebKitGTK kiosks. | Accepted: same precedent as #3796. On #4560. |
| 5 | nit | Two `PendingRestore` assertions in the new tests can't fail, because the stub's enrol answers 500. | Fixed: commented. The request and enrol counters are the real checks. |
| 6 | nit | Reusing the long warning as the error text gives a long "Error: …" line. | Accepted (no-new-keys constraint). |

The reviewer checked the route coverage:
- Every `completeJoin` caller is guarded, and `StageRestoreFromReader` is reached only through `completeJoin`.
- The `rp.mu` locking is consistent, and DB errors fail closed.
- `data-before-request` chaining runs the steps in order and stops at the first failure.
- hx-include of an unticked checkbox omits the field.
- Escaping holds on the new HTML and JS output.

## Verified
- **TDD, re-verified by the reviewer:** with the production files reverted and the test file kept, all five new refusal and render tests failed for the stated reason (a "waiting" render, an enrol reached, 502 from a contacted stub, missing markup). With the files restored they pass. The five regression guards (confirmed join, no-sales join, page without sales) pass both ways.
- **Dev full gate:** `gofmt`, `go build ./...`, and `golangci-lint` (0 issues) are clean. `go test ./...` passed; `internal/plugins` timed out while running alongside docs-shots and passed when rerun alone. All 54 build-job guard scripts passed, except shellcheck, which isn't installed (no shell changes).
- **Real run:** a till with seeded sales on :8095, driven with Playwright at 1024×600 and 360px, in en and ar (RTL).
  - Join sent 0 requests before the tick and 1 after.
  - No horizontal scroll; the warning sits on the inline-start side in RTL.
  - curl: `/api/sync/join` returned 409 with the warning, and pair-start returned the warning fragment.
  - Screenshots were looked at.
  - Not looked at: real touch hardware, and the LAN-discovery button in a browser (discovery is off in the harness; render test only).

## Verdict
Safe to merge. The deferred items are on ut-docs#4560.
