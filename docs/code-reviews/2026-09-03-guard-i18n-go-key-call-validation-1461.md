# Code review: guard-i18n Go-side key-call validation (ut-docs#1461)

**Card:** ut-docs#1461 (complexity:easy) — follow-up from #1455's review.
**Branch:** `fix/1461-guard-i18n-go-key-call-validation`

## What shipped

`scripts/ci/guard-i18n.sh` gained a new check (7): every string-literal key
argument passed to `httpx.RenderError`, `common.LocalizedError`,
`common.LogAndLocalizedError`, or `httpx.T` (qualified or bare) must resolve
in `web/locales/en.json`. Previously, checks 1-2 only validated template
`{{ T "key" }}` usage — a key built in Go and passed as a literal to one of
these four call shapes was invisible to any CI check, so a typo'd key
silently fell back to rendering the raw key text at runtime. Most visible on
`httpx.RenderError`'s full-page kiosk error screen (added in #1455), where
the typo'd key becomes the page's entire heading and body.

Argument extraction uses a small hand-rolled paren/string-aware scanner
(`split_args`/`literal_key`), not a regex comma-split, because a real call
site passes a nested call as `httpx.T`'s locale argument
(`httpx.T(httpx.ResolveLocale(w, r), "key")`) whose inner comma would
otherwise be misread as the key argument's boundary. A dynamic key (a
variable or helper call, e.g. `classifyTenderError(err)`) can't be verified
statically and is silently skipped, same tradeoff every other check in this
script documents (checks 3/5/6's own `i18n:ignore` convention, single-word
heuristic gaps, etc.).

New regression test `scripts/ci/guard-i18n_keycall_test.sh` (wired into
`.github/workflows/ci.yml`, same `build` job as the other i18n guard tests)
follows the established plant/expect/cleanup pattern from
`guard-i18n_test.sh`/`guard-i18n_toast_test.sh`.

## Independent review

Spawned a fresh-context Sonnet subagent (complexity:easy → same-model,
different-instance review, per the `reviewer` skill) with the diff and
instructions to actually run the guard/tests/build, not just read the code.

**First pass found one real, concrete gap**, not a nitpick: the initial
draft only recognised `httpx.T`'s 2-argument shape, and — on the mistaken
premise that a bare (unqualified) `T(` call could only mean
`internal/httpx`'s own function — only scanned files under
`internal/httpx/`. The reviewer found that premise false: `import_page.go`,
`internal/pages/catalog/handlers.go`, and `invoice_page.go` each bind a
locale up front into a 1-argument closure (`T := funcs["T"].(func(string)
string)` or `T := func(k string) string { return httpx.T(locale, k) }`) and
call it as `T(key)` — **~80 real call sites total**, all silently
unchecked, directly contradicting the check's own stated goal.

**Fix applied**: bare `T(` is now scanned in every file (confirmed by
inspection that no *other* meaning of a bare, unqualified `T(` call exists
anywhere in this codebase — the only two shapes are `internal/httpx`'s own
`T(locale, key)` and the locale-bound `T(key)` closure), and the key
argument's position is derived from the actual argument count (1 arg → key
is `args[0]`; 2 args → key is `args[1]`; anything else is an unrecognised
shape and is skipped rather than guessed at).

**Verified beyond the reviewer's own read**: after applying the fix, I
injected a typo into one real call site in each of the three files the
reviewer named (`import_page.go:260`, `catalog/handlers.go:936`,
`invoice_page.go:166`) and confirmed the guard now catches all three — and
confirmed each was previously silent (passed clean) before the fix, proving
the gap was real and is now closed in situ, not just against the synthetic
test fixtures.

Everything else in the reviewer's pass came back clean: the parser handles
escaped quotes, multiple same-shape calls on one line, and the nested-call
locale argument correctly; CI wiring is correct; fixture cleanup follows the
established trap pattern with no collision risk against the other i18n test
files; `go build ./...` and `gofmt -l .` are clean.

## Verified beyond automated tests

- **TDD claim re-verified personally** (not just taken on trust): stashed
  just the guard-i18n.sh check-7 addition and re-ran
  `guard-i18n_keycall_test.sh` — exactly the expected typo cases failed
  (6 in the first pass; the same cases plus the bare-T shape cases in the
  revised version), confirming the tests genuinely exercise the new code
  rather than passing vacuously.
- Ran the full guard four times across the fix iteration
  (`guard-i18n.sh`, `guard-i18n_test.sh`, `guard-i18n_toast_test.sh`,
  `guard-i18n_keycall_test.sh`) plus `go build ./...` and `gofmt -l .` —
  all clean on the final diff.
- Ran the other 17 CI-blocking guard scripts listed in `universal-till`'s
  `CLAUDE.md` "Before committing" section against this branch — all pass
  (a shell/Python-only change; no Go source touched, so this was a sanity
  check, not an expected-to-fail surface).
- `go test ./internal/httpx/... ./internal/pages/common/...` — pass (the
  two packages whose call shapes this check inspects; no production Go
  code was changed, so this is a regression check, not new coverage).

## Safe-to-merge verdict

Yes. The one real finding from independent review (bare-`T(` scope gap) was
fixed and re-verified against both synthetic fixtures and real production
call sites; nothing else came back as a blocker.

## Explicitly deferred / out of scope

- A call whose argument list wraps onto a second line can't be verified by
  this line-based scanner (documented in the guard's own check-7 comment,
  same class as check 5's pre-existing `web/public/` gap). One real call
  site in the codebase today (`internal/pages/sync_api.go:336-337`) is
  affected; not fixed here, per the card's own scope (`ut-docs#1461` asks
  for the static check, not a rewrite of the guard's line-based scanning
  model).
- A doc-comment example that happened to contain one of these four call
  shapes in quotes would trip the guard (no `i18n:ignore` on a comment-only
  line currently exists in the codebase, so this is latent, not live).
