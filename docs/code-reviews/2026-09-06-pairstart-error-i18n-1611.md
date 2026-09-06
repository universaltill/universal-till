# Code review: pairStartHandler request-build error i18n (ut-docs#1611)

- **Date**: 2026-09-06
- **Card**: universaltill/ut-docs#1611
- **Repo/branch**: `universal-till`, `fix/1611-pairstart-error-i18n`
- **Complexity**: easy — Dev inline at Sonnet, review via a fresh-context
  Sonnet subagent (isolated worktree)

## What shipped

`internal/pages/pairing_join.go`'s `pairStartHandler` had one error branch —
the `http.NewRequestWithContext` failure path, right after generating the
verification-code commitment — that still rendered a raw Go `err.Error()`
string straight to the operator, the same defect class ut-docs#1544 already
fixed for this handler's other error branches (missing-name, invalid
address, unreachable primary). Found as a deliberate follow-up note left on
ut-docs#1544's own review (2026-09-05): not one of that card's six targeted
strings, left alone deliberately, "realistically very hard to reach today."

- The branch now calls `httpx.T(locale, "tills.pairing.error.request_build_failed")`
  instead of `err.Error()` — `locale` is the same variable already resolved
  once near the top of the function (`httpx.ResolveLocale`), reused, not
  re-resolved.
- New locale key `tills.pairing.error.request_build_failed` added to
  `web/locales/en.json` ("Couldn't prepare the pairing request. Please try
  again."), and to `ar.json`/`fa.json`/`tr.json` — translated in this
  session, matching the tone/brevity of the neighboring
  `tills.pairing.error.*` keys, alphabetically positioned between `.refused`
  and `.unexpected_response` in all four files.
- No `docs-shots` regeneration needed — this diff touches only
  `internal/pages/pairing_join.go` and the four locale JSON files, none of
  `web/ui/**`/`web/public/**`.
- **Follow-up obligation, not yet done**: per `universal-till/CLAUDE.md`,
  adding a `web/locales/en.json` key needs a matching follow-up in the
  external `ut-plugin-language-{de,es}` packs. `lang-pack-drift` CI is
  **advisory-only** on this PR (it touches `en.json`) and will show the
  exact missing key in its Actions job summary rather than blocking this
  merge — but it **is blocking on push to `main`**, so whoever lands the
  `de`/`es` packs' own PR for this key should do so promptly; noting it
  here rather than assuming the advisory warning alone will get it done.

## Independent review — findings and disposition

Reviewed by a fresh-context Sonnet subagent in an isolated git worktree
(`isolation: "worktree"` — this card is `complexity:easy`, where "different
model" relaxes to "different instance" per the reviewer skill), instructed
to run everything itself and to independently try to disprove the
implementer's "practically unreachable" claim about the untested branch,
not take it on faith.

**No findings, blocker or non-blocker, on the code itself.** One process
note: this review record didn't exist yet at review time (expected — it's
written as this step). No other issues.

Also independently re-verified rather than taken on faith:
- **The "unreachable" claim.** The reviewer wrote a throwaway Go program
  and tried ~60 candidate `baseURL` strings against `validPrimaryBaseURL`
  followed by the actual `http.NewRequestWithContext` call — control
  characters, malformed/incomplete percent-encoding, malformed IPv6
  literals, userinfo forms, invalid ports, Unicode bidi-override and
  zero-width characters, invalid raw UTF-8, oversized hosts, fullwidth
  digits, NBSP, `%2f`/`%c0%ae` encodings, empty-authority forms. Every
  string that passed `validPrimaryBaseURL` also survived the second parse;
  everything that would fail the second parse was already rejected by
  `validPrimaryBaseURL`'s own identical `url.Parse` call first (I had
  independently found the same result during implementation with a smaller
  set of ~9 candidates). No counterexample exists — the coverage gap on
  this specific branch is genuinely real-but-accepted, not a shortcut.
- `err.Error()` is fully removed from this branch (not just moved
  elsewhere).
- English string tone/brevity matches neighbors; Arabic/Farsi/Turkish
  translations checked for correct script, no mismatched brackets or
  leftover placeholder text, plausible grammar; alphabetical key placement
  correct in all four files.
- No existing `TestPairStart_*` test is put at risk — `TestPairStart_
  SurfacesUnreachablePrimary` exercises the *different* `client.Do` failure
  branch (an actually-unreachable host), not the `NewRequestWithContext`
  branch this diff touches, so there's no overlap.
- Author/committer identity: `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>` for both — a real,
  GitHub-linked human identity, not an AI-tool identity.

## TDD discipline note (why there's no revert→restore evidence here)

This card's fix is a straight literal substitution (`err.Error()` →
`httpx.T(...)`) behind a branch that both the implementer and the
independent reviewer separately confirmed — via two different empirical
searches — cannot be reached through any `baseURL` value that clears the
existing `validPrimaryBaseURL` gate one line above it. Per the tester
skill's explicit allowance, this is reported plainly as a real-but-accepted
gap rather than manufactured via disproportionate mocking (e.g. swapping
`http.NewRequestWithContext` for an injectable seam purely to force this
one line, which would add a permanent test seam to production code for a
branch that cannot fire in practice). All of this handler's OTHER branches
retain their existing TDD-backed regression tests
(`TestPairStart_RejectsInvalidBaseURL`,
`TestPairStart_MissingNameRendersTranslatedFieldSpecificMessage`,
`TestPairStart_SurfacesUnreachablePrimary`), all still green.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` clean.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `go test ./internal/pages/... -run PairStart -v` — all 8 matching tests
  pass; full `go test ./...` — every package green, no regressions.
- `bash scripts/ci/guard-i18n.sh` — passes, confirms the new key resolves
  and all four locales match `en.json`'s key set exactly.
- `bash scripts/ci/guard-page-http-error.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`
  — all pass (docs-shots confirmed unaffected, as expected for a
  Go+locale-only diff).
- Git identity checked before committing: real GitHub-linked address,
  never an AI-tool identity.

## Explicitly deferred (not silently dropped)

- The `ut-plugin-language-{de,es}` follow-up for the new `en.json` key
  (see "What shipped" above) — tracked by `lang-pack-drift`'s own advisory
  warning on this PR; not a new Backlog card since the existing CI
  mechanism already surfaces it.

## Verdict

**Safe to merge.** No blocking or non-blocking code findings from either
pass of review; the one deliberately-untested branch's unreachability was
independently corroborated by a second, larger empirical search. Full gate
green.
