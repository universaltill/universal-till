# Code review — extend the inline-handler guard to internal/pages/*.go (ut-docs#3506)

- **Date:** 2026-10-03
- **Branch:** `fix/3506-inline-handler-guard-internal-pages`
- **Card:** universaltill/ut-docs#3506 (complexity:easy, security) — follow-up
  to ut-docs#3325 (CSP slice 2) and ut-docs#3510 (CSP slice 2c), which fixed
  three live `internal/pages/*.go` inline-handler instances but didn't add a
  guard against a fourth, and didn't close this card.
- **Author:** orchestrator (Sonnet, this cycle's build model for
  `complexity:easy`); **reviewer:** Opus 5.5, fresh context, independent
  checkout.

## What shipped

`scripts/ci/guard-no-inline-handlers.sh` scanned only `web/ui/**/*.html`
templates for inline CSP `script-src-attr` violations (`onclick=`, `hx-on...=`,
etc.). `internal/pages/*.go` hand-builds HTML outside any template
(`fmt.Fprintf`/`b.WriteString` with a backtick or quoted Go string), so it was
invisible to the guard; #3510's Dev investigation found and fixed three real
instances there but left the class itself unguarded. #3506's acceptance
criterion was "a decision recorded (new/extended guard, or an explicit scope
note)".

**Decision:** extend the guard with a second scan ("1b") over
`internal/pages/**/*.go` (excluding `_test.go`, which are test fixtures, not
rendered pages — same carve-out as `guard-plugin-menu-read.sh`). Reuses the
same allowlist file, with `internal/pages/<path>:<line>` keys. Alpine.js
`x-on:`/`@click` directives are explicitly scoped **out** (documented in the
header, not detected): none exist anywhere in the repo, and they aren't argued
to be a `script-src-attr` violation under CSP3's `unsafe-hashes`/`unsafe-eval`
model the way `onclick=` is.

The new pattern is deliberately narrower than the existing web/ui `$PATTERN`:
it requires a quote (optionally preceded by a backslash, for an interpreted
Go string's `onclick=\"...\"`) right after `=`, so it:
- catches the real violation shape (`onclick="..."`, `hx-on...="..."`, in
  either a backtick or an interpreted string), but
- never flags `el.onclick=function(){...}` — a DOM-property assignment inside
  a `<script>` block, still present at `internal/pages/setup_update_check.go:94`
  — which is a *script-src* concern for #3327, not *script-src-attr*, and is
  explicitly out of scope here (#3506's own finding); and
- never flags a Go doc-comment that merely *names* an hx-on/onclick attribute
  in prose with no `=` (real examples: `internal/pages/users_page.go:740`,
  `eod_api.go:1331`, `settings_page.go:1894`) — it deliberately does not reuse
  the web/ui pattern's bare `"hx-on"` substring alternative, which would have
  false-fired on these.

Confirmed the three originally-reported instances
(`pos_api.go:2395`/`2400`, `import_page.go:1534`/`1707`) are already converted
to `data-action` on `main` (via #3510/PR universal-till#1641) and do not trip
the new guard — no allowlist entry needed.

## Review findings — both fixed before this commit

Independent review (Opus 5.5, fresh context) ran the guard against real
fixtures rather than just reading the diff, and found two real bugs in the
first draft:

1. **Escaped-quote shapes were missed.** An interpreted (double-quoted) Go
   string's source text has a backslash between `=` and the quote
   (`onclick=\"...\"`), not a bare quote. The first pattern required a bare
   quote right after `=`, so it missed this real shape (confirmed against a
   constructed fixture; this style is real in the package, e.g.
   `internal/pages/plugin_page.go:173`'s `"<p class=\"empty\">..."`). **Fix:**
   pattern now accepts an optional backslash before the quote in both
   alternatives (`=[[:space:]]*\\?["']`). Verified with new red→green test
   cases for both `onclick=\"...\"` and `hx-on::click=\"...\"`.
2. **A real scan error could be silently swallowed.** The scan piped
   `grep -rniE ... | grep -v '_test\.go:'` to drop test files. Under
   `set -o pipefail`, the pipeline's exit status is the *rightmost* non-zero
   command — so a real error (`grep` exit 2) from the first `grep` was masked
   by the second `grep -v`'s exit 1 (its ordinary "no lines passed the
   filter" status), which is below the `rc -gt 1` threshold that's supposed to
   catch it. Confirmed with `(exit 2) | grep -v …` → `rc=1`. **Fix:** replaced
   the pipe with a single `grep --exclude='*_test.go'` call — no second
   command, so the exit code is unambiguous. Confirmed a simulated grep error
   now surfaces as `rc=2`, not masked as `rc=1`.

Non-blocking finding (documented, not fixed): the pattern can false-positive
on an ordinary Go assignment whose identifier happens to start with "on" and
is followed by `= "..."` (e.g. a hypothetical `cfg.OnboardingStep = "x"`).
None exist in the repo today (checked); the guard's header now says so and
points at allowlisting/renaming over loosening the pattern if one ever lands.

## Verification

- `bash scripts/ci/guard-no-inline-handlers_test.sh`: all 34 assertions pass,
  including the real, unmodified repo tree (zero args) and 12 new assertions
  covering the `internal/pages` check (positive hits for `onclick=`/`hx-on=`
  in both backtick and escaped-interpreted-string form, the two deliberate
  false-positive carve-outs, the `_test.go` exclusion, the
  `internal/pages/<path>:<line>` allowlist exact-match rules, and a
  nonexistent pages-dir failure).
- TDD red→green verified twice (before and after the review fixes): stashed
  only `guard-no-inline-handlers.sh`, re-ran the test script, confirmed the
  specific new assertions fail without the fix, then restored it and
  confirmed they pass.
- `bash scripts/ci/guard-no-inline-handlers.sh` (zero args, real tree): passes
  clean.
- `shellcheck` is not installed in this sandbox; CI's own `shellcheck
  scripts/ci/*.sh` step will cover it on push.
- No Go code changed, so `go build`/`go test`/`golangci-lint` are unaffected;
  not re-run for this shell-only change beyond the guard's own test script.

## Scope

Shell-only: `scripts/ci/guard-no-inline-handlers.sh`,
`scripts/ci/guard-no-inline-handlers_test.sh`,
`scripts/ci/inline-handler-allowlist.txt`. No production Go/HTML changed —
the three originally-reported `internal/pages` instances were already fixed
by #3510.
