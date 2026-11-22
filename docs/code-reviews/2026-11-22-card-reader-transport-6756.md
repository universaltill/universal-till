# Review: card reader registry — transport, device_serial, bound_at; kinds sdk/external (ut-docs#6756)

Date: 2026-11-22. Lane `lane:cloud-41`. Design: ADR-0205 (Accepted) §1, §3, §5, §8.

## What shipped

- Migration `114_card_readers_transport.sql` (append-only, checksum pinned):
  `transport TEXT NOT NULL DEFAULT 'internet'`, `device_serial` and `bound_at`
  (nullable). `bluetooth` rows (removed ones too) are rewritten to
  `sdk`/`bluetooth`; `tap_to_pay` rows get `device_own`. A row with an unknown
  kind is left as it is and is never offered for payment. Idempotent on replay.
- `internal/data/card_reader_repo.go`: kinds `sdk` and `external`, the
  transport constants (`serial` named only to be refused),
  `ValidCardReaderPair`, `defaultCardReaderTransport`, `knownCardReaderKind`,
  `CardReaderUnattendedAllowed`. `createTx` (the one insert path) refuses an
  unknown kind with `ErrCardReaderKind` and a known kind on the wrong
  connection with `ErrCardReaderTransport` (wraps `ErrCardReaderKind`, so the
  Settings 422 mapping is unchanged). Re-adding a removed reader clears its
  binding. `SetUnattendedDefault` also refuses `sdk` and `external`.
  `CardReaderKindBluetooth` is gone.
- `internal/pages`: `readerEligible` hides invalid stored pairs, `sdk` (no
  bridge yet) and `external` (its tender is #6755) — fail closed; a tender
  naming one gets the existing `card_reader.invalid`. The picker row carries a
  muted "Needs internet" / "Works without internet" line from the kind alone
  (template only; not in the JSON). The Settings list offers the kiosk-default
  checkbox only where ticking it can succeed, but keeps it on an already
  ticked row so it can be cleared.
- i18n: `card_reader.kind.sdk`, `card_reader.kind.external`,
  `card_reader.offline.needs_internet`, `card_reader.offline.external` (English
  + meta); `card_reader.kind.bluetooth` removed. Help: `card-readers.md`,
  `payments.md`. Manual screenshots/manifest regenerated (`make docs-shots`).

## Review

- BA, Architect, UX: Opus 5.5. Dev: Sonnet 5.5. Tester and independent
  review: Opus 5.5, fresh contexts (never the Dev agent).
- Verdict: no blocker; approve after fixes. All applied:
  1. (should-fix) the Settings kiosk-default checkbox was offered on sdk /
     external rows, where `SetUnattendedDefault` now refuses (422 with a
     misleading "reader ID" message). Fixed with `CardReaderUnattendedAllowed`
     shared by repo and template; test
     `TestCardReaderRoutes_UnattendedCheckboxOnlyWhereItCanSucceed` (also
     proves a ticked migrated row can still be cleared).
  2. (nit) unknown kind reported as a transport mismatch → now
     `ErrCardReaderKind`; `TestCreate_KindTransportPairs` pins both.
  3. (nit) `TestReaderEligible_Table` restores the previous
     `TapToPayAvailable`, not a hardcoded false.
  4. (nit) the migration test's bluetooth skip hid other-column changes on
     those rows; the snapshot now excludes only `kind` (checked separately).
  5. (nit) help line "every card reader needs an internet connection" becomes
     untrue with the external tender — flagged on #6755.
  6–8. (nits, ut-docs) stale kind lists / note wording — fixed in the ut-docs
     PR, except two lines that describe what had shipped at the time (left as
     history).
- Tester: PASS. Mutation checks (migration rewrite removed, pair check
  removed, sdk/external exclusion removed, unattended refusal reverted) each
  turned a test red. Real driven run: built binary, mixed seed plus raw sdk /
  external / mismatched rows; `/api/pos/card-readers` returns only the cloud
  rows; picker screenshots at 1024×600 and 360×740 show the note under the
  kind line, rows ≥ 56 px, no overflow, notes stay through an 11 s network cut.
  Not checked: RTL, long locales, real hardware.
- Unrelated timing failures under `-race` on this 4-core container
  (`TestSetupGBJoin_OfflineFinishesPendingThenRetryInstalls`,
  `TestStockUploadPendingAndHasSendable_Fast*`) pass without `-race` and touch
  no code here.

Pre-flight: help topic updated (card-readers, payments) + docs-shots
regenerated; reference pages updated (ut-docs PR: payment-provider-contract,
ux-card-readers-attach-flow); en.json keys have en.meta.json metadata;
ADR-0198 / ADR-0201 dated notes (ut-docs PR), adr/README index n/a (no new
ADR); README n/a (no claim changed); ut-cloud mirrors n/a (no manifest,
host-function or palette change); follow-up: #6755 help wording note.
