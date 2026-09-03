# 2026-09-03 — shifts_api.go `respondShiftSuccess` translated + escaped

Card: ut-docs#1406 (the sibling defect #1289's own review filed; same
defect class, the shift-*open* HTML-fragment success message).

## What shipped

`internal/pages/shifts_api.go`'s `respondShiftSuccess` — the HTML-fragment
path of `POST /api/shifts/open` (what the open-shift form actually renders
on screen; the JSON API path was already correct) — hardcoded:

```go
writeHTML(w, http.StatusOK, fmt.Sprintf("<div class='success'>Shift opened: %s</div>", data.ShiftID))
```

Two defects, same class #1289 fixed on the sibling `respondCloseSuccess`:

1. Untranslated English prose, never routed through `T()` — invisible to
   `guard-i18n.sh` because it's a Go-side `fmt.Sprintf`, not template
   markup.
2. Unescaped output. Not currently a security issue (`data.ShiftID` is
   `uuid.NewString()`, never user input), but worth the same
   `html.EscapeString` hardening for consistency with #1289.

Fix:

- `locale := httpx.ResolveLocale(w, r)`, then the message built via
  `fmt.Sprintf(httpx.T(locale, "shifts.open_success"), data.ShiftID)` —
  the same T()-routed, interpolated convention `respondCloseSuccess`
  already uses.
- New i18n key `shifts.open_success` ("Shift opened: %s") added to all
  four locale files (`web/locales/{en,ar,fa,tr}.json`), in key-sorted
  position. Translations mirror each file's existing
  `shifts.close_success` construction (ar "تم فتح الوردية", fa "شیفت باز
  شد", tr "Vardiya açıldı" — the passive "was opened" parallel to the
  passive "was closed").
- Output passed through `html.EscapeString`, matching #1289's pattern.
- New test: `TestRespondShiftSuccess_TranslatedAndEscaped`
  (`internal/pages/shifts_api_test.go`) — asserts (a) the en template
  renders with the shift ID, (b) a non-English locale (`fa`) cookie
  renders translated prose with no leftover English, and (c) a shift ID
  carrying markup is HTML-escaped. Calls the real choke point
  (`respondShiftSuccess`) directly, the same way
  `TestRespondAdjustmentError_EscapesHTMLInMessage` tests its helper.

## Independent review

General-purpose subagent, fresh context (the model that wrote the fix was
not the one reviewing). **Verdict: APPROVE, no findings.**

Independently re-verified the TDD claim (not taken on trust): reverted
only `internal/pages/shifts_api.go` to `main` (test + locale files left in
place), confirmed the new test fails —

```
shifts_api_test.go:178: expected the fa translation of shifts.open_success, got:
    <div class='success'>Shift opened: abc-123</div>
```

— then restored the fix and confirmed green again. (Revert→run→restore was
run atomically in one step in the orchestrator's own turn, not via a
worktree-isolated subagent, so no turn boundary could land mid-revert.)

Checked and confirmed correct: the Go change is a line-for-line pattern
match of `respondCloseSuccess` (same `ResolveLocale` →
`fmt.Sprintf(httpx.T(…))` → `writeHTML(… + html.EscapeString(msg) + …)`);
the JSON branch is untouched; `writeHTML` does no escaping of its own so
there is no double-escape; `T()` degrades to the bare key if the
translator is unwired (same as the precedent); each locale value has
exactly one `%s` and one arg is passed. The test has both a positive fa
assertion and a negative "no leftover English" assertion (the negative one
alone would catch a missing fa key, since `T()` falls back to the en
template), plus both an absence-of-raw and presence-of-escaped assertion
for the XSS case — no false-pass modes found. Neither of the two recurring
bug classes this pipeline watches for (missing `os.MkdirAll` / a
cwd-relative path where `paths.Data(...)` belongs) apply — the diff has no
file writes and no path construction at all. No real client/shop name in
the test data (`abc-123` and an XSS payload only). No `web/help/` manual
update needed (no new/changed route or page; the English text is
byte-identical to before, only now translated — `guard-help-topics.sh`
confirms unchanged route coverage).

Note: the reviewer's out-of-scope note that the de/es language packs
"already carry `shifts.open_success` upstream" was **wrong** — re-verified
directly, neither `ut-plugin-language-de` nor `ut-plugin-language-es` has
the key yet. The follow-up is real and is handled below.

## Verification

| Check | Result |
|---|---|
| `gofmt -l` (2 changed .go files) | empty |
| `go build ./...` / `go vet ./internal/pages/` | clean / clean |
| New test, directly and via full package | pass |
| `go test ./internal/pages/...` (full package) | pass |
| `go test ./...` (full repo) | pass, except one pre-existing `internal/server` listen test (`TestListenWithFallback_WildcardHostFallsBackToLoopback`) that fails on clean `main` too — environment-dependent, unrelated to this diff |
| All 19 CI-blocking guards (`scripts/ci/*.sh`) | pass |
| TDD red→green, independently re-verified by the reviewer | see above |

## Language-pack follow-up (mandatory, same cycle — not deferred to a card)

The new `en.json` key needs the same key added to the external
`ut-plugin-language-{de,es}` packs, or `lang-pack-drift` goes red on push
to `main` (blocking there, advisory on PRs). Done in this same pipeline
cycle, immediately after this PR merges, matching #1289's precedent — see
those repos' own history for the commits.
