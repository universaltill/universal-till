# 2026-09-08 — Reject non-lowercase payment-entry manifest keys (ut-docs#1811)

## What shipped

`internal/plugins/manifest.go`'s `validatePaymentEntryKeys` now rejects a
payment entry key that isn't already its own lowercase form, alongside its
existing whitespace/`':'` format checks:

```go
if e.Key != strings.ToLower(e.Key) {
    return fmt.Errorf("payment entry key %q must be lowercase", e.Key)
}
```

**Why:** `FindPaymentKeyConflicts` (`internal/data/plugin_repo.go`)
compares candidate keys against existing ones with SQLite's default
case-sensitive `TEXT` collation, so a differently-cased key (e.g. `"OKC"`
alongside an existing `"okc"`) previously installed cleanly as a distinct
tender instead of being caught as a conflict — the exact gap filed as a
follow-up by the independent review of ut-docs#1795 (see
`2026-09-08-payment-method-case-canonicalization-1795.md`, "Findings
accepted as-is").

`TestPersistManifest_PaymentKeyFormatAndSelfUpgrade`
(`internal/plugins/payment_key_validation_test.go`) gets one new case:
installing a manifest with payment key `"OKC"` must fail with an error
naming the key.

## Independent review

Sonnet, fresh context. **Verdict: SAFE TO MERGE.**

### TDD claim — independently re-verified, not taken on trust

Reverted the fix hunk in `manifest.go` only (test file left with the new
`"OKC"` case), reran
`go test ./internal/plugins/ -run TestPersistManifest_PaymentKeyFormatAndSelfUpgrade -v`:

```
payment_key_validation_test.go:194: mixed-case payment key must be
rejected with a message naming the key, got: <nil>
--- FAIL: TestPersistManifest_PaymentKeyFormatAndSelfUpgrade (0.10s)
```

Confirms the described gap for real: pre-fix, `PersistManifest` accepts
`"OKC"` with `err == nil` — it installs cleanly next to any existing
lowercase key. Restored the fix (`git apply` of the saved diff) in the
same shell step (no turn boundary between revert and restore, per the
`reviewer` skill's worktree-hygiene note) and reran: `PASS`. Diff
confirmed byte-identical to the pre-revert version afterward.

### Correctness — does this close the gap in the issue

Yes. The new check is a pure string-format guard evaluated before any DB
round-trip, so a mixed-case key never reaches `FindPaymentKeyConflicts`
at all — it's rejected at manifest-parse time with a clear, actionable
message, which is strictly stronger than trying to case-fold the conflict
query itself (that would still leave two visually-different keys treated
as "the same," which isn't what the issue asks for — it asks for the
mixed-case key to not be installable in the first place).

### Regression check against existing manifests

Searched every bundled `plugin.json` for a `"type": "payment"` entry:
only `plugins/tax-tr/plugin.json` (`com.universaltill.tax-tr`) has one,
key `"okc"` — already lowercase, unaffected. Grepped all Go test fixtures
across `internal/data`, `internal/pages`, `internal/plugins` that
construct payment-type manifest entries (`paymentManifest(...)`,
literal `{Type: "payment", Key: ...}` structs) — every key used
(`cash`, `card_reader`, `mystery_tender`, `keyone`, `keytwo`) is already
lowercase, so nothing pre-existing regresses. Confirmed with a full
`go test ./...` run (green, no `-count=1` cache reused for the
`internal/plugins` package specifically).

### Check order

Placed after the `':'`-namespace-separator check and before the
`reservedTenderSentinelKeys` lookup. `reservedTenderSentinelKeys` already
does its own `strings.ToLower(e.Key)` before comparing, so a key like
`"UNKNOWN"` now fails on the new lowercase check first rather than
falling through to the reserved-sentinel message — both are valid
rejections of the same input; the format check running first (general
hygiene before semantic meaning) matches the existing order of the
whitespace/`':'` checks immediately above it, which also run before the
reserved-sentinel and collision checks.

### Error message style

Matches its immediate neighbors exactly — same shape as the line above
it:
```
payment entry key %q must not contain ':'
payment entry key %q must be lowercase
```

### i18n — confirmed NOT needed

Read `internal/plugins/install_status.go`'s `ClassifyInstallError` in
full. It only special-cases payment-key errors that contain the true
*collision* phrases ("collides with" / "belongs to plugin" / "is already
provided by plugin" / "is already used by plugin") — deliberately
excluding "the separate within-manifest-duplicate and malformed-key
validation errors, which stay on the generic retryable default" (its own
comment, ut-docs#169). The new `"must be lowercase"` message doesn't
match any of those phrases, so it falls through to
`plugins.install.error.retryable` ("Install failed. You can retry.")
exactly like the pre-existing empty-key/whitespace/`':'` checks
immediately above it in the same function. No new locale key needed, and
none was added — correct.

### Scope — payment-only vs. also touching `validatePageEntryKeys`

Correct as scoped to payment keys only, per the issue. Checked whether
`validatePageEntryKeys` has the identical theoretical gap:
`FindPageKeyConflicts` (`internal/data/plugin_repo.go`) does the same
plain `key IN (...)` SQLite comparison with no `COLLATE NOCASE`, so a
mixed-case page key could in principle install alongside an existing one
too. **Not folding it into this fix** — ut-docs#1811 as scoped is
payment-only, mirroring how ut-docs#1795's own review filed this as a
narrowly-scoped follow-up rather than a blanket "all entry types" card.
Noting it here as a real, separate, low-severity gap for a future card
(same shape, `validatePageEntryKeys`) rather than expanding this diff's
scope unasked.

### Manual / help topics / UX checklist

Not applicable — this is a plugin-install-time Go validation error
surfaced only through the existing generic retryable install-failure
message; no new page, route, template string, or screen changed. Skipped
per the `reviewer` skill's own carve-out for backend-only diffs.

## Verification beyond automated tests

- `gofmt -l internal/plugins/manifest.go internal/plugins/payment_key_validation_test.go`: clean.
- `go build ./...`: clean.
- `go vet ./...` (full repo): clean.
- `go test ./...` (full repo, all packages): green, no regressions.
- `golangci-lint run ./...` (full repo): 0 issues.
- `scripts/ci/guard-data-access.sh`: green (no SQL added or moved).
- Supersession check (per `reviewer` skill, rule 7(a)): `git log
  <merge-base>..origin/main -- internal/plugins/manifest.go
  internal/plugins/payment_key_validation_test.go` is empty — no
  independent fix has landed on `main` since this branch diverged; not
  superseded.
- Git identity on the landed commit: `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>` (the pipeline owner's
  real GitHub-linked address) — confirmed via `git log -1 --format='%an
  <%ae>'`, not an AI-tool default.

## Process note

The fix arrived already committed on the branch as a single commit
(`dbc025f`, "Reject non-lowercase payment-entry manifest keys") by the
time this review's TDD re-verification ran its revert/restore — the
environment's own stop-hook committed the working tree at a turn
boundary that landed after the revert-then-restore sequence had already
completed atomically within one shell invocation (no exposed window with
the fix reverted). Content verified identical before and after; nothing
broken landed. Recorded here per the same caution `docs/.claude/skills/
reviewer/SKILL.md` calls out (ut-docs#386) — this time the atomicity
requirement is exactly why it came out clean instead of catching a
reverted file mid-flight.

## Findings

None blocking. One out-of-scope observation recorded above (page-entry-key
case parallel gap in `validatePageEntryKeys` / `FindPageKeyConflicts`) —
worth a follow-up backlog card, not a defect in this diff.

## Verdict

**SAFE TO MERGE.**
