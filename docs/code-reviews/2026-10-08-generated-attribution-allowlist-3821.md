# 2026-10-08 — Generated commit-author allowlist (ut-docs#3821)

## What shipped
`ALLOWED_IDS` and `ALLOWED_PLAIN_EMAILS` in `guard-commit-attribution.sh` are now `# devs:…` blocks. `python3 ut-docs/scripts/devs.py guards` renders them from ut-docs `developers/*.json`, and a local cycle start reports a stale copy. This repo can't read ut-docs, so the drift check runs there rather than in this repo's CI.

This copy already matched `developers/`. Only the per-entry comments change.

## Review
Fable, independently; the author was Opus 5.5. The full review covers all seven repos together: ut-docs `code-reviews/2026-10-08-generated-attribution-allowlists-3821.md`. For this repo it confirmed:
- the rendered array is valid bash;
- every known id is still present;
- the plain list is exactly what `developers/` allows;
- no credential or CI job was added.

## Verified
`guard-commit-attribution_test.sh` passes; `shellcheck` on the guard is clean.

## Verdict
Safe to merge.
