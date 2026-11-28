# Review: delivery order notes on the till (ut-docs#6988, #7186, universal-till#2765)

Reviewer: combined Reviewer for the batch, 2026-11-28. Code by Sonnet 5.5; independent pass by a Fable subagent (privacy and money touching), plus my own TDD re-verification. Scope: `git diff $(git merge-base HEAD origin/main)` on `feat/6581-delivery-orders` (delivery orders plus note erasure, migrations 122 to 124).

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | important | `ChannelOrdersRepo.Apply` treated any later revision without a note as a cloud redact. The cloud queues every staff-action revision (Accept, Ready) with no note (`channelorder/action.go`), so accepting an order erased the allergy note while the kitchen cooked, marked it `erased_by=redact`, and `storedErased` then blocked any restore. The help text "an order still open is never blanked early" was false. | FIXED: a note-free revision of an order that is live before and after the update keeps the order and line notes; a redact still works because it always ends the order (R8). Test `TestApply_NoteFreeRevisionOfALiveOrderKeepsTheNote` failed first (`notes lost on a staff-action revision of a live order: "" ""`), passes now; `TestApply_CloudRedactUpdateBlanksNoteAndMovesLiveOrderToTerminal` still passes. Residual: a note-free revision that ends the order (staff reject or collect) is also read as a redact and blanks at once, labelled `redact` (early erasure, the safe direction; a contract marker for "redacted" would make the label exact: follow-up). |
| 2 | important | Erasure is logical: `UPDATE note=''` plus a WAL checkpoint, no `secure_delete`, no VACUUM; the text can stay in free pages, and no code keeps `channel_orders.note` out of till snapshots or off-site backups (legal R6 not built). | NOT FIXED; the help text now says copies in a backup or not-yet-overwritten storage can outlive the blanking. Go-live gate (card), together with the cloud backup design (N1). |
| 3 | nit | `deliveryNoteDeadline`: a scheduled order uses start + 72 h with no floor after receipt (the cloud has a 24 h floor), so a start time far in the past erases an open order's note at the next sweep; no cap on a far-future start. | Deferred (card). |
| 4 | nit | `note_present` and `note_chars` ignore line notes, so `MarkDeliveryNoteViewed` never fires for a line-only note; `note_viewed_at` is stamped by the board poll, not by a person opening the note. | Deferred. |
| 5 | nit | The till's terminal list has `expired` and `failed`, which no status can be; the cloud has `shipped`. | Deferred. |
| 6 | important | Final-pass (Opus 5.5 subagent, delta): a note-free revision of a live order copied stored line notes back by `LineID` only; the Deliverect plugin numbers lines by position, so when the platform drops line 1 the renumbered line 1 (another item) inherited the allergy note. | FIXED: carry-back also needs the same `name` and `sku`. Test `TestApply_KeptLineNoteNeverMovesToADifferentItem` failed first (`burger note moved onto the fries line: "nut allergy"`), passes now. |
| 7 | nit | Migration 124 backfill left an order the till rejected at intake (status `new`, `reject_reason` set) with no `terminal_at` (14-day backstop instead of 24 h); comment listed `offboard` instead of `cloud_end`. | FIXED in place (124 not on main; pin in `shipped_migrations_test.go` updated, `TestShippedMigrationsUnchanged` passes). `partially_refunded` after collect stays on the backstop: status text does not say it ended; deferred. |
| 8 | nit | `note_present`/`note_chars` ignore line-only notes, so the "viewed" audit never fires for them (see 4); the cloud accepts `account_model: fixed` on kind `marketplace` while the till allows it only on `delivery`; the till mirror does not enforce capability names/values, `store_ref` shape, host shape, `secret_source`/`canonical` values (the cloud signs, so looser is harmless); `DeliveryRejectWithdrawn` and `NoteErasedByRedact` are unused; token_url error text still says `{account}`. | Deferred (card; none affects data safety). |

## Clean

Erasure runs on the till's own clock at start-up, every 15 minutes and before render, with no cloud call; channel orders are never LAN-synced; the 24 h default, 1 to 72 clamp, a refund never restarting the clock and a stale update never restoring an erased note are tested. Money is integer minor units. Migrations 122 to 124 follow main's 121 (main had taken nothing above 121 at review time, checked with `git ls-tree origin/main`); `TestShippedMigrationsUnchanged` passes. Offline-first: taps queue offline. Audit rows hold length only, never text. Help topic `channel-orders.md` updated in the same branch; new `en.json` keys have `en.meta.json` entries, English only (ADR-0160).

## The 72 h promise

Till help says 24 h after the order is finished (shop sets 1 to 72), 14 days after arrival for an order that never finishes, scheduled start + 72 h: honest, unchanged.

## Verification

`go build`, gofmt, `go test ./internal/data ./internal/pages -run 'ChannelOrder|DeliveryNote|NoteFree|Redact|Erase|Apply|Delivery'` pass. The Tester's `TestChannelOrderVendoredSchemaIsCurrent` failure is environmental (compares with the sibling dirty ut-docs checkout; the vendored file is byte-identical to ut-docs main).

Pre-flight: no new cwd-relative path; no file write added; help topic updated; no real names in fixtures; no secrets.

Final delta pass (2026-11-28, after the first review): Opus 5.5 adversarial subagent found no critical bug; fixes 6 and 7 applied with TDD. Verified: build, vet, gofmt, `go test ./internal/data ./internal/pages ./internal/plugins -run 'Channel|Manifest|Note|Delivery|Apply'`, `go test ./internal/db -run Migration`; the full `internal/plugins` package needs about 650 s (above the 600 s default timeout; the Tester ran it green with a longer one).

Verdict: APPROVE after fixes 1, 6, 7; finding 2 is a go-live gate, not a merge blocker (flag off, no real order possible).
