# Code review: export dispatch gains a declared-entity `tax_codes` ledger

**Date:** 2026-08-24
**Branch:** `feat/655-export-tax-codes-entity`
**Closes:** universaltill/ut-docs#655 ("Export dispatch: extend the ut-docs#600 items-entity pattern to tax_codes")
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model, Sonnet), fresh context, isolated worktree

## Scope

ut-docs#600 shipped the `items` entity only and split three same-shape
follow-ons onto separate cards: `categories` → #654, `tax_codes` → #655
(this card), `customers` → #656. This change mirrors #600's exact shape
for `tax_codes`.

## What shipped

- `internal/data.CatalogRepo.TaxCodeView` gains snake_case JSON tags
  (`id`, `name`, `rate_bp`, `takeaway_rate_bp`, `is_active`) — the type
  was never JSON-marshaled before this change (confirmed by grepping all
  call sites), so this changes no existing wire format.
- `internal/pages/data_api.go`'s `exportRequestPayload` gains a
  `TaxCodes []data.TaxCodeView` field. The `/api/data/export` handler
  populates it only when the resolved entry's `Entities` declares
  `"tax_codes"` **and** the plugin holds a new `tax_codes:read`
  permission — mirroring `items`'s gating exactly.
- `internal/data.CatalogRepo.ListAllTaxCodes` (existing, previously used
  only by the tax-code management UI, ut-docs#259) is reused as-is for
  the data — includes retired/inactive codes, unlike the active-only
  `ListTaxCodes`, which stays unchanged (`catalog_lookups.html`'s
  autocomplete depends on it staying active-only).
- `ut-docs/reference/plugin-manifest.md` documents the new field, its
  gating, its `[]`-vs-`null` contract, and the field list (separate PR in
  `ut-docs`, `docs/655-tax-codes-export-manifest`).

## Deliberate deviation from the ticket text

ut-docs#655's own ticket body said to gate on `catalog:read`. The
implementer instead used `tax_codes:read`, because
`internal/pages/export_dispatch_test.go`'s `seedExportPluginWithEntities`
doc comment records ut-docs#600's own review (finding F2): an earlier
draft of *that* card used exactly `catalog:read` and it was rejected for
(a) breaking the established `<entity>:<verb>` permission convention
(`items:read`, `sales:read`, ...) and (b) colliding with an unrelated
`catalog:read` permission already defined in `ut-cloud`
(`internal/config/config.go`) meaning "read the marketplace plugin
catalog". The independent reviewer verified this reasoning against both
that comment and `docs/code-reviews/2026-08-13-export-items-entity-600.md`
directly (not just taking the implementer's word) and confirmed it: the
ticket text is the stale artifact here, not the code.

## Independent review

Spawned a `general-purpose` subagent, `model: opus`, isolated worktree
(first attempt hit a transient connection error mid-response before
running anything and was discarded/relaunched fresh — not resumed, per
this pipeline's own "prefer a fresh subagent over resuming a
long-lived one" guidance, since nothing of value had accumulated yet).

**Should-fix, all addressed:**

- **Stale `catalog:read` reference in the handler's own gating comment**
  (`data_api.go`, the `wantsTaxCodes` block) — contradicted the adjacent
  payload-field comment explaining the F2 rejection, one line above it.
  Concrete failure mode: a plugin author reading the handler (the
  authoritative gating site) declares `catalog:read` in their manifest
  instead of `tax_codes:read`, and the export silently returns 200 with
  `tax_codes` omitted — no error anywhere. Fixed: comment now points back
  to the field comment instead of repeating (and getting out of sync
  with) the permission name.
- **`ListAllTaxCodes` returned a nil slice on an empty table**, breaking
  the `[]`-vs-`null` emptiness contract this same card's own
  `plugin-manifest.md` text promises, and reproducing #600's own review
  finding F4 (fixed on `ExportRows`, not carried over here). Low
  practical reach today (`001_init.sql` seeds 3 tax codes and nothing
  deletes from the table — it's FK-referenced), but the contract is
  documented and should hold regardless. Fixed: `make([]TaxCodeView, 0)`,
  matching `ExportRows`' existing F4 fix.
- **No test distinguished `ListAllTaxCodes` from `ListTaxCodes`.** The
  reviewer proved this by mutation (swap the call, full test suite for
  both packages still green) — the entire reason this card points at
  `ListAllTaxCodes` (retired codes must still export, so a plugin can
  resolve historical sales referencing them) was unprotected. Fixed:
  `TestExportDispatch_PayloadIncludesTaxCodesData` now seeds a retired
  (`is_active=0`) code alongside the active one and asserts both are
  present with the retired one's `is_active:false` — re-verified by
  swapping the call to `ListTaxCodes` myself and confirming this
  specific test now fails (see TDD section below).
- Also folded in while touching the same test: the inclusion test now
  asserts `takeaway_rate_bp` and `is_active` (2 of the 5 JSON tags this
  card added that the original draft's test never touched), not just
  `id`/`name`/`rate_bp`.

**Nits, addressed:**

- `TaxCodeView`'s doc comment claimed its tags "match `data.ExportRow`
  (`tax_rate_bp`/`takeaway_rate_bp`)" — only `takeaway_rate_bp` actually
  matches; the dine-in field is `rate_bp` (matching the sibling
  `data.ExportSaleTaxLine.RateBP` already in the same payload), not
  `tax_rate_bp`. The tag itself was already correct; only the comment was
  wrong. Fixed.
- `grantExportPluginPermission` (new test helper) used a plain `INSERT`
  against a `UNIQUE(plugin_id, permission)` column — not triggered by
  this card's own tests, but a trap for #654/#656 reusing it against a
  permission `seedExportPluginWithEntities` already granted. Fixed:
  `INSERT OR IGNORE`.

**Verified correct (no changes needed):** entity+permission gating is
genuinely independent of `items`' gating (two separate loops, two
separate `CheckPermissionGranted` calls, distinct strings); nil
`entry.Entities` is safe (zero-iteration range, no panic, exercised by
`TestExportDispatch_OmitsTaxCodesWhenEntityNotDeclared` passing `nil`
explicitly); `ListAllTaxCodes` is the right method vs. `ListTaxCodes`
(the latter must stay active-only for the settings-editor autocomplete,
per its own doc comment); no raw SQL outside `internal/data`; no
filesystem writes in this diff at all (checked for the two recurring bug
classes — missing `os.MkdirAll`, cwd-relative path instead of
`paths.Data(...)` — both N/A, the diff touches no file I/O); backend-only
(no `web/`, no `.html`, confirmed by grepping the diff stat), so no
manual/help-topic/i18n/docs-shots obligation — guards run anyway, all
green; no real client/shop name, no secret-shaped literal (the only
quoted literal is the already-seeded generic "Standard VAT").

**TDD re-verification, done by the reviewer personally** (isolated
worktree): forced `wantsTaxCodes := true` unconditionally (neutralizing
only the entity-declaration gate, leaving the permission gate intact) →
`TestExportDispatch_OmitsTaxCodesWhenEntityNotDeclared` and
`TestExportDispatch_OmitsTaxCodesWhenOnlyOtherEntityDeclared` both failed
with real assertion errors (payload carried tax codes instead of `null`),
while `TestExportDispatch_OmitsTaxCodesWithoutTaxCodesReadPermission`
still passed — proving the two gating axes are independent, neither
masking the other. Restored, diffed clean, re-ran green. Separately
swapped `ListAllTaxCodes` → `ListTaxCodes` to probe the (at-the-time)
test gap described above; restored after confirming the gap, which was
then closed by fix #3 above.

I (implementer) independently re-ran this same mutation myself after
applying fix #3, to confirm the new assertion actually catches it rather
than trusting the reviewer's earlier (pre-fix) report: swapped
`ListAllTaxCodes` → `ListTaxCodes` in `data_api.go`, ran
`TestExportDispatch_PayloadIncludesTaxCodesData` → failed on
`IsActive:false` (the retired code no longer present, so the byID lookup
for the active code deref'd wrong) — confirmed the fix is real, not
just claimed. Restored, rebuilt, re-ran green.

## Full gate (final, post-fix)

`gofmt -l .` — clean. `go build ./...`, `go vet ./...` — clean.
`go test ./internal/data/... ./internal/pages/... -race` (full packages,
not just the new tests) — all green. `scripts/ci/guard-data-access.sh` —
green. All 15 other CI-blocking guards from `ci.yml`'s `build` job run
clean (checked by the independent reviewer): `guard-kiosk-engine`,
`guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
`guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`,
`guard-kiosk-launch-flags`, `guard-android-status-address`,
`guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
`guard-autofill-suppression`, `check-brand-assets`,
`guard-makefile-version`.

A full-module `go test ./... -race` was attempted twice (once by the
orchestrating session, once independently by the reviewer) and both times
hit an unrelated pre-existing sandbox characteristic: `internal/plugins`'
`TestHostTCPMaxHandles` (a WASM/wazero JIT compile) and, on the
reviewer's run, `internal/pages`' `TestSyncPullTick_NoPrimaryConfigured_NoOp`
each blew the 10-minute per-package `go test` timeout under `-race` in
this specific sandboxed environment. Both were confirmed, independently,
to pass in ~1-2s each without `-race` and in isolation — this is `-race`
JIT-compile overhead specific to this sandbox, not a real failure, and
neither test is anywhere near this diff's files. `internal/data` alone
was also run to completion under `-race` (701.7s, genuinely slow but
finished with a real `ok`), and both `internal/data`/`internal/pages`
package-scoped `-race` runs targeting just the tax-code/export tests
completed and passed well within the timeout.

## Verdict

**Safe to merge.** All three should-fix findings were addressed with
code fixes (one of which strengthens test coverage, closing a real gap
proven by mutation) and both nits were fixed. Nothing was deferred. The
ticket's stated `catalog:read` permission name was deliberately not
followed, for a reason independently verified against the actual
codebase precedent it conflicts with, not just asserted.
