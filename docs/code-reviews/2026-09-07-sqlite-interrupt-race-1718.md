# Code review — till dies with "auth unavailable" (ut-docs#1718)

**Date:** 2026-09-07
**Branch:** `fix/1718-sqlite-interrupt-race`
**Reviewer:** independent fresh-context Sonnet subagent (different model from the
implementer)
**Verdict:** SAFE TO MERGE for the driver fix; the reviewer explicitly refused to
accept the UX half as complete, and was right — see finding 1.

## The bug

A till reached a state where every SQLite query returned `SQLITE_INTERRUPT`
("interrupted (9)") until the Android app was force-stopped. The operator tapped
their PIN and got a white page reading `auth unavailable`. Reported twice in one
day on the TECLAST tablet.

The captured device log is what pinned the mechanism down: 715 interrupt
failures, and the victims were overwhelmingly **background** goroutines
(`lan discovery`, the `cloudsync` tick) which pass the long-lived server context
straight through and cancel nothing per tick. So the goroutine whose context was
cancelled was not the goroutine that died — a cancelled query's watcher fired
`sqlite3_interrupt` against a connection already returned to `database/sql`'s
pool, and the next user of that connection took the hit.

`go.mod` pinned `modernc.org/sqlite v1.29.10` behind a `// or latest` comment
that had outlived whoever wrote it. That version's `interruptOnDone` checks its
"done" flag and calls interrupt as two non-atomic steps. Upstream v1.58.0 put
both under one mutex, commented "donemu prevents a TOCTOU logical race between
checking the done flag and calling interrupt".

## What the reviewer verified independently

- **The regression test is not vacuous.** Rather than reading it, the reviewer
  downgraded to v1.29.10 and ran `TestCancelledQueryNeverInterruptsAnotherGoroutine`
  **5×: failed 5/5**. Restored v1.58.0 and ran it **3×: passed 3/3**, then again
  inside the full package. It judged the pool size of 2, the 3-second window and
  the `victimNs < 100` vacuity guard "well-chosen — not a lucky-timing test".
- **The fix mechanism, at the source.** Confirmed v1.58.0's `interruptOnDone`
  really does hold `donemu` across both the check and `c.interrupt(c.db)`.
- **DSN compatibility.** `_pragma=...` and `_txlock=immediate` are still
  supported identically in v1.58.0 — no behaviour change for `internal/db/db.go`.
- **Cross-compile for the broken platform.** `GOOS=android GOARCH=arm64
  CGO_ENABLED=0 go build ./...` clean; no cgo dependency introduced.
- **`firstBoot=false` on error is defensible.** It traced `NeedsFirstBoot` and
  confirmed it only errors on a genuine DB failure (never on "table doesn't exist
  yet", post-`migrate()`), so the default cannot mask a legitimately fresh till.
- Locale files valid, sorted, matching key counts (2007) across en/ar/fa/tr, with
  no mass reformat. `gofmt`, `go vet`, `golangci-lint` (0 issues) and the guards
  all pass.

## Findings and what was done

1. **[HIGH] The UX fix was incomplete, and missed the likelier path.** The
   implementer fixed `GET /login` only. `login.html` submits the keypad as a
   plain `<form method="post" action="/api/auth/login">` — no htmx, no fetch — so
   the bare `http.Error(w, "login failed", 500)` at the generic-error branch
   replaced the whole WebView document exactly as before. That is the literal
   reported sequence: **the operator taps their PIN and the till goes white.**
   `guard-page-http-error.sh` cannot see it because it excludes `/api/` routes as
   htmx fragments — correct for every other `/api/` route here, wrong for this
   one. **Verified independently before accepting** (the form markup really is a
   plain POST) and **fixed**: all five bare errors in the login and setup POST
   handlers now render the keypad back with the same translated message, via a
   shared `loginUnavailable` helper, matching what the invalid-PIN and locked-out
   branches already do. Two new tests cover the POST paths.
   The reviewer's framing critique was also fair and is recorded here rather than
   quietly dropped: the original claim that this closed the dead end end-to-end
   was overstated.
2. **[MEDIUM] Turkish terminology.** The new string used "yazarkasa" for *till*
   where all 166 other occurrences in `tr.json` use "kasa". **Fixed.** ar/fa were
   confirmed consistent with their own established term (صندوق).
3. **[LOW] Log format.** The new line omitted the status field that
   `httpx.RenderError` always includes, breaking grep uniformity for the
   `[page-error]` class it borrows. **Fixed**, and the status logged is the 200
   actually sent — with a comment saying why it is 200 and not the old 500.
4. **[INFO] Pre-existing flake.** `TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond`
   failed; the reviewer suspected the driver bump, then disproved it properly —
   stashed the branch, returned to unmodified `origin/main`, and reproduced **3
   failures in 5 runs** on the old code. Not a regression. **Filed as
   ut-docs#1725** rather than left as folklore: two separate reviewers tripped
   over it in one day and each had to prove it was not their change.

## Cross-repo follow-up owned by this change

`auth.error.unavailable` was added to `web/locales/en.json`, which implies the
external packs. `ut-plugin-language-de#179` and `ut-plugin-language-es#178` carry
real German and Spanish translations. Ordering matters and is not arbitrary: the
packs' own `key-drift` check reports the new key as an *orphan* until core's
`main` has it, while core's `lang-pack-drift` is advisory on a PR and blocking
only on push to `main`. So core merges first, then both packs immediately.
