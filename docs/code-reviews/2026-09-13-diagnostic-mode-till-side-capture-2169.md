# 2026-09-13 — Diagnostic mode (a): till-side capture, activation, local session state + indicator (ut-docs#2169)

## What shipped

Part (a) of ADR-0092 (`adr/0092-persistent-diagnostic-mode-capture-and-live-tail.md`)
— the `universal-till` half of the persistent diagnostic-mode feature. Parts
(b1)/(b2) (`ut-cloud`'s activation/batch/revoke endpoints and entities) had
already merged; this card is the till side that talks to them.

**New `internal/diagnostics` package** — an allowlisted, typed event
emitter, never a string-based API:

- `events.go`: 7 event structs (`PluginAsk`, `TaxProvenance`,
  `TableAssignment`, `OrderStatus`, `Environment`, `PluginState`, `Gap`),
  each field carrying a `diag:"id|enum|count|ms|bool"` struct tag. A
  reflection-based static check (`checkEventShape`,
  `TestAllowlistedEvents_FieldTypesAreClosed`) fails CI if a future struct
  adds an untagged, wrongly-typed, or vocabulary-less enum field. A closed
  vocabulary (`enumValues`) backs every enum field; `Emit` refuses (drops,
  logs at Debug) any event whose enum value isn't registered or whose id
  field fails the charset check (see Findings #1). `Emit` is an atomic-load
  no-op when no session is active.
- `session.go`: local session state as ordinary till settings rows
  (survives restart/reboot/update for free), `Activate`/`Stop`/`Revoke`,
  a best-effort local-stop report for the next cloudsync heartbeat.
- `queue.go`: a bounded in-memory ring feeding a bounded on-disk pending
  queue (`paths.Data("diagnostics","pending")`, `MkdirAll`'d before first
  write), monotonic per-session sequence numbers persisted before each
  batch file, atomic temp+rename writes, a `diagnostic_gap{dropped_count}`
  marker per drop episode (ring-age-cap or disk-cap eviction), FIFO
  draining.

**Wired at real call sites**: `internal/pages/tax_hook.go` (plugin ask
lifecycle, including the "clean no_opinion" outcome — the exact shape of
the driving incident, ut-docs#1391's takeaway-VAT bug, that no prior
mechanism could see), `internal/pages/tables_claim_proxy.go` (table claim/
release), `internal/pages/order_status.go` (order status changes),
`internal/pages/import_page.go`/`plugin_settings_page.go` (tax-provenance
transitions), plus boot-time and activation-time `Environment`/
`PluginState` inventory. `internal/plugins/ipc.go` gained `AskFrom` (Ask
plus the answering plugin's identity) so callers can attribute an ask
without inspecting its payload.

**Settings UI + nav indicator**: `internal/pages/diagnostics_settings.go`
— activation redemption, local stop (two-step confirm showing exactly what
will be discarded), rail chip (persistent while active, gated so a cashier
never gets a link that 403s). Gated by the plain `canPerform(d, r,
"settings")` manager/owner check (same gate `registerCountrySettings` and
the TSE-provisioning dismiss/retry handlers use) — deliberately NOT the
heavier `checkOrElevate` approver-PIN flow, per ADR-0092 §1's own "no new
role, no new permission bit."

**Cloud sync**: `internal/cloudsync/diagnostics.go` (new) —
`ActivateDiagnostics` (real `{store_id, code, device_id}` contract against
`ut-cloud`'s live endpoints), `uploadPendingDiagnostics` riding the
existing cloudsync tick. `cloudsync.go` gained the `diagnostic_mode_revoke`
directive case and a best-effort stop report on the heartbeat device
record (cloud-side consumer not yet built — see Follow-ups).

**Issue-report attachment**: `internal/issuereport/bundle.go`'s `Meta`
gained an optional pointer to the active session's most recent buffered
events, sent over the existing upload path.

**i18n/docs**: 25 new locale keys × 4 locales (`en`/`ar`/`fa`/`tr`),
`web/help/*/display.md` (step 19) and `bug-reporting.md` updated in all
five locales (`de` included), screenshots regenerated for the 4 locales
whose `/settings` render changed (the nav-rail partial change would have
touched every screenshot; the unrelated 116 were reverted before commit,
verified against a clean `guard-docs-shots` pass).

## Independent review

Opus, fresh context, isolated detached-HEAD worktree (`.claude/worktrees/
review-2169`), from a `WIP: pre-review snapshot` commit so its own
revert-then-restore TDD verification never touched the orchestrator's
shared checkout. Verdict: **safe to merge after fixes** — no blocker-class
issue (no data loss, no security exploit with a real call-site path). Full
findings list and gate output are in the review's own transcript; the
items below are what changed as a result.

**Fixed in this PR:**

1. **`validIDValue`'s charset was weaker than its own doc comment and test
   claimed.** It rejected only whitespace/control characters and length —
   not punctuation. The reviewer's PoC: `PluginID:
   "ut_session=9f1c2b7e8a6d4c3b2a1f0e9d8c7b6a5f"` (a session-cookie-shaped
   value) with every other field valid reached the wire in full on the
   pre-fix code, and removing the whole check left the package's own test
   suite green — the id charset had zero coverage in isolation, because
   the existing adversarial test only ever set one field at a time,
   leaving an enum field at its zero value, which the *enum* check always
   refused first. Fixed: `validIDValue` now checks against an explicit
   `idCharset` (letters, digits, `-_.:/+@` — every character a real id in
   this codebase actually uses: uuids, `till-<uuid>` device ids, semver,
   sha256 hex, slugs), rejecting `=` specifically (the character that
   turns an opaque token into a labelled pair) and every other punctuation
   character. New test `TestEmit_RefusesIDFieldWithProseOrCookieValue`
   keeps every OTHER field on the struct realistic/valid and varies only
   the id field under test, so a failure can be attributed to the id check
   alone — confirmed to fail before the fix (verified by temporarily
   restoring the old charset) and pass after.
2. **A permanently-rejected batch blocked the whole upload queue.**
   `uploadPendingDiagnostics` treated every non-409 failure — including
   `ut-cloud`'s own documented `404 session_not_found` and `400
   invalid_request`/`invalid_event` — as transient and `return`ed,
   stopping the tick's drain. A malformed or session-deleted batch would
   therefore sit at the head of the queue and block every batch behind it,
   for every session, until `maxPendingBatches` eviction finally dropped it
   (losing everything queued in between). Fixed: `404` is now terminal for
   the *session* (same disposition as `409` — drain the whole queue,
   deactivate if it's the locally-active one); `400` is terminal for that
   *one batch only* (discarded, `continue` to the next batch rather than
   `return`). Three new tests (`TestTick404DrainsSessionAndDeactivates`,
   `TestTick400DiscardsOnlyThatBatchAndContinuesQueue`, plus the existing
   409/transient-failure tests re-run) prove each status code's actual
   disposition, including that a `400` on one batch does NOT stop a good
   batch right behind it in the same tick.
3. **`activationErrKey` mapped every unrecognized cloud error (e.g. `400
   invalid_request`) to "could not be reached"** — factually wrong (the
   cloud DID answer) and unactionable. Fixed: any documented
   `*cloudsync.CloudError` not matched by a specific case now maps to a new
   `settings.diagnostics.err_refused` key ("Universal Till Cloud refused
   this request…"); `err_unreachable` is now reserved for a genuine
   transport-level failure (no `CloudError` at all). New locale key added
   to all 4 files; new test case covers the genuinely-unreachable path
   (dead port, registered) that had no coverage before.
4. **`PluginAsk.CorrelationID`'s doc comment overclaimed** ("joins a
   cache-miss ask with any later event about the same ask") when the field
   is, as built, a fresh id per emitted event with nothing yet to thread it
   across a retry. Comment corrected to state the real, narrower scope and
   name the actual gap (a caller-side retry in `internal/pos` produces two
   unrelated correlation ids for one logical ask) as a follow-up rather
   than a documented guarantee that doesn't hold. Filed as ut-docs#2234 —
   real cross-retry correlation touches `internal/pos`'s recompute-retry
   loop, a tax-computation-adjacent path this review chose not to touch
   without its own dedicated scoping and testing.

**Deferred, filed as follow-up Backlog cards (none blocks this PR — no
data loss, no security exploit, no user-visible defect today):**

- ut-docs#2232 — `ut-cloud`'s diagnostic-event ingestion validates the
  `type` discriminator but not per-type field names yet (a documented,
  pre-existing gap on the cloud side, now unblocked since this PR defines
  the real wire vocabulary to allowlist against).
- ut-docs#2233 — `ut-cloud` doesn't yet consume the till's best-effort
  local-stop report on the heartbeat (the till side is built and sends it;
  nothing reads it yet, so a locally-stopped session stays `active`
  cloud-side until staff revoke it — an honest, bounded, already-documented
  lag, not silent data loss).
- ut-docs#2234 — `PluginAsk` correlation id across retries, and two
  high-frequency, non-operator-action event sources (a 30s reaffirm-tick
  emitting `table_assignment`, and a cache-hit `plugin_ask`) that may
  drown genuine operator-driven events in volume. ADR-0092 §2's "no
  severity sampling while active" means any mitigation needs a design
  decision, not a quick patch.
- ut-docs#2235 — `PendingSummary` parses every pending batch file's full
  JSON on every `/settings` render while a session is active (bounded at
  ≈40 MB worst case by the existing queue caps, but still a real render-
  time cost on Pi/tablet hardware with a real backlog); a narrow
  `Stop`/`Flush` race that can leak one flush's worth of events past an
  explicit stop (self-healing via the 404/409 path); `evictOverflow`'s
  "oldest first" claim only holds within one session; `Environment.
  DeviceModel` always empty (`mobile.go` doesn't plumb Android's
  `Build.MODEL` through the gomobile bind — the exact field the driving,
  Android-tablet incident most wants); and a minor UX nit (the activation
  code field clears on a failed attempt).

**Checked and found correct, not findings:**

- The allowlist invariant holds at every real call site traced (6 of 6):
  table/order/tax-code ids are always the underlying id, never an
  operator-typed label; `TillID`/plugin id/version/checksum all come from
  existing typed sources. A table named `"Window Seat PIN 4321"` in the
  test suite proves neither substring escapes.
- `os.MkdirAll` precedes the first write in `Flush` (this pipeline's own
  recurring bug class); confirmed load-bearing by removing it, which fails
  5 tests.
- The `ut-cloud` wire contract (allowed types, `MaxBatchEvents=500`,
  `MaxBatchBytes`, request/response shapes) was cross-checked against the
  real `ut-cloud` checkout, not assumed.
- Offline-first: nothing on the checkout path touches network or disk;
  `Emit` is an atomic load plus a mutex'd in-memory append only; flush/
  upload run exclusively on the cloudsync goroutine.
- Kiosk containment: `/self-order`'s standalone template never includes
  the chip partial; the chip route isn't in the auth-exempt set.
- No new CSS (the chip reuses existing `.nav-toggle`/`.nav-badge`, already
  logical-property-based) — no RTL risk introduced. All 25 new keys match
  across all 4 shipped locales (`guard-i18n` green).
- No secrets or real shop/client names anywhere in the diff.

## Verified

`gofmt -l .` (clean) · `go vet ./...` (clean) · `go build ./...` ·
`go test ./...` (60/60 packages `ok`, whole repo, twice — before and after
the review fixes) · `golangci-lint run ./...` (0 issues) ·
`guard-i18n.sh`/`guard-data-access.sh`/`guard-page-http-error.sh`/
`guard-htmx-loaded.sh`/`guard-help-topics.sh`/`guard-help-drift.sh`/
`guard-compliance-claims.sh`/`guard-docs-shots.sh` all green · targeted
`-race` runs on `internal/diagnostics` and `internal/cloudsync` clean ·
Playwright `diagnostics` project (3 specs: real registration + code
redemption against an in-process fake cloud, chip hit-tested on
`/settings`/`/`/`/menu` at 1024×600 and 360×740 in `ar` (RTL) and `en`,
survives reload, gone after stop) — 3 passed.

Beyond automated tests: 5 of the reviewer's TDD claims independently
re-verified via revert-then-restore (the enum-refusal path, the id-charset
gap itself, the static-check non-vacuity against 3 deliberately-violating
scratch structs, the untagged-field rejection, and the `MkdirAll`
precondition) — all failed with the guard removed, passed restored.

**Not verified — explicitly, per ADR-0092 §7's own honesty standard**: a
release-mode Android build. No device or emulator is available in this
(or the review's) cloud session; the diff touches neither `android/**`
nor `mobile/**`, so `android-ci.yml` correctly skips its own expensive
setup. The indicator is shared server-rendered HTML the WebView renders
unchanged, and the Playwright coverage above proves the markup itself is
correct — but the ADR's specific "verify on a release-mode Android build,
not only `BuildConfig.DEBUG`" acceptance item needs a human or local
session with real hardware before it can be marked satisfied. Not treated
as blocking (per this pipeline's own guidance: a card whose only remaining
gap is a real-device check other role-skills also cannot supply is a
documented limitation, not a reason to leave the rest of a working,
tested feature unmerged) — flagged here, on the issue, and left for a
human/local check.

## Follow-through to the external language packs

25 brand-new `settings.diagnostics.*`/`diagnostics.*` keys — no matching
core key existed anywhere before this PR, so `lang-pack-drift`'s
pack-first ordering isn't achievable (per this repo's own CLAUDE.md/
reviewer-skill rule): core merges first, `main` goes red on
`lang-pack-drift` until `ut-plugin-language-{de,es}` land matching keys,
and that is the expected, bounded state for this specific case. This same
lane opens and merges both pack follow-up PRs immediately after this PR
merges, in this same cycle.

## CI finding, after the PR opened

`guard-deadcode-baseline.sh` (ut-docs#1581/#1566's whole-program deadcode
gate) failed on the pushed head: two new exported functions in
`internal/diagnostics/events.go` were unreachable under its `-tags=desktop
-test=false` whole-program analysis.

- **`EventTypes()`** had zero callers anywhere, including tests — genuinely
  dead on arrival. Deleted, along with the now-unused `sort` import.
- **`EnumValues()`** is called only from `TestOrderStatusEnumMatchesPOS`
  (`internal/pages`), which pins `internal/diagnostics`'s hardcoded
  `order_status.status` vocabulary against `pos`'s real status constants so
  the two can't silently drift — a real, load-bearing regression test, not
  dead code. `deadcode -test=false` cannot see a test-only call site by
  construction; the guard's own header names this exact shape as a
  sanctioned baseline entry, citing `ResetCacheForTests` as the existing
  precedent. Added `internal/diagnostics/events.go: unreachable func:
  EnumValues` to `scripts/ci/deadcode-baseline.txt` (one line, correct
  sort position) rather than deleting genuinely-in-use test infrastructure
  or fabricating a fake production call site.

This guard could not be run locally in either the dev or review session —
both cloud containers lack the GTK/WebKit dev headers `cmd/unitill-desktop`
needs to even type-check under the `desktop` tag (a known, documented
environment limitation, not something either session missed checking for).
The fix was made directly from CI's own precise failure output (exact file
and function names) and re-verified via `gofmt`/`go build`/`go vet`/the
affected test packages/`golangci-lint`/the full `go test ./...` suite
locally; the guard itself is re-verified by CI on the next push, which is
also this fix's only available verification path.

## Safe to merge

Yes — no blocker-class finding remains open; every should-fix item from
the independent review is either fixed (with a new regression test) or
filed as a scoped Backlog follow-up with an honest reason it wasn't fixed
inline. Full gate green, three times (main-line, post-review-fixes,
post-merge-with-main); the deadcode-baseline CI finding above is fixed and
awaiting its own CI re-run.
