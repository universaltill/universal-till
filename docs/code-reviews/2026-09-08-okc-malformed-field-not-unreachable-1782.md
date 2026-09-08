# ut-docs#1782 — OKC bridge: a malformed field is not "device unreachable"

Date: 2026-09-08
Branch: `fix/1782-okc-malformed-field-not-unreachable`
Review: one independent subagent, fresh context, Sonnet (`complexity:easy`).

## The bug

`plugins/tax-tr/okc/bridge.go`'s `roundTripOn` unmarshalled the whole
device response into a typed `bridgeResponse` struct. Any unmarshal
failure — including a syntactically valid JSON response with a
wrong-typed field (e.g. a maker's buggy bridge sending `"receipt_no":42`
as a number instead of a string, a plausible integration mistake) — was
folded into `ErrDeviceUnreachable`. That is fail-closed, so it was safe,
but it is the wrong error class: "unreachable" is the one decline path an
operator is trained to just retry, and a wrong-typed field means the
device likely DID answer (and may have already printed) — a retry only
stays safe via the device's own `request_id` idempotency, which depends
on the maker's bridge actually honouring it. Split out of ut-docs#1763's
independent review (finding F5, pre-existing behaviour, not caused by
that fix).

## The fix

1. `plugins/tax-tr/okc/protocol.go` gains a new sentinel
   `ErrMalformedResponse`, alongside the existing `ErrDeviceUnreachable`/
   `ErrDeviceDeclined`/`ErrNoReceipt` family.
2. `roundTripOn` (`bridge.go`) uses `errors.As` to detect a
   `*json.UnmarshalTypeError` specifically when `json.Unmarshal` fails,
   and wraps it with `ErrMalformedResponse`, naming the offending
   field/expected-type/actual-value. Any other unmarshal failure
   (genuinely non-JSON/garbage/truncated bytes) still falls through to the
   existing `ErrDeviceUnreachable` path, unchanged.
3. `plugins/tax-tr/main.go`'s `declined()` gains one matching `switch`
   case, logging this class distinctly from both "unreachable" and
   "driver not implemented".
4. `plugins/tax-tr/README.md`'s "Rules the plugin enforces" section gains
   a sentence describing the new class and why it's kept separate from
   "unreachable".

Two new tests in `bridge_test.go`:
- `TestBridgeSale_MalformedField` — a hand-rolled raw-TCP-response test
  (new `rawServer` helper, same style as the file's existing `startSim`
  helper) proving a numeric `receipt_no` now yields `ErrMalformedResponse`
  and explicitly asserts it does **not** also match `ErrDeviceUnreachable`.
- `TestBridgeSale_GarbageAnswerStillUnreachable` — a regression guard
  proving genuinely non-JSON bytes still yield `ErrDeviceUnreachable`,
  unchanged, since this diff splits that code path in two.

## Independent review — findings

One Sonnet subagent (fresh context, per the `complexity:easy` review
tier) reviewed the diff in an isolated worktree
(`.claude/worktrees/review-1782`, detached HEAD on the WIP commit), ran
the full gate itself, and independently re-verified the TDD claim: it
reverted `bridge.go`'s detection logic (keeping the new sentinel in
`protocol.go` so the test package still compiled), confirmed
`TestBridgeSale_MalformedField` failed with the *old* misclassification
(`err = okc: device unreachable: unparseable answer ...`, not a compile
error), restored the fix, and confirmed the full suite passed again with
an empty `git status`/`git diff` afterward.

**Finding 1 (blocking per this repo's own `CLAUDE.md` doc-update rule,
not a runtime bug — fixed).** The plugin's `README.md` "Rules the plugin
enforces" section described "declines, times out or is unreachable" as
one bucket the cashier just retries, without carving out the new
malformed-field class this card introduces. `CLAUDE.md` requires a
behaviour change to update the affected doc in the same session. **Fixed**
by adding the sentence described above (point 4).

**No blocking correctness issues found.** Specifically checked and ruled
out by the reviewer: `errors.As` (not `errors.Is` or a bare type
assertion) is the correct, wrap-safe way to detect
`*json.UnmarshalTypeError` here; the split correctly separates a
wrong-typed field from a genuine JSON syntax/truncation error (traced by
hand plus the passing garbage-input regression test); no other caller in
the repo pattern-matches on `ErrDeviceUnreachable` (`grep -rn
"ErrDeviceUnreachable" --include=*.go .` from repo root returns only the
four sites in `bridge.go`/`bridge_test.go`), so nothing elsewhere is
silently misclassified; the new error message embeds only a short type
descriptor (`typeErr.Value`, e.g. `"number"`) plus the same
`strings.TrimSpace(string(line))` pattern the adjacent pre-existing
"unparseable answer" message already uses — no new size/log-injection
exposure; `declined()`'s new case is reachable from both `.authorize` and
`.refund` and preserves the unconditional fail-closed `os.Exit(1)`; the
two recurring bug classes this pipeline watches for (missing
`os.MkdirAll`, a cwd-relative path where `paths.Data(...)` belongs) do
not apply — this diff has zero file I/O; no i18n/UX/help-manual surface
is touched (backend operator-log-only change); no real client/shop name
or literal secret anywhere in the diff.

## Verified

- `go build ./...`, `go vet ./plugins/tax-tr/...`, `gofmt -l
  plugins/tax-tr/` — all clean.
- `GOOS=wasip1 GOARCH=wasm go build -o ... ./plugins/tax-tr` — the
  plugin's real build target — succeeds; reviewer confirmed the wasm
  binary is byte-identical before/after the revert-restore cycle.
- `go test ./...` — full repo, zero failures.
- `go test ./plugins/tax-tr/... -race -v` — all 16 tests pass, including
  the two new ones, no race warnings.
- TDD independently re-verified by the reviewer (see above) — the new
  regression test genuinely exercises the real code path, not a
  false-pass: it fails under the old behaviour and only passes with the
  fix present.
- No real client/shop name, no secret-shaped literal introduced.

## Explicitly out of scope / deferred

- ut-docs#1780 (the sibling finding from the same #1763 review batch,
  "OKC empty-receipt guard is duplicated per-driver") — separate card,
  not touched here.
- Real ÖKC hardware verification — unavailable to any cold cloud/cron
  session; the maker-driver scaffolds (`gmp3`, `hugin-pclink`,
  `pavo-rest`, `token-x`) remain unconditional
  `ErrDriverNotImplemented`, so no live path is exposed to this bug
  outside the reference `bridge` driver today.

## Verdict

Safe to merge. One review round; the round found one blocking-per-policy
(not runtime-safety) doc gap, fixed and re-verified in the same round —
no second round needed, per this pipeline's standing "second round only
for a blocker, scoped to the fix" rule (a doc-only fix here didn't even
rise to that bar, but was applied anyway since it was already found).
