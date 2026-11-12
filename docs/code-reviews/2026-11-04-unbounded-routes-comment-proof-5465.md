# 2026-11-04 — unbounded-routes guard ignores comments (ut-docs#5465)

**What shipped.** A test-only change to
`internal/pages/unbounded_routes_test.go`, following up the ut-docs#5456
review. Before it, `TestUnboundedRoutesMatchAppJS` confirmed the
`/pairing-restart$` exemption by finding its regex text anywhere in
`web/public/app.js`, so a copy of that text in a JS comment passed the
check. Its Go route scan also picked up `Handle`/`HandleFunc` calls inside
Go comments. Now:

- `stripJSComments` blanks `//` and `/* */` comments in app.js before the
  `UT_UNBOUNDED_PATHS` list and the marker are matched. It is a small lexer
  that skips string, template and regex literals, because app.js contains
  regex literals such as `/\/pairing-restart$/`. Blanking keeps newlines,
  so line numbers stay the same.
- `stripGoComments` uses `go/scanner` to blank Go comments before the route
  regex runs.
- New `TestUnboundedRoutesIgnoreComments` covers these cases:
  - the marker appears only in a `//`, `/* */` or multi-line comment;
  - the real marker in code is kept;
  - a list entry is commented out;
  - the list appears only in a comment;
  - `//` inside a string, template or regex is kept, and division is kept;
  - a stray quote does not hide a later comment;
  - Go routes in `//` and `/* */` comments are ignored, while a `//`
    inside a route string is kept.

Runtime behaviour is unchanged.

**Pre-flight:** web/help topic: n/a (test-only). `make docs-shots`: n/a.
reference/ page: n/a (`docs/sale-screen-notifications.md` already points to
the test, and its contract is unchanged). README: n/a. en.json keys: n/a.
Lang packs: n/a. ADR: n/a. Review record: this file. Follow-ups: see
Deferred.

**Verification by the author.**
- TDD: with both strippers made identity functions, 5 of the new subtests
  fail. Restoring the strippers makes them pass.
- Stripping the real app.js:
  - the output passes `node --check`;
  - every blanked span on each of the 1,605 changed lines starts at `//`
    or `/*`, or continues a block comment;
  - no `//` or `/*` is left anywhere in the output.
- Registered-route set across non-test Go files under `internal/`: the old
  raw regex and the new comment-stripped scan both find the same 501
  patterns.
- Gate: gofmt clean, `go build ./...` OK, `golangci-lint run ./...` 0
  issues, `go test ./... -race`, guards from `ci.yml`. Three guards failed
  locally only because of the environment: `deadcode` (go1.26/1.27 toolchain
  mismatch), `shellcheck-version` (no binary) and `makefile-version` (make
  job server). The change does not touch any of them, and CI runs them.

**Review** (independent, Sonnet 5.5; the author was Opus 5.5), in a
separate worktree on WIP commit `0eb7d34`. The reviewer re-ran the TDD
revert, probed both strippers for panics on malformed input (none), and
confirmed the 501-route parity.

Findings:
1. **Low — fixed.** An unterminated `'`/`"` string ran past the end of its
   line and could hide a later comment. Quote strings now end at a newline
   (only templates span lines). Added a regression subtest.
2. **Low — accepted.** The regex-vs-division heuristic misreads `)`/`]`
   followed by a regex, and `a++ / 2`. Every misread leaves a comment
   visible, which is the old behaviour (fail-open). The reviewer found no
   case where code gets blanked.
3. **Low — accepted.** Nested backticks inside a template's `${…}` are not
   tracked. app.js has no `${`. Same fail-open direction.
4. **Nit — fixed.** The doc comment now says that a misread fails open.
5. **Nit — accepted.** A ~70-line lexer is heavy for a test helper, but it
   is needed: a naive strip would break the regex literals in app.js.

**Verdict:** approve.

**Deferred:** none. Findings 2 and 3 are fail-open edge cases that app.js
does not hit. A full JS tokenizer is not worth a card.
