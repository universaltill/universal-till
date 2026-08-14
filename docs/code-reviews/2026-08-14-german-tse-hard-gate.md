# Code review: German TSE hard-gate on sale completion (ADR-0048)

**Date**: 2026-08-14
**Card**: ut-docs#715
**Branch**: `pipeline/715-german-tse-hard-gate`
**Design**: `ut-docs/adr/0048-german-tse-hard-gate-and-owner-override.md` (accepted, merged this cycle after its own independent review — see `ut-docs/code-reviews/2026-08-14-adr-0048-german-tse-hard-gate.md`)
**Dev**: Fable subagent (complexity:hard)
**Reviewer**: fresh-context Opus subagent, independent of the drafting reasoning, worktree-isolated, given the full diff scope and ADR-0048 as the binding design.

## What this change does

Implements ADR-0048 in `universal-till`:

- **`internal/fiscal`** (new package): the policy engine — six `fiscal.*`
  settings keys, `RequiresHardGate(country)` (true only for `"DE"`),
  `EvaluateGate(...)` returning `Allowed | BlockedNeverConfigured |
  BlockedTSEFailing | AllowedWithOverride`.
- **`internal/pages/fiscal_api.go`** (new): `POST /api/fiscal/tse-override`
  — admin-or-above only (verified after PIN auth, not just PIN-valid),
  typed acknowledgement, reason, duration capped at 8h, audit-logged.
- **`internal/pages/pos_api.go`**: gate evaluated at the top of
  `completeTender` (serves both cashier and kiosk); per-sale
  `sale`/`unsigned_override` audit marker written when a sale completes
  under an active override; on-screen receipt gains the marker line.
- **`internal/pages/settings_page.go`**: the generic settings editor is
  hardened against being a side door around the gate — the override
  window keys are unwritable by anyone; `fiscal.system_of_record` /
  `fiscal.tse_configured` are owner(admin)-gated and every transition is
  audit-logged; `fiscal.tse_failing_since` is refused entirely (no set,
  no clear) via this endpoint, matching ADR-0048's "not operator-settable
  in this card."
- **`internal/db/migrations/046_fiscal_tse_override_permission.sql`**:
  `fiscal_tse_override` seeded to `admin`/`super_admin` only — a
  deliberate break from the manager-inclusive pattern of every earlier
  permission migration (039–045).
- `web/locales/{en,ar,fa,tr}.json` (5 new keys), `web/help/*/sell.md` (new
  section, all 4 locales), `web/ui/pages/index.html` (persistent banner
  during an active override), `web/ui/partials/receipt.html`, `README.md`.

## Independent review — findings and fixes

First round returned **FAIL — 4 blocking findings** (plus several
non-blocking ones). All fixed on the branch; a second, narrower fix pass
followed the reviewer's own recommendations rather than a second full
review round, since none of the fixes reopened a money/tax/security
question the first round hadn't already settled.

### Blocking (all fixed)

1. **`guard-docs-shots.sh` failed** — the sale screen and manual topics
   changed but screenshots weren't regenerated. **Fixed**: `make
   docs-shots`, guard now green (verified twice — once mid-fix, once
   again after the later content edits below, since the first run was
   stale by the time the other fixes landed).
2. **Shipped copy instructed owners to use a Settings UI control that
   doesn't exist** — the refusal toast and `sell.md` said "an owner can
   switch the shop back to trial mode in Settings," but no such UI ships
   in this card (confirmed: `grep -rn "tse-override\|tse_configured\|
   system_of_record" web/` found zero UI references). **Fixed**: rewrote
   the copy in all 4 locales + all 4 `sell.md` files to say "ask an
   administrator" (true today via the settings API) instead of naming a
   nonexistent screen. The underlying gap — no dedicated toggle UI, no
   override-request dialog — is real and is **not** fixed here (building
   it is a UI feature, not a review fix); filed as `ut-docs#730`, a
   proper follow-up card rather than a silently dropped ADR deliverable.
3. **Refusal copy omitted two of the three TSE-setup routes** #715's own
   acceptance criteria name (hardware / own cloud account / managed
   subscription — only two were mentioned). **Fixed**: all three now
   named, in the same copy fix as #2.
4. **Receipt marking was on-screen only** — the printed (ESC/POS) receipt
   path (`buildReceiptDoc`) never carried the override marker, while
   ADR-0048 and #715's AC both require "marked on the receipt" without
   qualification, and the physical receipt is the primary artifact for a
   shop with a printer (the norm under §146a Abs. 2 AO Belegausgabepflicht).
   **Fixed**: added `POSRepo.HasAuditEntry` (new, minimal repo method —
   raw SQL stays inside `internal/data`) and wired `buildReceiptDoc` to
   check the sale's own `unsigned_override` audit row and append the
   marker line. Deliberately reads the *sale's own* audit row, not
   current settings — a reprint can happen well after the override
   window that was active at completion time, so re-reading current
   settings would give a stale or wrong answer for a reprint.

### Non-blocking, fixed anyway (cheap, and directly ADR-relevant)

5. **`lang-pack-drift`: the 5 new keys were missing from
   `ut-plugin-language-{de,es}`.** ADR-0048 §4 calls this out by name —
   German merchants are this card's entire audience, so an untranslated
   refusal message is the worst-case outcome for exactly the target user.
   Advisory on this PR, but **blocking on push to `main`** per the
   standing rule — left unfixed, this would have gone red on `main` right
   after merge. **Fixed**: cloned both plugin repos, added real German
   and Spanish translations (not baseline entries — genuinely translated),
   opened `ut-plugin-language-de#35` / `ut-plugin-language-es#32`. Their
   own `check-key-drift.sh` compares against `universal-till`'s live
   `main`, so both currently show an expected "orphan key" CI failure
   until this PR merges — documented on both PRs, holding the merge until
   after this one lands, not treating it as a bug to chase.
6. **`fiscal.tse_failing_since` was writable (and unaudited) via the
   generic settings editor** — ADR-0048 says this key is "not
   operator-settable in this card," full stop, but the shipped code only
   gated it behind admin-role, which still let an admin set or clear it
   with no trace. **Fixed**: the endpoint now refuses any write (set or
   clear) to this key outright, matching the ADR's actual intent — the
   key stays settable only in tests or a future real
   `fiscal.sign.ask` failure callback (#675). New regression test
   (`TestFiscalSettings_UpsertGuards`'s new subtest) covers both the set
   and the clear path.
7. **The fiscal-toggle audit write was silently best-effort** (`_ = ...`)
   — a failed audit insert would leave `system_of_record`'s transition
   completely untraced, exactly the gap ADR-0048 added the logging to
   close. **Fixed**: still doesn't fail the HTTP request (the setting
   write itself already succeeded by that point, and failing the request
   after the fact would be confusing, not safer) but now logs at ERROR
   level via the file's existing `logging.L()` convention instead of
   discarding the error.
8. **Migration 046's role restriction had no test coverage** — a test
   elsewhere hand-seeded the expected grant to mirror the migration, so a
   typo re-adding `'manager'` to the migration itself would pass every
   existing test. This is precisely the regression class ADR-0048
   Decision 3 warns about by name. **Fixed**: new
   `internal/db/fiscal_permission_test.go` opens a fresh DB through the
   real migration runner (`db.Open`, not a hand-seeded mirror) and asserts
   `fiscal_tse_override` is granted to `admin`/`super_admin` only.

### Deferred as genuine follow-ups, not silently dropped

- **No settings-toggle UI / override-request dialog** — `ut-docs#730`
  (see finding 2).
- **Refunds/returns aren't gated** — `CreateReturn` calls
  `pos.CompleteSale` directly, bypassing `completeTender` entirely, so a
  refund is unaffected by the gate. #715's AC is sale-specific and
  ADR-0048 scopes enforcement to `completeTender`, so this isn't a defect
  in what shipped — but it's a real open question the ticket's own "Why"
  doesn't fully resolve (a refund moves real money too). Filed as
  `ut-docs#731`, routed to the product owner as a genuine compliance-
  policy call, not guessed at here.
- **`super_admin` can't use the PIN-delegation path** (`AuthorizeManager`
  only accepts `manager`/`admin`) — inherited from following the ADR's
  own instruction to reuse that mechanism; `super_admin` sessions pass
  via `canPerform` regardless, so this only affects the PIN-delegation
  convenience path. Left as a known limitation, not worth a card on its
  own.
- **Banner date formatting** (fixed non-localized timestamp format,
  concatenated outside the translated string) — minor i18n polish, left
  as-is; worth revisiting if the manual UI work in `ut-docs#730` touches
  this surface anyway.

## Independently re-verified (by the reviewer, revert-then-restore)

- **A manager's valid PIN is rejected by the override endpoint** even
  though `AuthSvc.AuthorizeManager` authenticates it — confirmed real by
  temporarily removing the post-auth role check and watching
  `TestFiscalOverride_PINPaths` fail with a manager actually granted the
  override (`200`, not the expected `403`), then restoring and confirming
  it passes again.
- **The never-configured state is unreachable from the override by any
  path** — confirmed real the same way, temporarily removing the
  `tse_configured` pre-check and watching
  `TestFiscalOverride_UnreachableWhenNeverConfigured` fail (`200`, not
  `409`), then restoring.

## Verification beyond the automated pass

- Full gate run personally after all fixes, forced fresh (`-count=1`, not
  cached, per the ut-docs#215 lesson about trusting cached/skipped
  results): `go build ./...`, `go vet ./...`, `gofmt -l` on every touched
  file (clean), `go test ./... -count=1` — 38 packages, zero failures,
  `internal/pages` took a real ~148s (not a suspiciously-fast silent
  skip).
- All guards green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-plugin-menu-read.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` (the last one specifically
  re-run after the copy/screenshot fixes, since an earlier pass had gone
  stale).
- Manually grepped the new/changed locale strings and `sell.md` content
  for forbidden compliance-certification terms — none present (also
  covered mechanically by `guard-compliance-claims.sh`).
- Confirmed no real client/shop name and no secret-shaped literal in any
  new test fixture.
- Confirmed the two language-pack PRs' own `validate.sh` passes (valid
  JSON, every value a non-empty string) even though their `key-drift`
  check is expected-red until this PR merges.

## Outcome

All 4 blocking findings, plus 4 additional findings fixed for cheapness
and direct ADR relevance, resolved on the branch. Two genuine follow-ups
filed rather than silently dropped (`ut-docs#730`, `ut-docs#731`, the
second routed to the product owner as a real compliance-policy question).
Ready to merge.
