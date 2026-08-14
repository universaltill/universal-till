# Code review: German TSE hard-gate on sale completion (ut-docs#715)

**Date**: 2026-08-14
**Card**: ut-docs#715 · **Design**: ADR-0048 (`ut-docs/adr/0048-german-tse-hard-gate-and-owner-override.md`, accepted, its own review record: `ut-docs/code-reviews/2026-08-14-adr-0048-german-tse-hard-gate.md`)
**Branch/PR**: `pipeline/715-german-tse-hard-gate`
**Complexity**: `hard` → Dev at Fable, Review at Opus (fresh context, worktree-isolated), per the scrum-master skill's model routing.

## What this change does

Implements ADR-0048's policy: a German shop declared as recording real
sales (`fiscal.system_of_record`) cannot complete a sale as system of
record until a TSE is declared configured (`fiscal.tse_configured`) — hard
block, no override, unreachable from any code path. A shop whose TSE is
configured but currently failing may be unblocked by an owner
(admin/super_admin)-granted, typed-acknowledgement, reason-required,
time-boxed (≤8h) override; every sale completed during that window is
flagged in the audit trail and marked on both the on-screen and printed
receipt. Shadow/trial/demo use (`system_of_record` false) is never gated.

### New
- `internal/fiscal/` — the policy engine: `CheckSaleAllowed`,
  `RequiresHardGate` (DE only), `IsOwnerRole` (admin/super_admin, never
  manager), `ActiveOverride`, typed errors, `ConfirmationPhrase`,
  `MaxOverrideDuration`.
- `internal/pages/fiscal_api.go` — `POST /api/fiscal/tse-override`,
  `POST /api/fiscal/{system-of-record,tse-configured}`,
  `GET /ui/fiscal-override-banner`.
- Tests: `internal/fiscal/fiscal_test.go`, `internal/pages/fiscal_api_test.go`,
  `internal/pages/fiscal_gate_tender_test.go`,
  `internal/pages/fiscal_receipt_print_test.go`.
- `web/help/{en,ar,fa,tr}/fiscal-compliance.md` (no `routes:` claim —
  `/settings` stays claimed by `display.md`; linked via `helpLink`).
- `web/ui/partials/fiscal_override_banner.html`.

### Changed
- `internal/pages/pos_api.go` — gate at the top of `completeTender`
  (covers cashier + kiosk in one place); per-sale `unsigned_override`
  audit; cashier call-site → 402 with two distinct localized messages.
- `internal/pages/self_order_shop.go` — kiosk call-site → 409, one
  combined message (an anonymous customer can't fix either block kind).
- `internal/pages/print_api.go` — printed (ESC/POS) receipt marker.
- `internal/data/pos_repo.go` — `SaleHasAuditAction`.
- `internal/pages/init.go`, `settings_page.go`, `web/ui/pages/settings.html`,
  `web/ui/partials/{nav,receipt}.html`, `web/locales/{en,ar,fa,tr}.json`
  (25 keys each), `README.md`.

## Independent review — findings and fixes

Fresh-context Opus subagent, `git worktree`-isolated (ut-docs#386 —
never share the orchestrator's live checkout for a revert-then-restore
TDD re-verification), given the diff, ADR-0048, and instructed to
actually run build/vet/test/guards rather than just read.

### Blocking (fixed)

1. **The override marker never reached the printed receipt.**
   `print_api.go`'s `buildReceiptDoc` is a wholly separate ESC/POS render
   path from `renderReceipt` (the HTML template) and was untouched — the
   printed slip a customer keeps and the shop files carried no marker,
   contradicting ADR-0048 Decision 3 and the new help topic's own promise
   in all four locales. Fixed, keyed off the same per-sale
   `unsigned_override` audit entry (via `detail.ID`, not the current
   override-window state) so a reprint after the window naturally expires
   stays truthful, and a sale taken outside any window never gains a
   marker just because a different window is active at reprint time.
   Verified NOT a false-pass: disabling the fix made
   `TestBuildReceiptDoc_UnsignedOverrideMarkerIsPrinted` fail with
   "printed receipt must carry the override marker".
   Sub-finding caught while fixing: `print.Doc`'s renderers **clip**
   footer lines at `print.Width` rather than wrapping — the English marker
   line would have printed truncated, dropping the "no TSE signature" half
   (the entire point). Added a rune-aware `wrapReceiptNotice` helper plus
   a test asserting no wrapped line exceeds `print.Width`.

### Significant (fixed)

2. **All five localized override-form error messages were unreachable.**
   htmx doesn't swap a 4xx response by default, and `web/public/app.js`'s
   global `htmx:responseError` handler downgrades any of them to one
   generic "server error" toast (its own escape hatch is scoped to
   `/api/pos/` + status 400 only) — so an owner mistyping the ADR-mandated
   acknowledgement phrase, or hitting any other validation error, saw a
   generic toast with no way to tell what was wrong. Fixed with the
   existing house pattern (`hx-on::before-swap`, already used by
   `index.html`'s pfand-result form) scoped to the
   `#fiscal-override-result` target.

### Nit (fixed)

3. **Silent error swallow** on the on-screen receipt's marker lookup
   (`unsignedOverride, _ := repo.SaleHasAuditAction(...)`) dropped the one
   DB-read failure that would otherwise say a sale was taken unsigned.
   Both call sites (screen + print) now log on failure — fail quiet, not
   silent — while still not inventing a marker on read failure (which
   would mislabel an ordinary sale).

### Flaky test, found independently during my own re-verification (fixed)

4. Re-running the gate myself after applying the reviewer's fixes,
   `TestFiscalToggles_OwnerOnlyAndAudited` failed on a rerun (not part of
   the reviewer's own findings — caught independently). Its query ordered
   two same-second audit rows by `ORDER BY created_at, id`; `created_at`
   is `time.RFC3339` (second resolution, both writes land in the same
   test run) and `id` is a random `uuid.NewString()` unrelated to
   insertion order — so the tiebreak was effectively a coin flip on which
   row the test treated as "first". Fixed to `ORDER BY rowid` (the
   table's implicit SQLite rowid — TEXT primary key, not `WITHOUT ROWID`
   — is the true insertion order). Verified with 5 repeated
   `-count=1` runs, all green (was failing on most runs before the fix).

### Notes, not fixed (accepted / out of scope)

- **`scripts/ci/guard-compliance-claims.sh` does not exist** on this
  branch or `main`, despite `CLAUDE.md` and ADR-0048 §4 citing it as
  mechanically enforcing the ut-docs#667 wording list. Confirmed via
  `ls scripts/ci/`. Pre-existing CI gap, not this PR's bug. Reviewer and
  I both did independent manual sweeps of all new/changed
  `web/locales/*.json`, `web/help/**`, `web/ui/**` content for the
  forbidden terms (GoBD-compliant, audit-proof/revisionssicher, certified
  by the Finanzamt, filing the merchant's §146a Abs. 4 AO notification,
  "compliant"/"konform"/"garantiert") — zero matches both times. **Filed
  as a new Backlog card** (see close-out) rather than fixed here — adding
  a repo-wide guard script is a distinct task from this feature.
- **TOCTOU in `setFiscalFlag`**: two concurrent toggle requests could each
  read the same "old" value before either writes, so both audit entries
  could claim the same `from`. Low impact — single-tenant till, owner-only
  route, the final stored value is still correct either way, and
  ADR-0048 itself frames the toggle as an honesty mechanism, not a
  tamper-proof control. Left as-is.
- Per-sale `unsigned_override` audit write is best-effort after
  `CompleteSale` succeeds — a failed write means the sale keeps the money
  but gets neither the journal flag nor the receipt marker (same failure,
  same root cause, correct by design not to fail a tender whose money is
  already taken). Worth a louder operator alert once ut-docs#675 lands;
  not a defect in this card.
- ar/fa/tr `fiscal-compliance.md` help topics omit `keywords:` (en has
  them) — `guard-help-topics.sh` passes since it's optional. Cosmetic.

## Verification beyond the automated pass

- **TDD claim re-verified independently, not taken on trust.** Widened
  `IsOwnerRole` to also accept `"manager"`; `go test
  ./internal/fiscal/... ./internal/pages/...` then failed **three** tests
  (`TestIsOwnerRole`, `TestTSEOverride_ManagerIsRejectedEvenWithValidPIN`,
  `TestFiscalToggles_OwnerOnlyAndAudited` — one more than Dev's own report
  claimed), with the exact error output pasted in the reviewer's report.
  Reverted; diff byte-identical to the pre-mutation state; suite green
  again.
- Grepped the whole repo for writers of `fiscal.tse_failing_since`: only
  test files write it; zero production call sites, matching ADR-0048's
  "not yet driven by any live signal" statement and confirming the
  offline/failing distinction can't be violated by anything shipped here.
- `TestFiscalGate_ConfiguredHealthyProceedsEvenOffline` specifically pins
  the "a known-offline sale is never blocked by this gate" requirement
  through the real tender endpoint (`offline: true`), not just at the
  `internal/fiscal` unit level.
- Confirmed `{{ helpLink "fiscal-compliance" }}` actually resolves (not
  the bare `/help` fallback) via a throwaway assertion, since run against
  the real template.
- Confirmed no sale row is created on either block path
  (`SELECT COUNT(*) FROM sales` == 0) and that neither basket is reset by
  a blocked tender.
- No `internal/money.Money` involvement anywhere in this change — correct,
  this feature has no monetary amounts of its own.

## Final gate (after all fixes, run personally in the orchestrator's own checkout)

```
go build ./...                 clean
go vet ./...                   clean
go test ./...                  all packages ok, zero FAIL (incl. 5x repeated
                                run of the flaky test above, now stable)
guard-data-access.sh           ✓
guard-kiosk-engine.sh          ✓
guard-i18n.sh                  ✓ (1008 keys, all locales match en.json)
guard-help-topics.sh           ✓
guard-plugin-menu-read.sh      ✓
guard-compliance-claims.sh     does not exist — manual sweep clean (see above)
```

## Translations

All 25 new `ar`/`fa`/`tr` locale values and the ar/fa/tr `fiscal-compliance.md`
help topics are Dev's own first-pass AI translation — the homelab Ollama
translator (`192.168.1.231:11434`) was unreachable from this sandbox, same
established fallback as `2026-08-12-demo-seed-opt-in-539.md`. Reviewer
spot-checked script correctness, absence of leftover English, and
placeholder (`%s`/`%d`) fidelity across all four locales — clean. Treat as
a solid first pass needing native-speaker polish, not a blocker. The
standing `ut-plugin-language-{de,es}` follow-up for the 25 new `en.json`
keys is filed as a Backlog card (see close-out) — ADR-0048 §4 flags this as
mattering more than usual, since an untranslated German refusal message is
the worst outcome for exactly this card's audience.

## Deferred / needs human input before the pilot goes system-of-record

- Exact receipt/banner/refusal wording is a compliance detail for the tax
  adviser to confirm (ADR-0048 itself says so) — nothing here makes a
  certification claim in the meantime (verified above).
- No migration/permission-row work was needed or added: ADR-0048's own
  citation of migrations 042-045 doesn't match `main` (only 039/040
  exist, and 039 is inert groundwork) — role checks use
  `LookupUserRole`/`IsOwnerRole` directly, the actual house pattern
  (`CreateNegativeInventoryOverride`), not the not-yet-wired
  `role_permissions` table. Noted for whoever eventually wires #555.

## Outcome

**Safe to merge.** 1 blocking + 1 significant + 1 nit finding from
independent review, all fixed and re-verified; 1 additional flaky test
found and fixed during my own re-run of the gate. TDD claims independently
re-verified (stronger than reported, not weaker). No client/shop name used
as demo data; no secret-shaped literal anywhere in the diff.
