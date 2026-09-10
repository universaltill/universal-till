# Code review: translations page keyset (cursor) paging (ut-docs#2019)

**Date:** 2026-09-10
**Card:** ut-docs#2019 — "Translations page infinite-scroll: offset-based
paging can skip/duplicate a row under a concurrent edit" (follow-up from
the ut-docs#2014 review, which shipped offset/limit paging with this as a
documented, accepted limitation).

## What shipped

`/ui/translations-table`'s infinite scroll paged via a plain numeric
`offset`/`limit`. If the filtered key set's *length* changed between the
server rendering a "load more" sentinel row and the client following it —
a plugin installed/uninstalled, or a different manager's concurrent edit
changing whether a key matches the current search `q` — the next numeric
offset no longer pointed at what the client actually needed next, silently
skipping or duplicating one row.

- `internal/pages/translations_page.go`'s `renderTable`: replaced
  `offset`/`nextOffset` with a keyset cursor, `?after=<last-seen key>` /
  `nextAfterEscaped`. `i18n.Entries()` guarantees a stable, ascending,
  duplicate-free sort by `Key` (keys come from a Go map), so "the next
  page" is expressed as "every entry whose `Key` sorts after `after`"
  (`sort.Search`) instead of a numeric position — invariant to insertions/
  deletions anywhere else in the set, since it never depends on position,
  only on content already served.
- `web/ui/partials/translations_table.html`: the sentinel `<tr>`'s
  `hx-get` now carries `after={{ $.nextAfterEscaped }}` instead of
  `offset={{ $.nextOffset }}` — the only visible-template change, and it's
  a hidden attribute value, not user-facing text (confirmed no `T` key
  added/changed, `guard-i18n.sh` clean).
- `internal/pages/translations_page_test.go`: every existing offset-based
  test converted to the cursor's semantics (first page, append-fragment-
  has-no-wrapper, short/empty results, the true last page of the full
  unfiltered set, `?limit=` clamping, cursor-beyond-every-key, an empty
  append page still showing the end marker, sentinel URL escaping), plus
  two new regression tests (see TDD below).

## Independent review

Fresh-context Opus subagent, isolated worktree (`isolation: "worktree"`,
per `reviewer` skill — the WIP commit was reverted/restored inside the
worktree, never on the orchestrating session's shared checkout).

**Verdict: SAFE TO MERGE, one finding fixed.**

Commands it actually ran (not just "tests pass"): `go build ./...` (clean),
`go vet ./...` (clean), `go test ./internal/pages/... -run TestTranslations -v`
(20/20 PASS), full `go test ./...` (55 packages, 0 FAIL), `golangci-lint run
./...` (0 issues), `gofmt -l` on both changed files (clean).

### Findings

1. **MEDIUM (fixed) — `strings.TrimSpace` on the `after` cursor could wedge
   pagination into an infinite loop.** `internal/pages/translations_page.go`
   (then line 127): the cursor is a machine-generated exact key round-
   tripped from a prior response's own `nextAfterEscaped`, not user-typed
   input to tolerate stray whitespace in. Trimming it meant a key with
   leading/trailing whitespace could never be advanced past — `sort.Search`
   would keep finding that same padded key as "the first key after the
   trimmed cursor", so `nextAfter` never changed and the revealed sentinel
   would re-fire forever, appending a duplicate row each time. Reachable:
   a plugin's locale overlay keys are taken verbatim with no validation
   (`internal/plugins/plugins.go`), so a language-pack typo with trailing
   whitespace in a key is realistic. **Fix:** dropped the `strings.TrimSpace`
   wrapper — `after := r.URL.Query().Get("after")`, compared byte-for-byte.
2. **LOW (addressed, comment-only) — the new `..._ConcurrentRemovalDoesNotSkipNextRow`
   test's own comment overclaimed what it demonstrates when run against the
   pre-fix code**: it fails against `main`'s offset-based code, but on the
   cursor-plumbing assertion (the template already expected `nextAfterEscaped`,
   which old Go code never supplied), not on the skip assertion it exists to
   guard — so it doesn't, by itself, prove the original skip bug when run
   unmodified against `main`. The reviewer independently re-verified the
   actual skip bug with a separate scratch probe run against the real
   pre-fix offset semantics (see TDD section) and confirmed it reproduces.
   Test comment reworded to say exactly this rather than imply the assertion
   itself goes red on the skip.
3. **NIT (accepted, not fixed) — an empty-string key would be permanently
   unreachable** — same root cause class as finding 1 (cursor and "no
   cursor" share a namespace), but no such key exists today and none is
   expected to (translator keys are always non-empty dotted identifiers).
   Noted for completeness, not worth a defensive check against a case that
   cannot occur with real data.
4. **NIT (pre-existing, out of scope) — a comment says en.json is "2,258
   keys"; it's now 2,261.** Inherited from ut-docs#2014, not introduced or
   touched by this diff — left as-is rather than fixing an unrelated stale
   comment in passing.

### Things independently confirmed correct

Sort-order invariant (`Entries`'s plain byte-wise `<`, `sort.Search`'s
byte-wise `>`, same comparator, keys map-sourced so unique); no off-by-one
in `start`/`end`/`nextAfter` (`nextAfter = all[end-1].Key` is only reached
when `end > start`, so always a valid index; `hasMore && nextAfter == ""`
is provably impossible); all four `isAppend`/`hasMore` combinations (empty
result, exact page boundary, cursor past every key, `after == ""`) render
correctly; `renderRow` (the save/clear single-row path) untouched by any
of this, since it never used offset in the first place and `rowOnly:true`
short-circuits the block that would reference the missing template field;
no other `?offset=` consumer left anywhere in the repo (`.go`/`.html`/
`.js`, prose comments aside); escaping (`nextAfterEscaped`) mirrors the
existing `qEscaped`/`editLocaleEscaped` pattern correctly; the two
synthetic-overlay-key regression tests' `"0000."`/`"0001."` key prefixes
don't collide with or sort after any real `en.json` key (checked all 2,261
of them). Neither of this pipeline's two recurring bug classes (missing
`os.MkdirAll`, a cwd-relative path where `paths.Data(...)` belongs) apply
— this diff touches no filesystem I/O.

## TDD — verified beyond automated tests

**Finding 1 (whitespace wedge).** Re-introduced the bug (reverted just the
`strings.TrimSpace` line), ran the new
`TestTranslationsTable_CursorWithTrailingWhitespaceAdvances` — **FAILED**
(cursor did not advance past the padded key, page 2 still returned it).
Restored the fix, full `go test ./internal/pages/... -run TestTranslations`
— **21/21 PASS**.

**Original skip bug (ut-docs#2019 itself).** The reviewer additionally
reverted the whole diff to `main` and drove the pure offset flow with a
scratch probe (not part of the shipped suite): page 1 served the first
synthetic key with `offset=1` as the next cursor; after removing that key
(simulating a concurrent plugin uninstall) between requests, the real
`offset=1` fetch against the now-shrunk set silently skipped the second
synthetic key entirely and returned an unrelated real row instead —
reproducing the exact bug this card describes. Restored, full suite green
again.

## Gate (after the fix)

`gofmt -l` clean · `go build ./...` clean · `go vet ./...` clean ·
`go test ./...` — 0 failures across every package · `golangci-lint run
./...` — 0 issues · `guard-data-access.sh`, `guard-i18n.sh`,
`guard-page-http-error.sh`, `guard-htmx-loaded.sh`, `guard-kiosk-engine.sh`
— all pass (this diff has no SQL, no new user-facing strings, no kiosk-
route/self-order code).

## Driven, real-server verification

Built and ran the actual `unitill-pos` binary (`UT_DATA_DIR` scratch dir,
`UT_AUTH=off`, `UT_LISTEN_ADDR=:18080`), then drove it for real (not
httptest mocks) with `curl`:

- `GET /translations?edit_locale=en` — HTTP 200, real page load.
- Followed the real sentinel chain for 3 consecutive pages
  (`/ui/translations-table` → cursor → cursor → cursor) — 100 rows each,
  confirmed **zero overlap** between consecutive pages' row sets, and each
  page's own sentinel correctly carried the next real `after=` key.
- `POST /api/translations/set` on a real key, then re-fetched the filtered
  table — the override applied and rendered immediately, proving the
  save/reload path is unaffected by the paging change.
- Server stopped, scratch data dir and binary removed.

**Visual check not performed** — this diff changes no visible markup, CSS,
or text; the only template change is one hidden `hx-get` attribute value
(`offset=…` → `after=…`), confirmed by inspecting the real rendered HTML
above and by the independent reviewer separately. No Playwright/e2e run
either (e2e's `node_modules` aren't installed in this session and this
diff has zero layout/visual surface to justify installing them for one
change) — flagging this explicitly per the `tester` skill's own
instruction to say so rather than imply a screenshot check happened.

## Deferred / explicitly out of scope

- The Go/WASM-plugin `bin/` build-artifact gap and other unrelated
  version-bump-guard work (different card, ut-docs#1948) — not touched
  here.
- Finding 4 (stale "2,258 keys" comment) — pre-existing, from ut-docs#2014,
  left as-is.

## Safe-to-merge verdict

**Yes.** Design is sound (a keyset cursor over a stable sort is the
textbook fix for offset paging's concurrent-mutation hazard), the one real
regression the independent review found is fixed and TDD-proven, the
original bug this card exists for is independently reproduced and proven
fixed, and the full gate — including a real driven run of the actual
server — is green.
