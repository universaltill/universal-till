# Code review: e2e Makefile ambiguous wildcard bind (ut-docs#2253)

**Date:** 2026-09-19
**Card:** ut-docs#2253 (follow-up from the independent review of ut-docs#2252)
**PR:** universaltill/universal-till (this branch, `fix/2253-e2e-makefile-loopback-bind`)

## What shipped

One-line fix: `Makefile`'s `e2e:` target started the app with the ambiguous
`UT_LISTEN_ADDR=:8080` wildcard bind, then health-checked and ran Playwright
against the explicit IPv4 loopback address `http://127.0.0.1:8080` — the
same wildcard-vs-loopback mismatch ut-docs#2252 already fixed in
`.github/workflows/ci.yml` and `tests/e2e/README.md`. Changed to
`UT_LISTEN_ADDR=127.0.0.1:8080` so the app binds the same address the
health check and Playwright already probe.

Not currently exercised by CI (`ci.yml` duplicates the "Start app" step
inline rather than calling `make e2e`), so this is a local-dev-only fix —
real but low severity, per the card's own framing.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → Sonnet review, per
`scrum-master`'s model-routing table), worktree-isolated. Verified:

- Diff touches exactly the one line in `Makefile`'s `e2e:` target.
- `grep`/`git grep` for the old `UT_LISTEN_ADDR=:8080` string confirms
  every remaining occurrence (`Dockerfile`, `pos.env`/`pos.env.dev`/
  `pos.env.example`, root `README.md`,
  `specs/000-pos-core-mvp/quickstart.md`, `mobile/mobile.go`'s comment) is
  exactly the card's explicit non-goal list — the intentional production
  wildcard default per `docs/code-reviews/2026-08-27-no-wildcard-fallback-bind-1169.md`.
- `make -n e2e` expands cleanly with the new address, matching the
  health-check curl and Playwright's `BASE_URL`.
- `gofmt -l .` and `go build ./...` clean (no Go code touched).
- Cross-checked against the sibling `docs/code-reviews/2026-09-14-ci-e2e-listen-addr-ipv6-2252.md`
  review record, which explicitly named this Makefile target as the
  deferred follow-up — this change completes it.

**No blockers, no should-fix items, no nits.** Verdict: safe to merge.

## Verified beyond automated tests

`make -n e2e` (dry-run expansion) — no live Playwright run was driven for
this change since it's a one-line config-string substitution with a direct
precedent (#2252) already verified the same way; the fix makes the bind
address literally equal to the address already being probed, which is the
whole content of the change.

## Safe-to-merge verdict

Yes.
