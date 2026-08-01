# 2026-08-01 — GoBD immutability/completeness assessment (audit log)

## Context

ut-docs#40 ("Verify audit log meets GoBD immutability requirements") asked
whether the audit log and broader financial-record handling satisfy GoBD's
immutability/completeness requirements — part of the German fiscal-
compliance surface tracked alongside #38 (TSE signing) and #39 (DSFinV-K
export). This is a verification task, not a feature: the deliverable is a
documented, evidence-backed finding, not a code fix — any fix for what was
found requires a product decision (see below), which is explicitly out of
scope for this pass.

## What shipped

One new file: `docs/gobd-audit-log-assessment.md`. No Go code, config, or
locale changes.

**Verified:**
- `audit_log` is append-only in application code — every write across
  `internal/data/plugin_repo.go` and `internal/data/pos_repo.go` is an
  `INSERT`; no `UPDATE`/`DELETE` against it exists anywhere.
- Normal sale lifecycle (void, catalog cleanup) preserves financial history
  correctly — voids flag `status`/`voided_at` rather than deleting;
  `CleanupObsoleteItems` explicitly excludes anything with sale/stock
  history.
- **Real gap found:** `POSRepo.ResetTransactionHistory`
  (`internal/data/pos_repo.go:1849`), behind
  `POST /api/data/reset-transactions`, permanently deletes every sale,
  payment, invoice, shift, stock-movement, and report-archive row with no
  code-level check tying it to pre-launch status — only a manager-role gate
  (itself bypassed entirely when `UT_AUTH=off`) plus a typed `RESET`
  confirmation. Currently latent (no real shop onboarded yet), but a real
  risk before the first one goes live.

Filed a new tracked card, **ut-docs#187** (`status:triage`, `p2`,
`compliance`, `needs-info`), asking the product owner which signal should
gate the reset (shop-claim status, a sale age/count threshold, or a manual
go-live toggle), with a recommended default (shop-claim status) — not
implemented here, deliberately deferred rather than guessed.

## Independent review

`general-purpose` subagent, `claude-opus-5` (different model from this
pipeline run). Given the exact diff scope, told to re-derive every claim
against the real code rather than trust the doc's summary, and to run
build/vet/test itself.

**Verdict: safe to merge**, after fixing what it found.

**Fixed (real-but-minor):**
- Internal inconsistency — the doc referenced the new card as both `#187`
  and (once) `#186`; the latter was a typo, now corrected everywhere.
- Finding 3 understated the gate: `isManagerOrAuthOff` returns `true`
  unconditionally when `UT_AUTH=off`, so with auth disabled there is *no*
  role check at all on the reset endpoint — only the typed confirmation.
  Now stated explicitly, and folded into the #187 card's comment thread
  so the eventual fix accounts for it.
- A cited line number was off by two (`pos_repo.go:1969` → `:1971`, the
  `obsoleteItemsWhere` predicate).
- Finding 1's list of `audit_log` `INSERT` sites named three of five call
  sites; the two inline sites (`pos_repo.go:444,486`) are now included.
  (The headline claim — every write is an `INSERT` — was correct either
  way; this only affects the parenthetical list's completeness.)

**Added (optional, taken):**
- Named two existing signals the #187 decision could build on
  (`enroll.Status.Registered`, and proposed ADR-0026's shop-profile step)
  rather than leaving the card fully open-ended.

**Confirmed correct (verified independently by the reviewer, re-deriving
from the real code, not trusting the doc):**
- Every substantive claim in Findings 1–3, re-checked against
  `internal/data/pos_repo.go`, `internal/data/plugin_repo.go`,
  `internal/pages/data_api.go`, `internal/pages/settings_page.go`, and
  `web/locales/en.json` directly.
- No real client/shop name or secret-shaped value in the new file
  ("Task Runner" is the vendor's own company name, used only in existing
  test fixtures/attribution strings elsewhere in the repo).
- No ADR needed — this records existing state and defers the one real
  policy decision to a separate card rather than making an architectural
  call itself.
- Doc placement (`universal-till/docs/`, alongside `data-model.md`,
  `performance.md`) is consistent with existing convention.

## Verification beyond the automated gate

- `go build ./...`, `go vet ./...` — clean (both by me and independently
  by the reviewer).
- `go test ./...` — one pre-existing failure,
  `TestSaveCleansUpDirectoryOnWriteFailure` (`internal/issuereport`),
  confirmed unrelated to this change: it also fails identically on
  unmodified `main`, and independently by the reviewer with this doc
  removed entirely from the working tree. Root cause: the test creates a
  `0o500` directory and expects a write to fail, but this sandbox runs as
  `uid=0` (root), which bypasses the DAC permission check; the till's
  production service runs as the unprivileged `pos` system user, where the
  real check holds. Not fixed here — out of scope for a docs-only change,
  and not a regression this diff introduced.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  (783 keys) — both clean.

## Safe-to-merge verdict

Yes. Zero risk to running code (single Markdown file); the one real
compliance gap found is deliberately not fixed in-line (it needs a product
decision) and is tracked on its own card with the question and a
recommended default already posted.

## Explicitly deferred

- The `ResetTransactionHistory` gating fix itself — ut-docs#187, pending
  product-owner input.
- `audit_log`'s lack of cryptographic tamper-evidence (hash chain/signing)
  — not a new finding, already owned by ut-docs#38's TSE-signing track.
