# Code review: name the English topic as owner in a manual route-conflict error

**Card:** universaltill/ut-docs#366
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), Review via an independent
fresh-context Sonnet subagent (isolated worktree). One review round;
nothing money/tax/data-loss/security-class was found, so a second round
wasn't earned per this pipeline's process-depth rule.

## What shipped

`internal/manual/manual.go`'s `Load()` builds its route→topic registry by
iterating locale directories in `fs.ReadDir` order (alphabetical: ar, en,
fa, tr). On a route conflict it named whichever topic was scanned
**first** as the "owner" in the error message — but routes are declared
canonically on the English topic (the package's own doc comment: "Routes
are declared on the English topic; a translation repeating them is fine,
but must not contradict."). Since `"ar"` sorts before `"en"`, a conflict
between an `ar` translation and the real `en` owner produced a misleading
error naming the `ar` file as the "owner", e.g. `route "/invoices"
claimed by both "invoices" (help/ar/invoices.md) and "catalog"
(help/en/catalog.md)` — backwards from what a human fixing the conflict
needs to know.

Fix (`internal/manual/manual.go` only, +17/-2):

- Added a `routeOwnerLocale map[string]string` (route → the locale that
  registered it), populated alongside the existing `lib.routes`/
  `routeOwner` maps.
- On a conflict, if the topic currently being processed is `en`
  (`FallbackLocale`) and the already-registered owner's locale isn't,
  swap which side of the error message is named "owner" — so the `en`
  topic is always named first, regardless of scan order. When neither
  side is `en` (e.g. an `ar`/`fa` conflict), naming stays order-dependent
  as before — the requirement is specifically about preferring `en`, not
  about ordering non-`en` pairs.
- Two new regression tests in `internal/manual/manual_test.go`:
  `TestLoadRouteConflictNamesEnglishTopicAsOwner` (the bug case, ar before
  en) and `TestLoadRouteConflictNamesEnglishTopicAsOwnerWhenEnScansFirst`
  (the pre-existing-correct case, en before fa, as a regression guard that
  the fix doesn't disturb it).

TDD: wrote both tests first, confirmed the ar-before-en case actually
failed against the unmodified code with the real (backwards) error text,
then implemented the fix and confirmed both pass.

## Independent review (Sonnet, fresh context, isolated worktree)

Read the diff fresh, ran `go build ./...`, `go vet ./...`,
`go test ./internal/manual/... -v`, the full `go test ./...`, and all five
CLAUDE.md-mandated guards (`guard-data-access.sh`, `guard-i18n.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-help-topics.sh`) — all green. Independently re-verified the TDD
claim by hand-reverting `Load`'s conflict-handling block to its pre-fix
shape (test file untouched), confirmed
`TestLoadRouteConflictNamesEnglishTopicAsOwner` fails with the exact
backwards message quoted above (a real assertion mismatch, not a compile
error), confirmed the en-first regression test still passes pre-fix
(proving that case was already correct), then restored the fix and
reconfirmed both pass.

Traced the swap condition against every locale-pair ordering (en-then-fa,
ar-then-en, a hypothetical future locale sorting before "en", ar-then-fa
with no en side, and a three-way conflict scenario) and confirmed:
`Load` returns on the first conflict detected, so `routeOwnerLocale`
never gets read/written in an inconsistent state; all three tracking maps
(`lib.routes`, `routeOwner`, `routeOwnerLocale`) are always written
together in the one success path, so there's no code path where one gets
set without the others; the ID/file variable pairs are swapped correctly
together, no mismatched swap; and the fix only changes which side is
*named* in the message — it never changes which topic wins a conflict
(both abort `Load` with an error either way).

Also traced all three call sites of `manual.Load` (`internal/pages/help_
page.go`'s `Library()`, `scripts/ci/checkhelptopics/main.go`) and
confirmed this error string is genuinely developer/CI-facing only — the
browser-facing handler responds with a hardcoded generic `"manual
unavailable"` on any `Load` error, never the detailed conflict message —
so no i18n treatment applies here, consistent with `guard-i18n.sh`
passing.

**Verdict: safe to merge — yes.** No findings.

## Verified beyond the automated suite

- Full `internal/manual` package test suite (27 tests) green, both before
  (with the bug reproduced) and after the fix, run independently by both
  Dev/Tester and the reviewer.
- Full `go build ./...`, `go vet ./...`, `go test ./...` (every package),
  and all five guard scripts — run in this session before handoff, and
  independently re-run in full by the reviewer in its isolated worktree.
- Scope confirmed clean: only `internal/manual/manual.go` and
  `internal/manual/manual_test.go` touched, nothing else.
- Confirmed (not assumed) this error message never reaches a shop-owner-
  facing page — only a build/CI/boot-time developer diagnostic — so no
  `web/help/` manual update and no README update are needed. No new
  user-facing string, no i18n key required.
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Safe-to-merge verdict

Yes, as shipped — no findings from the independent review.
