# Review: help names rail buttons by label, not emoji — ut-docs#1431

Date: 2026-11-04 · Branch: `docs/1431-help-rail-labels` · complexity:easy

## What shipped

The rail buttons are inline SVG icons (`bugreport_chip.html` `{{ icon "bug" }}`,
`uislot.CoreRail` `/orders` icon `bell`, `session_chip.html` `{{ icon "user" }}`),
and `/menu` has been emoji-free since ut-docs#1845, yet five help topics still
told the reader to press 🐞, 🛎️ or 👤. `web/help/{en,de,fa,ar,tr}/{bug-reporting,
my-reports,order-status,sell,users}.md` now name each button by its UI label
plus a plain icon description:

- 🐞 → **Report an issue** (bug icon) — `issuereport.nav_label` (en "Report an
  issue", fa «گزارش یک مشکل», ar «الإبلاغ عن مشكلة», tr "Sorun bildir"; de
  "Problem melden", the wording the de help already uses, since German UI
  strings live in `ut-plugin-language-de`, not `web/locales/`).
- 🛎️ → **Order status** (bell icon) — `nav.orders`.
- 👤 → your name (person icon) — the session chip shows the operator's name.

Front-matter `summary:` stays plain text (no `**` in YAML) and matches body
line 13 in each locale. New `internal/pages/help_no_emoji_icons_test.go`
(`TestHelpDoesNotNameRailButtonsByEmoji`) walks every `web/help/**/*.md`,
anchored on the test file via `runtime.Caller` (sibling tests `chdir`), and
fails per file:line on any of the three code points (🛎 matched without the
FE0F selector, so both spellings are caught).

## Process

Docs + one test. BA → Dev (Sonnet) → Tester (PASS) → Review: Opus, independent
of the Sonnet author.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | fa `order-status.md` step 1 put the label in «» instead of bold (Dev did it so the line wouldn't start with a bold lead-in and trip `guard-help-drift`'s bold-lead-in count). Every other locale bolds it. Rephrased to «از نوار کناری صفحه فروش (آیکون زنگ)، یا از منو ☰، **وضعیت سفارش‌ها** را باز کنید.»: bold restored, lead-in stays plain, same word order as the pre-change fa line | fixed |
| 2 | minor | Test could pass vacuously if the walk found no `.md` (e.g. help moved but `web/help` left as an empty dir) | fixed: counts scanned files, `t.Fatalf` on zero |
| 3 | nit | en `sell.md` status-chip bullet says "see **Reporting a bug**", a title that doesn't exist (topic is "Reporting a problem"; de/fa/ar/tr already cite their real titles) | fixed (en only) |
| 4 | note | en `sell.md` still calls the button a "bug-report chip" (all locales). Accurate enough (it is a rail chip with a bug icon), and not an emoji, so out of the card's scope | accepted |

Checked and fine: labels match `web/locales/{en,fa,ar,tr}.json` and the rail
markup (icons `bug`/`bell`/`user` in `internal/httpx/icons.go`); rail labels
are visually hidden but are each button's `title`/`aria-label`, and are shown
in the phone ☰ drawer, so "named by label + icon" is what a user sees.
Translations read naturally: de "Schaltfläche **Problem melden** (Käfer-Symbol)",
"Glocken-Symbol", "Personen-Symbol"; tr suffixes follow vowel harmony
("**Sorun bildir**'e", "böcek/zil/kişi simgesi"); ar "أيقونة الحشرة/الجرس/شخص"
with «زر» agreement correct; fa "دکمهٔ … (آیکون حشره)" consistent with the
topic's existing "دکمهٔ". No orphaned hyphens (de "🐞-Schaltfläche" rewritten,
not stripped). ✕ and ☰ untouched (☰ is the real drawer glyph). No locale JSON,
Go runtime, UI or screenshot change; no CLAUDE.md rule touched (no new keys, no
fixed dates in the test, gofmt clean). The only other pictographs left in help
are 🧾 (en `invoices.md`) and 📜 (en `reports.md`), which label Journal/Reports
page buttons, not rail buttons — outside this card.

## Verification

- TDD re-verified by the reviewer without touching the shared checkout: the
  test copied into a detached `origin/main` worktree fails (pre-fix help, 55
  hits); in the branch it passes.
- `go test ./internal/pages/...` ok; `guard-help-drift.sh`,
  `guard-help-topics.sh`, `guard-help-terms.sh`, `guard-help-breadcrumbs.sh`
  exit 0 after the fixes; `gofmt -l .` empty.
- Tester attestation (driven run): built till, `UT_AUTH=off` temp data dir,
  first-run wizard + PIN login, Playwright over bug-reporting, order-status,
  users, sell, my-reports in en/fa/ar: labels render as `<strong>`, no raw
  `**`, no emoji, `dir=rtl` for fa/ar; rail shows SVG bug/bell/person icons.
  The tester drove help pages with JS disabled because, with JS on, headless
  `/help/*` ended up on `/`.
- That redirect, looked into: no client code navigates to `/` (`app.js`'s
  idle-lock goes to `/login`, or to `/` only on a satellite kiosk in
  maintenance via `X-UT-Lock-To`; the auth middleware sends anonymous requests
  to `/login`, or for `/` only, to the self-order root). Re-running the
  tester's own `dbg.js` with JS on against the tester's build and data dir
  (setup not yet finished there) with both the saved cookie and an empty
  context: `GET /help/bug-reporting` → 200 and the page stays put. So it isn't
  general help-page or first-run/lock behaviour, and nothing in this diff
  (Markdown only) can cause it. Most likely it was a one-off of the tester's
  post-wizard session (the first-run sale-screen hand-off/guided tour, or the
  boosted-shell fallback reloading the response URL). Not reproduced, so no card.

Pre-flight: web/help topic updated (5 topics × en/de/fa/ar/tr); `make docs-shots` n/a (text-only, no UI change, no screenshot shows the emoji); reference/ n/a; README n/a (no stale text); en.json keys n/a (none added/changed); lang packs n/a (de/es pack help copies live in pack repos, card non-goal); ADR n/a; review record this file.

## Verdict

Approve, with the three small fixes above applied. Nothing deferred.
