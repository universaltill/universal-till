# Combined Reviewer, delivery reshape (2026-11-28)

Reviewer: one combined Reviewer agent (Opus 5.5; the merges and syncs were written by Sonnet 5.5). Scope:

1. ut-docs `docs/6988-delivery-reference-evidence` (PR #7127), `git diff origin/main...HEAD`: ADR-0215 Accepted, contract `channel-order-v1.json` + `sales-channel-v1.md` section 14, schema checker + self-test, 19 fixtures, evidence reference, ADR-0169/0201 notes, index.
2. universal-till: commits on top of `origin/feat/6581-delivery-orders` (merge 15a29db26, sync d635a2437).
3. ut-cloud: commits on top of `origin/feat/6989-6990-delivery-host` (merge 3f0fc728, sync 23a97f55).
4. ut-my-shop: commits on top of `origin/feat/6998-delivery-settings` (merge 1d15f29, fix 5c83e15).

Method: `git show --remerge-diff` on every merge (shows only what the resolution changed against a clean auto-merge), byte compare of every contract copy, targeted tests only (full-suite results are in tester.md).

## Findings (ranked)

| # | Sev | Repo | Finding | Status |
|---|---|---|---|---|
| 1 | important | my-shop | Merge kept the branch's old `<nav className="integ-actions">` link row (Online orders, Needs your decision, Stock sharing) that origin/main had removed when it moved those links into the action bar's More menu. Every **webshop** connection page (Shopify) showed each link twice; 5c83e15 only hid the More-menu copies for delivery, so the delivery test passed while webshop pages stayed doubled. | FIXED: wt-myshop commit (see below): removed the stale nav, More menu keeps Online orders for delivery too (review/stock stay hidden for delivery). New test `offers each of those links once on the connection page` failed first with `Found multiple elements with the role "link" and name "Needs your decision3"`, passes after. Integrations, Integrations.delivery, DeliveryOrders: 76/76 pass; oxlint and `tsc -b` clean. |
| 2 | important (tracked, not merge scope) | till + cloud | ADR-0215 Decision 9 / contract 14.3 is a binding bound: order and line `note` erased at most 72 h after the order is terminal, on cloud and every till. Neither wt-till nor wt-cloud has a note-blanking job (grep: no erase/blank of `note` in till `internal/`, none in cloud `channelorder`). Migration 122 stores `channel_orders.note` / `channel_order_lines.note`. | NOT FIXED (build work, not a merge defect). Must be carried on a card before any delivery adapter takes a real order; the orchestrator should confirm #6989 / #7189 carry it explicitly or file one. |
| 3 | nit | till | After renumbering 120/121 -> 122/123 the delivery migration test file, its test names and two code comments still said 120/121, now colliding in name with main's real 120 (setting_meta) / 121 (plugin_settings_ver). | FIXED: wt-till commit: file renamed `migration_122_delivery_channel_orders_test.go`, `TestMigration122_*` / `TestMigration123_*`, comments in `cloudsync/delivery_signals.go` and `data/delivery_availability_repo.go` say 123. `go test ./internal/db -run 'TestMigration12[23]_|TestShippedMigrations'` pass. |
| 4 | nit (accepted) | ut-docs | `scripts/check-channel-order-schema.py` measures `maxLength` in code points while the schema descriptions say "bytes" (500 / 256). JSON Schema `maxLength` is characters by definition; the byte cap is enforced separately in the cloud (`channelorder.MaxNoteBytes = 500`, schemacheck.go:85) and the gate's 256-byte name check uses bytes. The checker is a fixture checker, not the enforcement point. | ACCEPTED, no change. |
| 5 | info | till | Upgrade order: the renumber is safe for release (feature branch never shipped; main's 120/121 keep their pinned checksums, 122/123 pin the old delivery checksums, comment-only header change is checksum-neutral, `TestShippedMigrationsUnchanged` passes). A developer DB that ran the old branch has versions 120/121 recorded for the delivery SQL and will skip main's 120/121: throwaway dev DBs only, recreate them. | ACCEPTED. |

## Checks that came back clean

- **till merge 15a29db26**: remerge-diff touches only the two renamed migration headers and `shipped_migrations_test.go`; checksums 118..121 equal origin/main's, 122/123 equal the branch's former 120/121. No lost code.
- **cloud merge 3f0fc728**: `webhook_test.go` keeps both sides (`ChannelStoreBinding` and main's `ChannelFieldPolicy/BlockMode/FieldChange`); ent conflicts (`loadedTypes [83]`, re-indexed edges) verified by re-running `go generate ./internal/repositories/ent`: zero diff. `go build ./...`, `go vet` on channelorder/channelhook pass; `go test ./internal/channelhook ./internal/channelpartner/...` pass. `channelorder` fails only `TestEmbeddedSchemaMirrorsTheContract`, which compares against the sibling `unitill/ut-docs` and `unitill/universal-till` checkouts (on other branches): environmental until PR #7127 lands.
- **my-shop merge 1d15f29**: i18n import/spread conflict keeps both `DELIVERY_MESSAGES` and `CONFIG_MESSAGES`; `ChannelRuns` import, `SyncedStale` and the import hint kept (only the nav was wrong, finding 1).
- **Contract sync**: till `internal/cloudsync/testdata/channel-order-v1.json` and cloud `internal/channelorder/contract/channel-order-v1.json` are byte-identical to wt-ref `reference/contracts/channel-order-v1.json`; cloud `contract/fixtures/` is identical (`diff -r`) to wt-ref `fixtures/channel-order-v1/`.
- **ut-docs**: `bash scripts/check-channel-order-schema_test.sh` passes (19 fixtures, mis-bucket self-test, gate exemptions incl. modifier `name`, non-string, >256 bytes, nested). ADR-0215 status line, scoped dependency, README index and INDEX-by-feature entries, ADR-0169 dated note consistent. Cloud gate implements the `lines[].modifiers[].name` exemption (`channelhost/host.go:802-834`).
- **Authors**: every new commit in all four worktrees is `Farsid Mirza <farsid@taskrunnertech.co.uk>` (the configured lane identity), AI only in `Co-Authored-By`. No secrets in any diff (fixtures use opaque ids, no keys). Cloud non-English locale files carry English placeholders listed in `english-only-until-weblate.txt`, the repo's ADR-0160 pattern.

Pre-flight: review record updated (this file + per-repo records); en.json n/a (no till keys); my. UI changed (link duplication removed, no new strings, help unchanged: the links still exist in the More menu); ADR index updated (README + INDEX-by-feature); plugin host function `channel_menu_read` deferred to the build cards (#6994); note-retention job deferred (finding 2).

Verdict: APPROVE after fixes 1 and 3 (committed, not pushed). Finding 2 must be tracked on a card before go-live.

## Commits made (not pushed)

See the final report for SHAs.
