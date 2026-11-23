# Review: payments finish batch (ut-docs#6653, #6565, #6566, #6455, #6811)

Date: 2026-11-23. One combined review of four uncommitted worktrees, one PR each:

| Branch | Repo | Card |
|---|---|---|
| `fix/6653-tender-journal` | universal-till | #6653 durable payment-leg journal (migration 115, ADR-0136 Decision 9, merged as ut-docs#6938) |
| `fix/6565-bundled-lock-refresh` | universal-till | #6565 bundled lock: stripe 1.9.0, sumup 1.9.2, tax-uk 1.3.1 |
| `fix/6566-picker-lastused-6455-hardening` | universal-till | #6566 picker last-used test, #6455 G-4 tap entry binding, G-6 tap offering rules |
| `fix/6811-own-key-version` | ut-cloud | #6811 `key_version` on tap payments (own-key rotation) |

Dev: Dev agents (Sonnet 5.5). Tester: separate agent (`temp/wt-integ-payments/TESTER.md`; its two failures were fixed
afterwards, the ut-cloud failures there were the pre-existing toolchain). Reviewer: Reviewer agent (Sonnet 5.5, not the
Dev), one round, TDD claims re-verified by reverting the fix.

Pre-flight: review record: this file (ut-cloud: its own record) · ADR: amended before code (ADR-0136 Decision 9, ut-docs#6938 merged; ADR-0007 met) · help: updated (`payments.md`, one sentence) and `web/help/img/manifest.json` NOT regenerated: `make docs-shots` fails on this macOS host (font pin: needs Inter/DejaVu/Noto, ut-docs#4407), so the `docs-shots` guard is red on #6653 and #6566 until it is run on a host with the pinned fonts (the guard hashes `internal/pages/**`; #6565 changes test files only and passes the guard). The run deleted images, restored with `git checkout` · en.json keys: English + `en.meta.json` only (ADR-0160) · data model: `docs/data-model.md` updated · sync: `nonAdminTables` entry for the new table · api reference: updated (ut-cloud) · README: n/a · follow-ups below.

## What was checked

- Money paths: write-before-ask is fail closed (no journal row, no provider call, basket kept, own toast on till and kiosk); every later write is best effort and never changes an outcome after the provider was asked. The sale and its `settled` rows share one transaction (`CompleteSale`), idempotent per sale id, so the receipt-number retry never doubles them. Boot reconcile touches only rows older than this process, never refunds an `asked`/`unknown`/`reversing` leg (money may never have moved), never auto-reverses a tap leg (ADR-0198), runs once per leg (`warned`/`reverse_failed` are not re-listed), and is bounded (500 rows, 5 min, off the boot path).
- Migration 115: additive `CREATE ... IF NOT EXISTS`, number free on main, checksum pinned, append-only trigger (UPDATE aborts, DELETE only for the retention sweep of terminal chains older than 90 days), no card number/expiry/CVV stored, till-local (listed in `nonAdminTables`, not synced).
- Lock refresh: I downloaded the stripe 1.9.0 and sumup 1.9.2 bundles from GitHub into an empty dir and the SHA-256 equals `bundle_sha256` in the lock; `gh release list` shows all three are the latest releases. The floor test (`lock_floor_test.go`) stops the lock lagging again; tests follow the lock instead of a literal version.
- G-4: `tap_to_pay_request` is denied unless the running event belongs to the plugin's own `tap: true` entry. Reverting the `s.tapEntry` check turns `TestTapRequestBoundToTapTrueEntryOnly` red (`non-tap entry: code 49, want -2`).
- ut-cloud #6811: a replaced own key can no longer turn a possibly charged payment into "not found" or "abandoned": 409 `payment_from_previous_key`, only the current key is ever used, a connect-mode payment keeps `key_version` 0 and the credential still follows the row's `key_mode`. Column is additive with default 0 (ent auto-migrate).
- Ran: `go build ./...`, `go vet` on the touched packages, `go test` of data, pos, db, app, full pages (all green), bundled, plugins tap tests; ut-cloud stripeconnect, merchantexport, api.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | G-6 `tapOfferGate` failed OPEN when the plugin-list read failed (`tapEntry` swallows the error): on a kiosk, satellite or an offline sale a tap leg could pass the gate. Downstream `tapAuthorize` is fail closed, but the gate is the stated refusal point | fixed: uses `tapEntryErr`, an error refuses (`tapStartFailedError`); `TestTapOffer_FailedPluginReadRefusesOnAKiosk` is red with the old condition, green now |
| 2 | medium | #6653 help sentence, code comments and a boot log line said a manager is asked / the "needs-attention list" shows legs that could not be settled, but that list (slice 3) is deferred: a warned leg is only in the log, so a shop owner would be told something the till does not do | fixed: help and comments/log now say the till records it in its log for a manager to check at the provider |
| 3 | low | `withoutTapEntries` keeps the list when the plugin read fails (kiosk picker could show a tap entry) | accepted: by design and documented, the tender gate (finding 1) refuses the leg |
| 4 | low | `dev-6653` notes said no manifest regeneration was needed; the docs-shots guard hashes every non-test `internal/pages` file, so #6653 and #6566 needed it | open: the regeneration cannot run on this host (font pin); deleted images restored; see Deferred |

## Deferred / not done

- `make docs-shots` for #6653 and #6566 (manifest only, no screen changed): run on a Linux/pinned-font host after merging the pages changes, once, on the last merged branch; never hand edit the manifest.
- Slice 3 of #6653 (attention list + Retry for `warned` / `reverse_failed` legs): until it lands those legs are visible only in the log. Backlog card needed (the card's own follow-up line).
- Merge order: lock (#6565), then #6566 + #6455, then #6653. `web/i18n-meta/en.meta.json` has adjacent inserts in #6653 and the others, and `web/help/img/manifest.json` changes in #6566 and #6653: the second of those two merges resolves them at merge time (merge main into the branch, no rebase) and re-runs `make docs-shots` for the manifest.

## Verdict

Approve after the fixes above. No secrets in the diffs (the ut-cloud tests use placeholder keys), no real shop names in test data.
