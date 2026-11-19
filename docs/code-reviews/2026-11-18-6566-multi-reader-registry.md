# Review — per-till card reader registry, picker and manager page (ut-docs#6566)

Status: Dev hand-off record, completed by the combined Tester pass and the
independent Reviewer (sections at the end). Scope here is the CORE part
(universal-till). The Stripe 1.9.0 / SumUp 1.8.0 plugin changes and the
ut-docs ADR amendments are separate PRs (design: PR order 0, 2, 3, 4).

Pre-flight: till user-visible behaviour — `web/help/en/payments.md` updated,
`web/help/en/card-readers.md` new (routes: `/settings/card-readers`),
docs-shots recipe + `mustShow` + seed updated and `web/help/img/manifest.json`
regenerated with `node e2e/tests-docs/write-manifest.js`, PNGs deferred →
`make docs-shots` on the pinned-font box (the harness refuses to run on this
Mac); `guard-docs-shots` stays red on `en/card-readers` until that is run · `reference/` page (`payment-provider-contract.md`) deferred → ut-docs
PR A · `README.md` n/a (lists payment plugins only, nothing stale) · new
`en.json` keys updated (54 keys, `en.meta.json` described, `card_reader` and
`tap` added to `areas.json`, English only, ADR-0160) · ADR amended deferred →
ut-docs PR A (ADR-0201 s3 + ADR-0198 O-4/s6.2/s6.14 must merge before this PR)
· new plugin type / host function / manifest field n/a (the capability is the
existing `hooks` entry `payment.<key>.readers`; no manifest or signing-mirror
change) · plugin repos deferred → Stripe 1.9.0 and SumUp 1.8.0 PRs.

## What shipped
- Migration `108_card_readers.sql` (`card_readers`, soft delete, unique
  `(method_key, reader_ref)`), checksum pinned. `internal/data/card_reader_repo.go`
  is the only SQL. Cap 32 active rows; name 1-40 chars unique among active rows;
  `reader_ref` matches `^[A-Za-z0-9_.:-]{1,128}$` (plugins put it in a provider
  URL path).
- Legacy import `MigrateLegacyReaderSettings`: every register-scoped, non-empty
  `*_reader_id` plugin setting becomes one `migrated` row, after every plugin
  sync (startup and Reload). Idempotent, never rewrites the setting, a removed
  row is not re-imported. Removing a reader equal to the legacy setting clears
  that setting in the same transaction (and bumps the settings generation).
- Tender: a method is `managed` when its plugin declares the hook
  `payment.<key>.readers` AND it has an active row; everything else is `legacy`
  (payload, request id and tender signature byte-identical to before, golden
  test). Client sends a registry row id (`reader`), never a provider ref. Rules:
  one eligible row used silently; several and none sent -> 409 `reader_required`
  (no money moved); wrong method / removed / unknown / provider ref -> 400
  `card_reader.invalid`; reader in use on this till -> 409 `card_reader.busy`;
  kiosk uses its single row or the `unattended_default` row, else
  `card_reader.kiosk_none`, never a `tap_to_pay` row. Authorize payload gains
  `reader_id` + `reader_kind` (before the digest, so a different reader is a
  different request id); `tenderSignature` includes the row id so a locked
  outcome-unknown tender cannot be retried on another reader. Approve writes
  `payments.terminal_id = reader_ref` server-side and stamps `last_used_at`
  (best effort).
- Refund / reversal / settle: `original_payments[].reader_id` from the payment
  row (no registry lookup, never blocks a refund; works with the reader removed
  or offline), reversal carries `reader_id` at top level and per leg,
  `.requested` carries `reader_id`.
- State: `payment.<key>.readers` (read-only, blocking), parsed by
  `internal/cardreaders` (unknown state / missing reader / non-zero exit /
  timeout / malformed / over 8 KB -> `unknown`, still selectable), 3 s total
  deadline, 10 s cache bounded at 32 entries, single flight, max 16 refs per
  call. Core's in-flight set and a pinned unknown-outcome leg win over the
  plugin's answer. `WasmRuntime.Sync` now marks `payment.*.readers` Blocking
  (found by the real-wasm test: without it the hook is non-blocking and every
  state would silently be `unknown`).
- UI: `GET /api/pos/card-readers` (picker fragment, JSON twin), one `Card` pay
  button replacing managed methods only (legacy tills unchanged), in-overlay
  panel (no dialog), split-tender reader select, manager page
  `/settings/card-readers` with add/rename/move/self-order/remove, journal
  detail shows the reader name while the row exists.
- Tests: red-first for data, cardreaders, tender, picker, routes, migration
  hook-up, Sync blocking, real compiled wasm guest end to end.

## Security exposure (what this change exposes)
- No card data. The registry holds a provider reader id (a device identifier,
  not a credential, not a PAN, token or secret); `guard-card-data-schema` is
  green. Logs carry reader refs only through `maskTxnRef` (last 4); the
  settings page shows the full id to a manager only; the picker fragment and
  JSON carry registry row ids only (test asserts no provider ref in the HTML).
- Reader ids now reach the cloud sales mirror, LAN sale sync and privileged
  integration plugins as `payments.terminal_id`, exactly like `trace_id`
  already does. `ipc.go` still clears it for unprivileged plugins. The registry
  TABLE itself never syncs (journal, snapshot, admin bundle, roster): classified
  in `nonAdminTables`, and `TestCardReaders_NotInSyncPaths` fails if any other
  file queries it.
- Validation of reader ids: strict regex in core (repo layer, so no caller can
  bypass), registry row id resolved server-side against active rows of the same
  method, a client can never name a provider reader; plugins are to validate
  again (Stripe `tmr_`, SumUp format) in their own PRs. `.readers` answers are
  size-bounded, unknown reasons dropped (never echoed), unknown states are
  `unknown`.
- Authorization: manager routes use the `settings` action (cashier 403, one
  test per route, audited via `settingsAudit`); picker is read + choose only and
  is open to a sale-only cashier. The self-order kiosk has no picker (403) and
  never sees a reader name.
- Offline-first: the state check never blocks checkout (it only runs on picker
  open, 3 s bound, failure = `unknown` + selectable); the tender is not gated
  on readiness server-side.
- Tap to Pay: `kind='tap_to_pay'` rows are offered only while
  `cardreaders.TapToPayAvailable()` is true (set by `mobile.SetTapToPayBridge`,
  i.e. never on a flag-OFF build); row creation and the collect wiring are the
  F6 follow-up (blocked on #6455 / #6456 / #3055).

## Findings and fixes during Dev (self-review)
1. `.readers` was non-blocking in production (Sync only blocks `.ask`,
   `.authorize`, `.refund`): fixed + test.
2. `PendingTender()` existed only as a test helper; moved to `service.go` since
   the picker reads it.
3. Importing `devicepay` from `pages` put its dead code into the deadcode
   guard: replaced by the `cardreaders.TapToPayAvailable` function variable.
4. Migration replay tests need `IF NOT EXISTS` (pre-merge edit of 108, checksum
   re-pinned).
5. Several guard classifications (demo routes, head asset signature, split-tender
   msg attrs with the ADR-0160 English fallback, schema drift).

## Not done / untested (for Tester and the follow-up cards)
See `/tmp/tabletfix/pm-dev-core.md`: real touch / Pi / screen reader / RTL
screenshots, PNG regeneration, e2e specs for the picker, Stripe and SumUp
plugin PRs, Tap to Pay end to end. The restored-backup banner (AC12 second
half) is NOT built: rows are kept after a restore and each reader's state is
asked live, so a reader id that belongs to another device shows offline or
unknown until a manager checks it; a dedicated banner needs restore detection
that does not exist for this table yet.

## Tester follow-up fixes (Dev, 2026-11-19; O1-O4)
Tests first (Go compile-red on `LegacyCard`; Playwright red on the footer assertion), then fixes:
- O1: `cardReaderGridView.LegacyCard` is set when the generic `card` method stays in the grid; the reader button then reads "Card reader" (new key `tender.card_reader`, en + meta), the generic one stays "Card". With no generic card the reader button stays "Card" as the UX note says. Both stay: they are different things (reader-driven vs manual).
- O2: the picker toggles `.card-reader-picking` on `#payment-overlay`; CSS hides `.tender-footer` and `.offline-toggle` then. List window with 10 readers: 297 px at 1024x600 and 370 px at 360 px (was ~140-187). Back restores them. The originals in `.tender-default-footer` are unchanged.
- O3: settings "self-order kiosk" label is a full-row flex target (>=48 px), checkbox 1.75rem.
- O4: Confirm label is built from DOM nodes, amount and name each in `<bdi>`; server-rendered plain text remains the no-JS fallback and is replaced on swap/select.
- O5: manifest regenerated with `node e2e/tests-docs/write-manifest.js`. PNGs NOT regenerated (pinned box needed).
Run: go test ./internal/pages (card reader/picker), ./web/..., guards i18n, i18n-meta, help-topics, rtl-logical-css, no-native-confirm rc 0; picker spec 11/11.

## Tester (combined pass, 2026-11-18/19)
PASS WITH FIXES (full report: Tester notes of the #6566 batch). Four defects
found and fixed red-first: picker rows squashed once the list scrolls
(`flex: none`); the picker's 10 s refresh counted as input so an open picker
never idle-locked (`poll=1` + `cardReaderRefreshPoll`); two unrecovered
goroutines in `internal/cardreaders` and a single-flight slot that a panic
left taken; "Last used" tag on a till where no reader was ever used. Suites:
gofmt, vet, golangci-lint clean; `go test ./...` green as CI runs it; new
Playwright spec green; 4 + 2 Playwright failures identical on a clean
baseline. Not driven: real hardware, screen readers, a real restart, the
`cutout` Playwright project (disk full). PNGs not regenerated (pinned box).

## Reviewer (independent, 2026-11-19)
Reviewed by: Fable 5.2 (independent Reviewer agent; the code was written by a
Sonnet Dev agent). One combined review for ut-docs#6566 across this repo, the
Stripe 1.9.0 and SumUp 1.8.0 plugins and the ut-docs amendments.

Pre-flight: help topic `card-readers.md` + `payments.md` section present and
re-read against the code (two lines added for the review fixes); new
`en.json` key English + `en.meta.json` only; no manifest, host-function or
signing change; ADR amendments are the ut-docs PR and must merge first; PNGs
still owed (`make docs-shots` on the pinned box).

Read in full: `pos_api.go` tender/lock/reversal diff, `card_reader_tender.go`,
`card_readers_page.go`, `card_reader_repo.go`, `internal/cardreaders`,
migration 108, the picker JS and templates, `auth/middleware.go`, the wasm
runtime's slot and timeout rules for the new event.

### Re-verified personally (money paths)
Targeted tests re-run: `internal/cardreaders`, `internal/pos`,
`internal/data` (reader tests), `internal/pages` (reader, picker, routes),
`internal/auth` (poll): all ok. Mutants on the uncommitted worktree, each
restored afterwards:
- `tenderSignature` ignoring the reader -> `TestReaderTender_UnknownOutcomeIsPinnedToItsReader` red. Killed.
- hook gate removed from `managedReaders` -> `TestReaderTender_RowWithoutPluginHookStaysLegacy` red. Killed.
- `reader_id` added AFTER the request-id digest -> **survived** (finding R3).

After the fixes: `go build ./...`, gofmt, vet clean; guards i18n, i18n-meta,
core-neutral, data-access, migration-version-collision, no-native-confirm,
card-data-schema, help-drift, help-topics, help-breadcrumbs rc 0; packages
`data`, `cardreaders`, `pos`, `auth`, `httpx`, `db`, `logging`, `web` ok.
`internal/pages` full package, twice on a machine loaded by other sessions:
all reader tests ok, two failures both times
(`TestSetupGBJoin_AutoInstallsEligibleTaxPlugin`,
`TestSetupWizardOnlyGB_AutoInstallsTaxPlugin`: the tax plugin's wasm compiled
in 5-6 s and the first-load check hit its deadline). Both pass alone, neither
is touched by this branch, the Tester's full run was green: load, not this
change; CI is the proof. Not run by the Reviewer: full `go test ./...`,
`internal/plugins`, Playwright, docs-shots.

### Findings
- R1, medium, security, **fixed**: core accepted `.`, `..` and ids holding
  `..` as a reader id (`ValidCardReaderRef`, `card_reader_repo.go`). A plugin
  puts the id into a provider URL path, so `..` would address
  `/readers/../...`. Stripe and SumUp refuse it themselves, but the contract
  names core's check as the first line and a later provider plugin may trust
  it. Now: first character a letter or digit, no `..`. Red first:
  `TestCreate_Validation` (4 new cases).
- R2, medium, money safety, **fixed**: a manager could remove a reader while a
  payment was running on it, or while a locked outcome-unknown tender was
  pinned to it. The retry on that same reader is how the till learns whether
  the card was charged; after the removal every retry answered 400 and the
  till stayed locked until the basket was cleared. Now the remove route
  answers 409 with `card_reader.settings.err_in_use` (new key) and removes
  nothing. Red first:
  `TestCardReaderRoutes_RemoveIsRefusedWhileAPaymentNeedsTheReader`.
- R3, medium, test gap on a double-charge guard, **fixed (test only)**: no
  test proved that `reader_id` is inside the request-id digest. Without it a
  leg declined on reader A and retried on reader B would reuse A's request id
  and the provider's dedupe would answer with A's attempt. New
  `TestReaderTender_AnotherReaderAfterADeclineIsANewRequestID` (pins the
  digest to an independently computed sha256); the mutant is now killed.
- R4, test added, no code change: Architect risk 1 (a tender locked while its
  method was legacy, then the plugin gains the hook and a row). The lock
  refuses the retry, no second authorize is sent:
  `TestReaderTender_LegacyLockIsNotRetriedAsManagedAfterThePluginGainsTheHook`.
  Also `TestReaderTender_LockedTenderWhoseReaderWasRemovedAsksNoPlugin`.
  The attempt id and the lock are in memory only, so a restart never reuses a
  request id, before or after this change: no new exposure on upgrade.
- R5, low, accepted (follow-up): a locked SPLIT tender whose legs ran on two
  readers shows only the reader of the unknown leg; if the cashier clears the
  split form, the same split cannot be rebuilt in the UI and the basket must
  be cleared (captured legs are then reversed by the existing abandon path).
  Fails safe; no money path.
- R6, low, accepted (follow-up): the `.readers` answer is capped at 8 KB only
  after the runtime buffered it (`answerCapFor` gives it no cap; guest memory
  is capped at 64 MiB). Ask the runtime for an 8 KB cap.
- R7, low, accepted: only register-scoped `*_reader_id` settings are imported
  and cleared. A global-scoped row of the same key (none is shipped by the
  first-party plugins) would still be read by the plugin after a removal.
- R8, low, accepted: after the import the registry decides the reader. A
  manager who later edits the plugin's own reader setting changes nothing
  until the next plugin sync adds it as a second row. The help page says the
  list is the place to manage readers.
- Checked and fine: the reader is resolved and refused BEFORE the fiscal
  dispatch and any authorize; the lock check runs on the resolved row id, so a
  silent single-row choice and an explicit one are the same tender; a legacy
  method's payload and request id are byte-identical (golden test); a stray
  `reader` on a legacy leg is dropped; the client can never set a provider
  reader id (`json:"-"` fields, row id resolved server-side against active
  rows of the same method); refunds read `payments.terminal_id` only; two
  concurrent tenders on two readers end in one sale and a reversal of the
  other (existing abandon path, the reversal carries its reader); the picker's
  `.readers` call is an ordinary call, so the reserved sale-path slot keeps
  an authorize from queueing behind it; cache, single-flight map and
  in-flight set are bounded and released on panic; names reach the DOM only
  through `html/template`, `textContent` and `escapeHtml`; manager POST routes
  sit behind `settings` and the SameSite=Lax session cookie like every other
  settings route; no provider name in core.

### Security exposure (what changed exposure)
- New till-local table `card_readers` holding provider reader ids (device
  identifiers; not credentials, not card data). Never in the LAN journal,
  snapshot, admin bundle or roster; it is in a full backup.
- Reader ids of approved legs are now written to `payments.terminal_id` and
  so travel wherever a payment row already goes: LAN sale sync, the cloud
  sales mirror, privileged integration plugins (unprivileged plugins still
  get it cleared). Same reach as `trace_id` today.
- New routes: `GET /api/pos/card-readers` (any signed-in operator, read and
  choose only; shows reader NAMES and states to a cashier, never a provider
  id) and four manager routes under `/settings/card-readers` (`settings`
  action, audited, denied in demo mode).
- New plugin call `payment.<key>.readers`: core sends the method's reader ids
  to that method's own plugin and only to a plugin that declared the hook; it
  is read-only and bounded (16 ids, about 3 s, 10 s cache).
- A plugin that declared the hook now receives `reader_id`/`reader_kind` on
  authorize, settle and refund. A plugin without the hook receives nothing
  new.
- Removing a reader deletes the matching register-scoped `*_reader_id` plugin
  setting (stated in the confirm dialog and audited). Nothing else touches
  plugin settings.
- No new secret, credential, host function, permission or outbound host.

### Verdict
Ready to commit and open the PR once the ut-docs amendments PR is merged
(ADR-0007). Before the RELEASE (not the merge): regenerate the help PNGs on
the pinned-font box, and run one sandbox or simulated-reader sale through the
packaged Stripe and SumUp wasm, since the plugins' `main.go` wiring is not
executed by `go test`.

Deferred (cards to file): R5, R6, R7; Architect F6 (Tap to Pay row end to
end, blocked on #6455/#6456/#3055), F7 (`card_reader_credentials`), F8 (SumUp
legacy refund endpoint units); the restored-backup banner (AC12 second half).
