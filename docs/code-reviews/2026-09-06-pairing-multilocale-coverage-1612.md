# Code review: multi-locale coverage for pairing error messages (ut-docs#1612)

- **Date**: 2026-09-06
- **Card**: universaltill/ut-docs#1612
- **Repo/branch**: `universal-till`, `test/1612-pairing-multilocale-coverage`
- **Complexity**: easy — Dev inline at Sonnet, review via a fresh-context
  Sonnet subagent (per this card's routing)

## What shipped

Test-only change, no production code touched. `internal/pages/
pairing_join_test.go`'s `TestPairStart_SurfacesUnreachablePrimary`,
`TestPairStart_RejectsInvalidBaseURL`, and `TestPairStatus_ErrorStateKeeps
PollingAndKeepsItsMessage` only ever drove `pairStartHandler`/
`pairStatusHandler` with the default "en" locale, even though the handlers
correctly thread `httpx.ResolveLocale`/`httpx.T` (confirmed by code reading
in the ut-docs#1544 review) — a coverage gap, not a live bug: a regression
that hardcoded the English string back into one of these branches would
slip past every existing test for a non-English operator.

- New `TestPairStart_SurfacesUnreachablePrimary_MultiLocale`, mirroring
  `TestFriendlyJoinError_TranslatesEachKind`'s `for _, locale :=
  range []string{"en","ar","fa","tr"}` loop, driving `pairStartHandler`'s
  unreachable-primary branch via `?lang=<locale>` (the same precedence
  `httpx.ResolveLocale` reads: query param before the `ut_lang` cookie) and
  asserting the rendered body contains the locale-specific translated
  prefix of `tills.pairing.error.unreachable`.
- No new locale keys — all four locale files already carry this key
  (added under ut-docs#1544).

## Independent review — findings and disposition

Reviewed by a fresh-context Sonnet subagent (no shared conversation with
the implementer), instructed to run everything itself and independently
re-verify the false-pass risk via hardcode→confirm-real-failure→revert
rather than take it on faith.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| B1 | **Blocker** | The new function's doc comment was pasted directly after the pre-existing `TestPairStart_RejectsInvalidBaseURL` doc comment with no blank line separating them. Go attaches a comment block with no blank line to the *next* declaration, so the combined 13-line comment attached to the new function instead — `TestPairStart_SurfacesUnreachablePrimary_MultiLocale`'s doc wrongly opened describing base_url validation, and `TestPairStart_RejectsInvalidBaseURL` was left with no doc comment, orphaning its CLAUDE.md-citing rationale ("validate all external input") from the function it explains. Didn't fail build/vet/gofmt/lint (`.golangci.yml` here only enables `unused`), but a real documentation defect. | **Fixed**: reordered so the new function (with its own doc comment) sits before `TestPairStart_RejectsInvalidBaseURL`, with a blank line separating the two comment blocks so each attaches to its own function. Re-verified build/vet/test/gofmt/lint all still clean after the reorder. |

No other findings — reviewer confirmed: the new test genuinely exercises
`pairStartHandler`'s real `httpx.ResolveLocale`/`httpx.T` path (not a stub);
the `wantPrefix == key` guard correctly catches a missing-translation
fallback, mirroring `TestFriendlyJoinError_TranslatesEachKind`'s own
`want == tt.key` check; reusing `postPairStart` wasn't practical (it
hardcodes the URL with no `?lang=` support) so the new test builds its own
request directly — judged non-blocking, not worth threading a `lang` param
through the file's eight other call sites for one new test; no goroutines/
shared state/parallelism concerns; "Bar Till" is pre-existing fixture data
used identically at 8 other call sites before this diff, not a real
client/shop name; no SQL, money, kiosk-engine, or compliance-wording
surfaces touched; no i18n keys added so `guard-i18n.sh` is unaffected (and
was confirmed still green); no UI/help-manual/ADR implications — this is a
pure test-only diff.

## False-pass verification (done independently, twice)

Both the implementer and the review subagent independently hardcoded
`pairStartHandler`'s unreachable-primary branch (`internal/pages/
pairing_join.go`) to `httpx.T("en", "tills.pairing.error.unreachable")`
instead of the resolved `locale`, confirmed the new test's `en` subtest
still passes (the hardcode happens to match) but `ar`/`fa`/`tr` genuinely
fail (the body renders the English-prefixed string where the
locale-specific prefix was expected), then reverted the edit
(`git diff internal/pages/pairing_join.go` empty afterward) and confirmed
all four locale subtests pass again. The test is not a false-pass.

## Verified beyond automated tests

- `gofmt -l internal/pages/pairing_join_test.go` — empty, both before and
  after the B1 fix.
- `go build ./...`, `go vet ./internal/pages/...` — clean.
- `go test ./internal/pages/...` — full package green (all pre-existing
  tests plus the new one); `go test ./...` — full repo suite green.
- `golangci-lint run ./...` (full repo) — 0 issues.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally — all green (this is a Go-test-only diff; the i18n/compliance/
  kiosk-engine/data-access/docs-shots/help-topics guards are all
  unaffected but were run in full regardless).
- Git identity verified before both commits in this cycle (the initial
  commit and the B1-fix amend): `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>` — a real, GitHub-linked
  human identity, never an AI-tool identity.

## Explicitly deferred

None — the one finding (B1) was fixed in this same diff, in scope.

## Verdict

**Safe to merge.** The one Blocker (misattached doc comment) is fixed and
re-verified; the false-pass risk was independently ruled out twice; full
gate green (build/vet/test/lint/guards); no production code touched, so no
behavioral risk.
