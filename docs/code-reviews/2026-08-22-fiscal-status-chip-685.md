# Fiscalisation status chip on the sell-screen nav (ut-docs#685)

**Reviewer**: independent Opus instance, fresh context, isolated worktree
(a different model from the one that wrote the code).
**Branch**: `feat/685-fiscal-status-chip` (base: `main` @ `2deb818`, the
merge commit for PR #433).
**Date**: 2026-08-22.

## What shipped

A nav status chip for fiscal signing, mirroring the existing `/ui/sync-chip`
pattern (`internal/pages/sync_admin.go` + `web/ui/partials/sync_chip.html`).

- **`internal/data/pos_repo.go`** — two new `POSRepo` methods:
  `LatestLocalSaleID` (this till's own most recent sale) and
  `CountUnresolvedAuditActionsSince` (distinct entities of a given
  `entity_type` carrying any of a set of audit actions at/after a bound,
  excluding entities that also carry the historical `fiscal_signing_resolved`
  marker). Both keep the raw SQL inside `internal/data` per the repository
  rule.
- **`internal/pages/fiscal_api.go`** — `GET /ui/fiscal-chip`. Renders nothing
  when `fiscal.tse_configured` is not truthy; otherwise `ok`/`warn` from the
  till's own audit trail, with an unresolved-gap count for the current
  business day.
- **`web/ui/partials/fiscal_chip.html`** (new), **`web/ui/partials/nav.html`**
  (wiring, `hx-trigger="load, every 30s"` — identical to the sync chip),
  **`web/public/app.css`** (`.fiscal-chip` pill, added by `da16114` after the
  Tester found the chip rendering as unstyled bare text).
- **`web/locales/{en,ar,fa,tr}.json`** — five new `fiscal.chip_*` keys.
- **`web/help/{en,ar,fa,tr}/sell.md`** — a new "fiscal signing status chip"
  paragraph, plus regenerated `web/help/img/**` and `manifest.json`.

### Ground truth the design rests on — independently re-verified

- **There is no `fiscal.status.ask` extension point.** Confirmed:
  `grep -rn "fiscal.status" internal/ web/` returns only the two new comments
  that say so. ADR-0044 registers `fiscal.sign.ask` only. Sourcing the chip
  from the till's own audit trail is therefore correct, not a shortcut.
- **ADR-0056 removed the background re-sign retry**, so a signing gap is
  permanent on its own sale and nothing ever clears it
  (`fiscal_sign_hook_test.go:538` even pins that no code path writes
  `fiscal_signing_resolved` any more). Windowing the count to the current
  business day rather than all-time is the right consequence — an all-time
  counter would only ever grow.
- **`saleFiscalSigningGapKind` reuse is correct**: right function, right
  semantics (returns the gap's audit action name, `""` when there is none,
  and degrades conservatively on read errors). The chip only needs `!= ""`.
- **Business-day boundary matches `parseReportWindow`'s `"day"` case
  exactly.** The chip computes
  `time.Date(anchor.Year(), …, hh, mm, 0, 0, anchor.Location())`;
  `parseReportWindow` computes `time.Date(y, m, d, hh, mm, 0, 0, time.Local)`.
  `businessDateFor(reportNow(), …)` returns a time in `reportNow()`'s
  location, which is `time.Local` — so the two are identical, including DST
  normalisation. No off-by-one, no timezone drift.

## Findings

### 1. BLOCKING (fixed here) — `LatestSaleID` picked a *replica's* sale, silently flipping the chip green

`LatestSaleID` was `SELECT id FROM sales ORDER BY created_at DESC LIMIT 1` —
unfiltered by till.

On a **primary** till in a multi-till shop (ADR-0011), `sales` also holds
every replica's journaled-in sale: `sync_sales.go`'s `applyJournal` replays it
through `pos.CompleteSale` and then `POSRepo.SetSaleProvenance` stamps it with
the source `till_id` **and the origin's own `created_at`**. So the newest row
in `sales` is routinely a *foreign* sale.

That foreign sale can never carry a local `unsigned_fiscal_signing` marker,
because `declareUnsignedFiscalSale` is only reached from `pos_api.go`'s
`completeTender` — the local tender path — never from `applyJournal`. The
result: the primary's own last sale completes unsigned, the chip goes `warn`,
and then the very next replica journal push flips it back to
**"✓ Fiscal signing OK"**. A silent false green on precisely the condition
the chip exists to surface, and directly contradicting the manual text this
branch ships in all four languages ("the most recent sale on **this till**").

This is not theoretical — reproduced in a test before fixing (see
"TDD re-verification" below); the failure output is literally
`<span class="fiscal-chip ok">…✓ Fiscal signing OK</span>` with an unsigned
local sale on the books.

**Fixed**: renamed to `LatestLocalSaleID` and filtered on `till_id = ''` —
the same "this till's own sales" predicate `LocalSalesSince` already uses,
with its ADR-0011 D3 rationale. Also added `, rowid DESC` as a tiebreak:
`created_at` is second-granularity RFC3339, so two sales in the same second
otherwise gave SQLite a free choice and the chip could flicker between `warn`
and `ok` across successive 30s polls. Handler updated to call it, with the
reason recorded inline.

Two regression tests added, both verified to fail without the fix:
- `TestPOSRepo_LatestLocalSaleID` — a `till_id='till-b'` row with a later
  `created_at` must not win; plus a same-second tie must resolve stably.
- `TestFiscalChip_ForeignJournaledSaleDoesNotClearWarn` — end-to-end through
  the handler: a journaled-in foreign sale must not clear this till's `warn`.

### 2. Minor (fixed here) — the unresolved-gap count was queried on the healthy path but never rendered

`fiscal_chip.html` only renders `.count` inside its `warn` branch, but the
handler ran `CountUnresolvedAuditActionsSince` unconditionally — a wasted
`audit_log` scan on every 30s poll, on every open page, on every healthy till.
Moved the settings read + business-day computation + count query inside
`if class == "warn"`. Output is byte-identical; the healthy path no longer
pays for it.

### 3. Minor (fixed here) — a weak assertion in `TestFiscalChip_..._RendersWarnWithCount`

The count assertion was `strings.Contains(body, strconv.Itoa(1))` — a
substring search for a bare `"1"` anywhere in the markup, which would pass on
any incidental digit. Tightened to the rendered separator+count (`"· 1 "`),
and dropped the now-unused `strconv` import.

### 4. Accepted as-is — the `warn` label collapses "cannot sign" into "unavailable"

`saleFiscalSigningGapKind` deliberately returns *which* kind of gap it is,
because ut-docs#835 / ADR-0044 require that a signer's deterministic refusal
(`unsigned_fiscal_cannot_sign`) is never worded as a connectivity problem —
the receipt renderers (`pos_api.go`, `print_api.go`) both honour that split.
The chip collapses both into `fiscal.chip_degraded` ("Fiscal signing
unavailable" / "TSE imzalama kullanılamıyor" / …), which reads as
*unreachable* for a sale that was refused outright.

Not blocking: the legally-material surface (the customer receipt) still
distinguishes correctly, and the chip's own `title` tooltip is
outcome-neutral and accurate ("The most recent sale on this till completed
without a TSE signature"). Splitting the short label needs a product-approved
wording in four locales plus a matching manual + screenshot pass, which is a
BA/product call, not a reviewer edit. **Recommended as a new Backlog card.**

### 5. Accepted as-is — the chip does not reflect `fiscal.tse_failing_since` or an active owner override

`fiscal.EvaluateGate` (ADR-0048) already tracks a *configured-but-failing*
TSE and a live owner override; the chip only ever reacts *after* a sale has
already completed unsigned. Surfacing the gate's own state would let the chip
warn before the first gap rather than after it. Out of scope for #685 as
specified. **Recommended as a new Backlog card.**

### 6. Non-goals confirmed as real gaps, not built here

Both are genuine and correctly deferred:
- **Ownership-aware wording (ADR-0045)** — there is no settings
  representation of the ownership model in code yet, so there is nothing to
  key wording off. File as Backlog.
- **Vendor status-page link** — no `status_url` field exists on the plugin
  manifest. File as Backlog. (Note the sync chip *is* a link to `/tills`; a
  local link from the fiscal chip to the audit trail would be a cheaper win
  than a vendor link, and worth folding into that card.)

### 7. Checked and clean — no findings

- **Repository pattern**: all new SQL is inside `internal/data/pos_repo.go`.
  `guard-data-access.sh` passes.
- **SQL injection**: `CountUnresolvedAuditActionsSince` builds *only* the
  placeholder count from `len(actions)`; `entityType` and every action are
  bound parameters, never concatenated. Placeholder trimming is correct for
  both `len == 1` (`"?,"` → `"?"`) and `len >= 2` (`"?,?,"` → `"?,?"`), and
  the `len(actions) == 0` guard prevents the slice underflow that would
  otherwise panic on the trim. Argument order —
  `entityType, actions…, since, entityType` — matches the query text's
  parameter order exactly (outer `entity_type`, `IN (…)`, `created_at >= ?`,
  subquery `r.entity_type`).
- **String-comparison time bound**: `since.UTC().Format(time.RFC3339)`
  compared against `audit_log.created_at` is sound because
  `declareUnsignedFiscalSale` — the *only* writer of either gap action —
  writes `time.Now().UTC().Format(time.RFC3339)`. Same fixed-width UTC shape,
  so lexicographic ordering is chronological. The test comment already flags
  the `datetime('now')` shape hazard; verified it does not apply to these two
  actions.
- **Offline-first**: the handler does zero network I/O — settings + local
  SQLite only. It cannot block or slow checkout, and the chip is a status
  surface, never a modal.
- **Auth**: `/ui/fiscal-chip` is not in `internal/auth/middleware.go`'s
  `exempt()` list, so it requires a session — the gap count is not readable by
  an anonymous LAN client. Not a `/self-order` route;
  `guard-kiosk-engine.sh` passes.
- **i18n**: every string in `fiscal_chip.html` goes through `{{ T … }}`; no
  literals. All five keys exist in all four locales. Translations read
  correctly and idiomatically in `ar`/`fa`/`tr` (checked by reading them, not
  just trusting the guard's key-parity check) — e.g. ar
  `"آخر عملية بيع على هذا الجهاز اكتملت بدون توقيع TSE"` and tr
  `"Bu kasadaki en son satış TSE imzası olmadan tamamlandı"` are both faithful
  renderings of the en source. The locales say "TSE" where en says "fiscal
  signing", which matches those locales' existing `receipt.fiscal.*` wording.
- **RTL**: `.fiscal-chip` uses `margin-inline-start`; no `left`/`right`
  anywhere in the new CSS. The `· {count} {label}` shape is the same one
  `.sync-chip` already ships in RTL.
- **CSS fix (`da16114`) is correct and complete**: `.fiscal-chip` is a
  field-for-field mirror of `.sync-chip` (`display`, `align-items`,
  `margin-inline-start: .6rem`, `padding: .15rem .55rem`,
  `border-radius: 999px`, `font-size: .8rem`, `font-weight: 600`) and uses the
  same `rgba(…, .12)` tint + `var(--success)` / `var(--warning)` pair that
  ut-docs#405's contrast fix established. Its comment correctly notes the
  `.nav .sync-chip a` specificity workaround is unnecessary here — verified:
  the partial contains no `<a>`, and none of the four themes
  (`amber`/`fresh`/`monarch`/`slate`) defines a `.nav span` rule that could
  outrank the chip's own colour, nor overrides `--success`/`--warning`. There
  is no `prefers-color-scheme` / `data-theme` mechanism in this codebase at
  all, so "theme-correct in dark mode" reduces to "correct across the four
  brand themes", which it is.
- **Recurring bug classes** (checked, both not applicable): no file writes
  anywhere in the diff, so no missing `os.MkdirAll`; no filesystem paths at
  all, so no cwd-relative path in place of `paths.Data(…)` — the template is
  served by `httpx.RenderPartial`, which reads from the embedded FS (pinned by
  `TestRenderPartialWorksFromAnyWorkingDirectory`).
- **No real client/shop names** used as demo or test data (`ABC`, `s1`–`s3`,
  `till-b`, `R-FOREIGN`), and **no secret-shaped literal** anywhere in the
  diff.
- **Manual accuracy**: the new `sell.md` paragraph in all four languages
  describes the shipped behaviour correctly — the two states, the count, the
  "nothing at all when no TSE is configured" zero state, and "never blocks the
  sale screen, never depends on reaching the network". Its claim that the warn
  state reflects "the most recent sale on **this till**" was *false* before
  finding 1 was fixed and is true now. Screenshot regeneration is real:
  `manifest.json`'s `surface_sha256` moved `5924cf9a…` → `9174e91d…` and all
  four `sell` topic hashes changed; `guard-docs-shots.sh` confirms the
  committed manifest matches the current source. (My own fixes touch
  `internal/data/**` and `internal/pages/fiscal_api.go`; the latter is
  excluded from the docs-shots surface set under ut-docs#620 because all its
  registered routes are unscreenshotted, so no regeneration was needed — the
  guard re-run confirms the surface hash is unchanged.)

## What I verified beyond reading the diff

Everything below was run in this worktree.

**Build/format/vet/tests** — after my fixes, all clean:
`gofmt -l .` (no output), `go build ./...`, `go vet ./...`,
`go test ./...` (whole repo, zero failures).

**TDD re-verification — mutate, watch it fail, restore, watch it pass.**
Not taken on the implementer's word; each mutation produced a real assertion
failure, never a compile error:

| Mutation | Result |
| --- | --- |
| `CountUnresolvedAuditActionsSince` body gutted to `return 0, nil` | `expected 2 unresolved gapped sales in-window, got 0` |
| `AND NOT EXISTS (… fiscal_signing_resolved …)` clause + its arg removed | `expected 2 unresolved gapped sales in-window, got 3` |
| `LatestLocalSaleID` `ORDER BY … DESC` → `ASC` | `expected the most recently created sale s2, got id="s3"` |
| `WHERE till_id = ''` removed (my own fix, reverted) | repo: `a replica's journaled-in sale must not win: got id="foreign"`; handler: `a foreign journaled-in sale must not clear this till's warn state, got "<span class=\"fiscal-chip ok\">…✓ Fiscal signing OK…"` |

All four restored afterwards and re-confirmed passing.

**Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job**, run
individually against the post-fix tree — all pass:
`guard-data-access.sh` (+ `_test`), `guard-kiosk-engine.sh` (+ `_test`),
`guard-plugin-menu-read.sh` (+ `_test`), `guard-i18n.sh` (+ `_test`,
`_toast_test`), `guard-compliance-claims.sh` (+ `_test`),
`guard-docs-shots.sh` (+ `_test`), `guard-help-topics.sh`,
`guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
`guard-android-status-address.sh`, `guard-android-i18n.sh`,
`guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh` (+ `_test`), `check-brand-assets.sh`,
`guard-makefile-version.sh`.

Note: `lang-pack-drift` will flag the five new `en.json` keys as missing from
the external `ut-plugin-language-{de,es}` packs. That is advisory-only on a PR
by design, and blocking on `main` — the packs need the five `fiscal.chip_*`
keys as a follow-up.

## Verdict

**Safe to merge**, with finding 1 fixed on this branch. Findings 4, 5 and 6
are recommended as new Backlog cards; none of them blocks this change.
