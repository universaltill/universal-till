# Code review: stale EAN-13 barcode literal in tender-panel-reachable.spec.ts

**Date:** 2026-08-01
**Scope:** `e2e/tests/tender-panel-reachable.spec.ts` (1 line)
**Trigger:** ut-docs#188 (reviewed, CI-green PRs pile up unmerged) — swept
and merged 4 stale-but-reviewed `universal-till` PRs (#119, #122, #123,
#127) this cycle. After the sweep, `main`'s CI went red on the "UI E2E"
workflow.

## What broke

PR #122 ("seed valid EAN-13 check digits for demo catalog barcodes")
corrected the demo catalog's fabricated barcode check digits, and its own
PR body states it updated "5 Playwright e2e specs" that hardcoded the old
(invalid) barcode literal for the Coca-Cola Can 330ml item. It missed a
6th site: `e2e/tests/tender-panel-reachable.spec.ts`, still scanning the
stale `5000000000011`. Since the seed's real barcode is now
`5000000000012` (confirmed identical to what `rtl.spec.ts` and
`inventory-to-till.spec.ts` already use, and matching
`internal/db/migrations/001_init.sql`'s seed block), the stale literal no
longer matched any item — the test's basket assertion
(`toContainText('Coca-Cola')`) failed with "Item not found".

This is a cross-PR interaction: PR #122 was CI-green in isolation (its
own branch didn't contain this spec file's current content at time of
test), and this file's hardcoded literal was never exercised against
#122's new checksums until both landed on `main` together.

## The fix

One-line change: `'5000000000011'` → `'5000000000012'` in
`tender-panel-reachable.spec.ts` line 50, matching every other spec's
already-corrected barcode literal.

## Verification (self, before independent review)

- Reverted the fix, ran `npx playwright test tests/tender-panel-reachable.spec.ts`
  (with a local-only, uncommitted `executablePath` override for the
  sandboxed Chromium binary — not part of this diff): both subtests fail
  with the exact error from CI ("Item not found" / basket never shows
  Coca-Cola).
- Restored the fix, re-ran: both subtests pass.
- Full `e2e/` Playwright suite: 26/27 pass; the sole failure is
  `catalog-image-to-till.spec.ts`'s image-loading assertion — the same
  pre-existing, unrelated flake PR #122's own review record already
  documented as failing identically on unmodified `main`.
- `go build ./...`, `go test ./...`: clean except the standing
  pre-existing `internal/issuereport` root-in-container flake (documented
  in every recent PR in this repo).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green.
- Grepped the repo for any other reference to the stale `5000000000011`
  literal: none found — this was the only remaining site.

## Independent review

Different-model subagent (Opus), fully independent re-verification, not a
rubber stamp:
- Confirmed `5000000000012` is the sole valid EAN-13 for `itm001`
  (Coca-Cola Can 330ml) in the seed, and `5000000000011` collides with
  nothing else in `item_barcodes`/`variant_barcodes`/`shortcut_buttons`.
- Independently recomputed the EAN-13 checksum by hand (`500000000001` →
  check digit `2`) rather than trusting the claim.
- Reproduced the failure by reverting the fix locally and re-running the
  spec (1 failed / 1 passed, same "Item not found" error at the same
  assertion), then restored the fix and confirmed both subtests pass.
- Re-ran the full e2e suite (26/27 pass, same pre-existing
  `catalog-image-to-till.spec.ts` flake) and the Go build/vet/guard gate.
- Repo-wide grep for the stale literal found 5 more hits, all confirmed
  unrelated (own test fixtures in `cloudsync`, `print`, `catimport` — not
  reads of the demo seed).
- Chased one extra lead on its own initiative (whether `shortcut_buttons`
  seed codes have the same defect class) and correctly cleared it as
  intentional non-EAN "Designer tile" codes, not a missed site.
- No findings. Confirmed the fix doesn't mask a real product bug: the
  spec's actual purpose (proving payment buttons are real hit-test
  targets) is orthogonal to which barcode is scanned.

## Verdict

**Safe to merge.** Independent review found nothing to fix.

