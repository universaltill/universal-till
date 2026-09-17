# 2026-09-17 — favicon re-fetch on boosted navigation (ut-docs#2363)

## What shipped

`e2e/tests/persistent-shell-2224.spec.ts`'s "a rail tap keeps the document
and fetches only the page" test was failing on a clean `main`:
`/public/assets/logo/ut-logo.ico` gets re-fetched on every boosted
(`hx-boost`/`pushState`) navigation, violating ADR-0098's "no asset
re-fetch" guarantee. Root cause: this is Chromium's own browser-internal
favicon-fetch subsystem re-requesting the `<link rel="icon">` target on
every History API URL change — not app code, and not fixable by caching.
The test now excludes exactly this one path from the re-fetch assertion,
with a comment explaining why; every other asset still must never
re-fetch. `ut-docs/adr/0098-persistent-app-shell-hx-boost.md` gained an
Addendum documenting the finding for future readers.

## Investigation (Dev)

- Confirmed no app JS touches `<head>`/`<link rel="icon">` anywhere in the
  repo (grep).
- Booted a real till (`e2e/run-till.sh`) and drove Chromium directly to
  observe `PerformanceResourceTiming`: the favicon request's
  `initiatorType` is `"other"` (Chromium's marker for its own
  browser-internal fetches), not `"link"`.
- Tested whether HTTP caching could suppress it: forced
  `Cache-Control: public, max-age=31536000, immutable` on the response via
  a Playwright route interceptor — still re-fetched on every navigation.

## Independent review (Opus subagent, isolated worktree)

Went further than the Dev investigation and found stronger, more direct
evidence:

- **Bare `history.pushState()`, no htmx, no DOM change**: reproduces the
  favicon fetch on its own, with nothing else in flight — the single most
  decisive test, ruling out htmx/the shell script/the swap entirely.
- Removing the `<link rel="icon">` from the DOM before navigating stops
  the fetch, confirming the element drives it.
- Re-ran the "immutable" caching experiment against the **real server**
  (not a mock) with a versioned URL the server genuinely answers
  `immutable` for — still re-fetched on every nav. Stronger evidence than
  the original route-interceptor version.
- **Falsified an alternative the write-up hadn't tested**: `pushState`
  is not special — `history.replaceState()` and even a hash-only
  `pushState('/#same')` trigger the identical re-fetch. There is no
  navigation-mechanism swap that avoids this.
- Independent TDD re-verification: reverted the fix, confirmed the exact
  originally-reported mismatch; reapplied, confirmed 8/8 passing, then
  `--repeat-each=3` (24 passed, no flake).
- **Mutation test**: injected an unrelated `fetch('/public/app.css')` into
  the navigation path — the fixed assertion still fails on it, proving the
  exclusion is scoped to exactly the favicon path and the guarantee is
  intact for everything else.
- Confirmed no other e2e spec depends on the old stricter assertion; no
  i18n/money/repository-pattern/offline-first surface (test+doc only); no
  CI guard hashes `e2e/tests/**` for this file.

**Findings — all fixed, none blocking:**

1. (Doc accuracy) The ADR addendum's original wording implied abandoning
   `pushState` would fix it — the reviewer's `replaceState`/hash-only
   tests disprove that. **Fixed**: addendum now states the trigger is any
   History API URL change and names the two app-side levers that would
   actually stop it (drop the `<link>`; a `data:` URI, rejected as
   disproportionate) and why neither was taken.
2. (Doc accuracy) Two soft overclaims — "proving bypasses the HTTP cache"
   stated as measured fact rather than a well-supported inference, and
   citing the committed spec as the verification artifact when the
   `initiatorType`/immutable experiments were ad-hoc. **Fixed**: reworded
   to "consistent with" and "verified ad-hoc, 2026-09-17, not reproducible
   from the committed spec."
3. (Out of scope, noted not fixed) The favicon `<link>` is the only
   `base.html` head asset without a `?v=` version, so it separately pays a
   conditional revalidation on every full document load (rule 6's
   ~12-request cost). Versioning it does not fix this bug (tested against
   the real server), so correctly not bundled here — worth its own
   follow-up card if the revalidation cost matters in practice.
4. (Nit, deliberately not changed) The exclusion is an unconditional
   `!== FAVICON_PATH` filter rather than a strict burn-down baseline
   (`toEqual([FAVICON_PATH])`) the way `help-drift-baseline.json` does it
   elsewhere in this repo. Left as-is: the resource-timing read happens
   right after the assertion, and a browser-process fetch landing on
   either side of that read is a plausible timing race, so a strict count
   would trade a real assertion for flake risk with no real gain.

## Verified beyond automated tests

- `gofmt`/build not applicable (no Go changed).
- Full `persistent-shell-2224.spec.ts` suite run twice independently (Dev,
  then Tester) against a real Chromium + real till server: 8/8 both times.
- Reviewer's independent run: 8/8, then 24/24 under `--repeat-each=3`.
- Grepped for any other e2e spec depending on the old assertion shape:
  none found.

## Safe to merge

Yes. Root cause independently confirmed by a second, more rigorous set of
experiments than the original investigation; fix is minimal and correctly
scoped (exact path match, not a `/public/` carve-out); TDD-verified twice
independently; no regression to the shell's other guarantees (all 8 tests
in the file, including the separately-tested immutable-asset-versioning
guarantee, still pass); doc addendum corrected per review findings before
merge.

## Deferred

- Consider versioning the favicon `<link>` like every other head asset in
  `base.html` (rule 6) to cut the conditional-revalidation cost on full
  document loads — separate from this bug, not filed as a card yet.
