# Review: release notes v0.30.14

- Date: 2026-10-01. Author lane: lane:cloud-54 (Opus 5.5). Independent review: Sonnet, fresh context.
- Scope: `web/release-notes/{en,de,tr,fa,ar}/v0.30.14.md`, covering the 15 first-parent merges since v0.30.13.

## Findings and outcome
1. **High.** The "Privacy: personal data removed from every log" line overstated what shipped and repeated the v0.30.10 note. **Fixed:** replaced with a "Stability" line. Background-task errors are now logged (passwords and keys hidden) instead of stopping the till.
2. **Medium.** The open-sale restart-warning fix was missing. **Fixed:** added under Fixed in all locales.
3. **Medium.** The joined-till sale-screen line was imprecise. **Fixed:** the grid no longer reloads on unrelated sync changes.
4. **Medium.** Two "New" items depend on the cloud side. "Pictures from the cloud" was **dropped**: the my. image UI is still in Backlog, so owners can't use it yet. "Rename a till from the cloud" was **kept**: the close-out of its card confirms ut-cloud is deployed.
5. **Low.** Arabic used "جهاز" for till. **Fixed:** now "صندوق", consistent with earlier notes and ar.json.
6. **Low.** A Turkish sentence was ambiguous. **Fixed:** punctuation.
7. **Low.** The online manager wasn't named consistently. **Fixed:** the translations now use each locale's help term, and en names my.universaltill.com.
8. Number formatting was checked and is fine.

## Verified
- `scripts/ci/guard-release-notes.sh v0.30.14` passes.
- `go test ./internal/releasenotes/ ./internal/pages/ -run 'Release|Builtin'` passes.
- No card or PR numbers appear in the notes.

## Verdict
Safe to merge.
