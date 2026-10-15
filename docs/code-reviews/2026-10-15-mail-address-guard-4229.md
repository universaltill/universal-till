# Review: mail-address allow-list guard (ut-docs#4229)

Date: 2026-10-15. This repo gets a byte-identical copy of
`guard-mail-addresses.sh` and its `_test.sh`, wired into CI. Where this repo
had dead `@universaltill.com` addresses, they were fixed in the same change.
The full review is in ut-docs `code-reviews/2026-10-15-mail-address-guard-4229.md`.
It covers the findings (one blocker: the guard flagged its own files; one
should-fix: git errors passed silently; both fixed), the mutation checks and
the deferred rollout (ut-docs#4405). Easy card: Sonnet built it and Opus 5.5
reviewed it in a fresh context.

Here: the guard and its self-test pass, and `shellcheck` is clean. Verdict:
safe to merge.
