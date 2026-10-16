# Review: RTL logical-CSS guard + he/ur dir pin (ut-docs#4375)

Date: 2026-10-16 · Branch: `feat/4375-rtl-logical-css-guard` · Lane: cloud-24
Card: ut-docs#4375 (ADR-0160 §8, task 14) · Reviewer: independent Fable subagent (author: Opus 5.5)

## What shipped

- `scripts/ci/guard-rtl-logical-css.sh` checks CSS only: `web/public/**/*.css`, plus the `<style>` blocks and `style="…"` attributes in `web/ui/**` and `web/public/**` `.html` files. It fails on physical `margin-/padding-/scroll-*-left|right`, `border-left|right*`, `border-top|bottom-left|right-radius`, `left:`/`right:` and `text-align|float|clear: left|right`. It skips comments. Go template actions count as declaration boundaries. A reviewed exception takes a same-line `rtl:allow <reason>`. Lines that predate the guard go in the shrink-only `scripts/ci/rtl-css-baseline.txt` (`path<TAB>line`, counted per occurrence), and a stale entry fails the guard.
- Regression test `guard-rtl-logical-css_test.sh`: fixture tree in `mktemp -d`, 50+ cases, then the real tree. Both are wired into `ci.yml`'s `build` job.
- Existing hits: `buttons_admin.html` `left:0;right:0` → `inset-inline:0` (same result in both directions). `plugin_manual_import.html` `.permission-list` `padding-left:0` → `padding-inline-start:0`. That fixes a real RTL bug: the UA's default 40px `padding-inline-start` stayed on the right in fa/ar. Baselined: the self-order `.toast`'s `left:50%` + `translate(-50%)` centring, which is symmetric.
- `TestDirFuncRTLLocales` pins `<html dir>` (the template `dir` func) to `rtl` for ar/fa/he/he-IL/ur/ur_PK. `he`/`ur` were already in `httpx.IsRTL`.
- CLAUDE.md RTL bullet names the guard. ut-docs `reference/coding-standards.md` §7 and `ux-guidelines.md` updated (ut-docs PR).

## Split out

The length render check at 1024×600 for each new locale needs the `max_length` key metadata (#4362) and installable language bundles (#4369). It is filed as ut-docs#4475 with `blocked:dep`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A `/*` inside `accept="image/*"` opened a "comment" that blinded the guard until the next `*/` (live in catalog.html / categories.html) | Fixed: `/*` followed by a quote is literal; test added |
| 2 | minor | A declaration next to a Go template action (`{{ if .E }}margin-left:…`) was missed | Fixed: `{{…}}` → `;` before matching; 2 tests |
| 3 | minor | The baseline matched by content, not count, so a second copy of a baselined line passed | Fixed: per-occurrence counts, stale when fewer remain; 3 tests |
| 4 | minor | `el.style = "left:…"` / `:style="{ left: x }"` read as style attributes (JS) | Fixed: `.`/`:`/`$` before `style=` excluded; test added |
| 5 | nit | The error text suggested `float: inline-start`, which older Android WebView lacks | Fixed: "prefer flex/grid" |
| 6–8 | nit | Out-of-scope list incomplete; paths with spaces; `internal/recovery/templates` not scanned | 6/8 documented in the header; 7 accepted: such paths fail loudly, not silently, and none exist |
| 9 | nit | Test gaps (`rtl:allow` in a template, malformed baseline, uppercase `<STYLE media>`) | Tests added |

Each new test was confirmed to fail against the pre-fix guard (6 failing cases) and to pass after the fix.

## Verified beyond automated tests

- On real Chromium (Playwright), the old and new rules render identically in LTR and RTL: `left:0;right:0` and `inset-inline:0` give the same box. The `.permission-list` padding was `0/40px` in RTL with the old rule and `0/0` with the new one.
- The reviewer ran the guard under mawk (CI's awk) and ran about 30 extra state-machine probes. `inset-inline` is already used 4× in app.css, so it is safe for the WebKit targets.
- TDD: removing `"he"` from `IsRTL` makes `TestDirFuncRTLLocales` fail; restoring it makes it pass (author and reviewer each checked).
- `shellcheck` 0 issues on both scripts, `guard-ci-tests-sandboxed.sh` green, `go vet`/`go test ./internal/httpx` ok, and `guard-i18n`, `no-showmodal`, `no-inline-handlers` and `help-topics` are green.

No visual surface changed for an LTR shop, so there are no new docs-shots and no help topic changed (no shop-owner-visible behaviour).

## Verdict

Safe to merge.
