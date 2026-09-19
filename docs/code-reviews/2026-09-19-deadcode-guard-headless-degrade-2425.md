# Code review: guard-deadcode-baseline.sh headless degrade (ut-docs#2425)

**Date:** 2026-09-19
**Author:** Farshid Mirza (lane:cloud-24)
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier)
**Issue:** universaltill/ut-docs#2425
**Branch/PR:** `fix/2425-deadcode-guard-headless-degrade`

## What changed

`scripts/ci/guard-deadcode-baseline.sh` always analyzed `./cmd/unitill-desktop`
as one of its three `deadcode` roots, which requires real GTK3/WebKit2GTK cgo
dev headers just to type-check — so the guard failed outright, not just less
accurately, in any sandbox without them (hit twice independently: universal-till#1284's
review, and ut-docs#1566's 2026-09-12 slice). `.golangci.yml`'s `unused`
linter already has the equivalent carve-out for the same reason.

Detects the headers via `pkg-config --exists gtk+-3.0 webkit2gtk-4.1` and
drops the `./cmd/unitill-desktop` root (and the now-pointless `-tags=desktop`,
since nothing outside that directory carries the `desktop` build tag) when
they're missing, falling back to `.` + `./cmd/unitill-uninstall` only. Real CI
(`desktop-shell` job, which installs the headers) is unaffected.

`guard-deadcode-baseline_test.sh`'s fixture used to live unconditionally
under `cmd/unitill-desktop/`, which would go silently unanalyzed (false pass)
in the same headless case — it now plants the fixture under the repo root
instead when headers are missing, using the same detection check, so the
"must catch a new unreachable function" assertion still holds in both modes.

## Verification performed (mine + review's independent re-derivation)

- Ran both scripts live in this sandbox (confirmed no GTK/WebKit headers
  present): `guard-deadcode-baseline.sh` now exits 0 cleanly (previously
  failed outright on a pkg-config cgo error); `guard-deadcode-baseline_test.sh`
  passes all 3 cases (previously false-failed the "must reject a new
  unreachable function" case for the reason above).
- `go build ./...` clean; working tree only the two intended files, no stray
  fixture file left behind.
- Reviewer independently confirmed `gtk+-3.0 webkit2gtk-4.1` is the exact
  pkg-config pair `cmd/unitill-desktop/webkit_linux.go`'s own `#cgo
  pkg-config` directive requires, and matches what `ci.yml`'s `desktop-shell`
  job installs (`libgtk-3-dev libwebkit2gtk-4.1-dev`) — the detection can't
  silently diverge from what the real build needs.
- Reviewer empirically diffed `deadcode -test=false . ./cmd/unitill-uninstall`
  output with and without `-tags=desktop`: byte-identical, confirming
  dropping the tag alongside the root has no side effect on the two
  remaining roots' findings.
- Reviewer confirmed every root-level `.go` file is `package main`, so the
  test's fallback fixture location has no package-collision risk, and that
  `guard-i18n.sh`'s scope never reaches `scripts/ci/*.sh`.
- `shellcheck` isn't installed in this sandbox; reviewer manually walked the
  diff for the classic shellcheck complaints (quoting, array usage,
  `set -e` interaction) and found nothing it would flag.

## Findings and disposition

Two low-severity nits, both fixed same-session:

1. **Stale opening sentence.** The unmodified top-of-file comment's first
   line ("Requires the same GTK/WebKit dev headers...") read as an absolute
   requirement even though the script no longer strictly requires them.
   Fixed: reworded to "For the full three-root analysis, requires...".
2. **Duplicated detection one-liner.** The `pkg-config --exists gtk+-3.0
   webkit2gtk-4.1` check is intentionally repeated verbatim in the test file
   rather than shared from one place — correct today, but could silently
   drift if the guard's own detection ever changes. Fixed: added a one-line
   comment in the test file naming the guard as the copy to keep in sync,
   proportionate to an easy-tier, two-file shell script change (no shared
   helper introduced).

No correctness, shell-safety, or CI-interaction issues found. Re-verified
both scripts pass after applying both fixes.

## Not in scope

While testing this guard against `main`, it (correctly) flagged
`internal/pos/session_manager.go`'s `SessionBasketManager.TableOwner` as a
new unreachable function on the **unrelated** `feat/2261-self-order-session-basket`
branch (ADR-0103, a different in-flight card, same lane). That's ut-docs#2261's
own finding to resolve, not this card's — noted here only so the two don't
get conflated; not carried into this diff.
