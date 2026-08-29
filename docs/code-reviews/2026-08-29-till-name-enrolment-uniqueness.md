# Code review — till names must be unique at enrolment (ut-docs#1264)

- **Date:** 2026-08-29
- **Branch:** `fix/1264-till-name-uniqueness`
- **Reviewer:** independent reviewer (Opus, isolated worktree
  `/home/user/universal-till/.claude/worktrees/agent-a77b238ee73cc8634`).
- **Verdict: SAFE TO MERGE — with one BLOCKING prerequisite that cannot be
  done in this container:** `make docs-shots` must be run and its result
  committed on this branch before CI can go green (finding 3 below). Every
  other gate passes. Three findings fixed in place, three accepted with
  reasoning.

## What shipped

The card's full scope was deliberately narrowed by BA/Architect to one
thing: reject a duplicate till name at `POST /api/sync/enroll`.

- `internal/data/tills_repo.go` — new `TillsRepo.NameTaken(ctx, name)`.
- `internal/pages/sync_api.go` — the enrolment handler now rejects with
  **HTTP 422** when the joining till's name matches (case-insensitively)
  either an already-enrolled sibling or the primary's own effective name
  (`till.name`, or its translated default via `tillNameOrDefault`). The
  check runs *before* `InsertTill`.
- `internal/pages/sync_api.go` — replica side: new `joinErrNameTaken` kind,
  mapped to `tills.join_error.name_taken`, returned from `completeJoin`
  when the primary answers 422, surfaced through the existing
  `friendlyJoinError` path.
- `web/locales/{en,ar,fa,tr}.json` — new `sync.error.name_taken` and
  `tills.join_error.name_taken`.
- `web/help/{en,ar,fa,tr}/multitill.md` — one sentence in step 3 telling the
  owner the name must be unique and what to do when the join is rejected.

Explicit non-goals, spun out as separate Backlog cards and correctly absent
from this diff: (A) discovery-list cross-shop name-collision UX, (C)
unifying `lan_discovery.till_id` / `sync.till_id`.

## Independent review — what was checked

- **Every changed file read in full**, not just the hunks.
- **Ordering.** The uniqueness check sits after `tokens.consume` and the
  `name`-defaulting, and before `InsertTill` — verified by reading the
  handler top-to-bottom and confirmed at runtime by the new tests, which
  assert `ListTills` is unchanged after a rejection.
- **Status-code disambiguation (422 vs the handler's existing 409).**
  Confirmed correct and non-colliding. `completeJoin` only ever calls
  `POST /api/sync/enroll`, and the only other 422s in the tree
  (`sync_sales.go`, `data_api.go`, `invoice_page.go`,
  `receipt_designer.go`) are on endpoints `completeJoin` never touches.
  Branch order in `completeJoin` is right: 404 (`joinErrNotATill`) → 422
  (`joinErrNameTaken`) → the `!= 200` catch-all (`joinErrRefused`), so the
  new branch cannot be swallowed by the catch-all, and the 409 "pair new
  tills on the primary till" case still lands in the catch-all as before.
  `joinErrNameTaken` carries no `detail` and its locale string has no `%s`,
  which is the shape `friendlyJoinError` expects.
- **SQL injection:** none — the query is parameterised.
- **Operator-facing error leakage:** none. `LogAndLocalizedError` logs the
  raw error and renders only the locale key; the new
  `fmt.Errorf("till name %q already in use", name)` reaches the log only.
  Verified live in the test output (`[INFO] [sync_api] till name "Till 2"
  already in use`, with the HTTP body carrying only the localized text).
- **CLAUDE.md rules:** all raw SQL stays in `internal/data`
  (`guard-data-access.sh` green); no money involved; every new user-facing
  string goes through a locale key present in all four locale files
  (`guard-i18n.sh` green); no `left`/`right` CSS; no kiosk/checkout modal;
  no new page route, so no new `routes:` claim needed.
- **i18n spot-check.** `web/locales/` contains exactly four files
  (`en/ar/fa/tr`) — all four updated, key sets in parity. The ar/fa/tr
  strings are real translations, not copy-paste or machine gibberish, and
  they say what the English says (checked word by word; e.g. tr *"Bu kasa
  adı bu dükkânın ağında zaten kullanılıyor — farklı bir ad seçin ve tekrar
  deneyin"* = "this till name is already used on this shop's network —
  choose a different name and try again"). Same for the four `multitill.md`
  sentences.
- **Help topic.** The added sentence is accurate for *both* join paths
  (paste-the-code and discovery/approve-to-pair), because the approve-to-pair
  flow drives the same `completeJoin` and therefore gets the same
  `joinErrNameTaken`. It sits at the end of step 3, the step that covers
  both. Front matter untouched; `guard-help-topics.sh` green.
- **Scope creep:** none. The diff touches exactly the files the narrowed
  scope calls for.
- No real client/shop name and no secret-shaped literal anywhere in the
  diff. No file-write path, so neither of this pipeline's two recurring bug
  classes (`os.MkdirAll`, cwd-relative path where `paths.Data` belongs)
  applies.

## TDD claim — independently re-verified, not taken on faith

Done in this isolated worktree, revert→run→restore treated as atomic
(single script invocation, no turn boundary in between), then confirmed the
restored files were byte-identical to the diff under review and
`go build ./...` clean.

- **Both production files reverted** (`internal/data/tills_repo.go`,
  `internal/pages/sync_api.go`):
  - `internal/data` fails to build — `repo.NameTaken undefined (type
    *TillsRepo has no field or method NameTaken)` ×4. Expected shape for a
    brand-new method under test, not a failure masking something else.
  - `internal/pages` fails for the *right* reason, on the actual assertion:
    `enrolling duplicate name "Till 2": expected 422, got 200` and
    `enrolling with the primary's own name: expected 422, got 200`, each
    with the full 200 body printed.
- **Only the handler reverted** (repo restored, so the tests compile): the
  same two `expected 422, got 200` failures — proving the tests pin the
  *handler's* behaviour, not merely the existence of the repo method.
- `TestSyncEnroll_UniqueNameStillSucceeds` and
  `TestSyncEnroll_DefaultNameStillSucceeds` pass both pre- and post-fix, as
  regression guards should — they are not vacuous, they pin that the new
  reject path did not start rejecting good names.
- Restore verified: `diff` against the reviewed files clean, `go build
  ./...` clean, `git status` back to the expected 12 modified files.

## Findings

### Fixed 1 — the case-insensitive comparison was not actually case-insensitive on the SQL side (correctness, would have shipped)

`NameTaken` used `WHERE lower(name) = lower(?)`. **SQLite's built-in
`lower()` folds ASCII only** — it is not Unicode-aware without ICU.
`strings.EqualFold`, used for the primary's own name in the same `if`, does
full Unicode simple folding. So the two halves of one check disagreed: the
same pair of names was a duplicate against the primary but *not* against a
sibling.

Confirmed empirically against this repo's actual driver
(`modernc.org/sqlite v1.29.10`) with a throwaway probe before fixing:

```
stored="Ünite"  probe="ünite"  taken=false     <-- should be true
stored="Café"   probe="CAFÉ"   taken=false     <-- should be true
stored="Till 2" probe="TILL 2" taken=true
```

On a product that ships **tr/fa/ar** this is a live gap, not a theoretical
one: "Kasa Ünite" / "kasa ünite" would both enrol.

Fixed by doing the fold in Go inside the repository method (`SELECT name
FROM tills`, then `strings.EqualFold` on trimmed values) so both halves of
the check use identical semantics. SQL stays in `internal/data`, so the
repository-pattern guard is unaffected; a shop has a handful of tills, so
reading the column is cheap. `strings.TrimSpace` added on both sides for
symmetry with the handler, which already trims the incoming name.

Covered by new assertions in `TestTillsRepo_NameTaken`, **verified to fail
against the original implementation** (reconstructed the exact
`lower(name) = lower(?)` body and re-ran): `expected "ünite" to collide
case-insensitively with an existing non-ASCII till name`.

### Fixed 2 — the new check silently shadowed `InsertTill`'s own failure coverage

`TestSyncEnroll_InsertTillFailureIsLocalized` drops the `tills` table to
force a failure. With `NameTaken` now reading the same table *first*, the
request never reached `InsertTill` — proven from the log line the test
produces:

```
before: [ERROR] [sync_api] insert till: SQL logic error: no such table: tills (1)
after:  [ERROR] [sync_api] till name taken: SQL logic error: no such table: tills (1)
```

The test still passed and its observable contract (500 + localized message
+ no raw SQL leak) was still asserted — but it was now testing a different
code path than its name claims, and `InsertTill`'s 500 path had quietly
lost all coverage. Judged worth fixing rather than accepting, because
"passes for a reason nobody noticed changed" is exactly the false-confidence
class this pipeline's review step exists to catch.

Split into two tests:
- `TestSyncEnroll_NameCheckFailureIsLocalized` — the drop-table variant,
  renamed to what it now actually covers.
- `TestSyncEnroll_InsertTillFailureIsLocalized` — restores dedicated
  coverage using a `BEFORE INSERT ... RAISE(ABORT, ...)` trigger, so the
  name-uniqueness `SELECT` succeeds and the *write* genuinely fails. A real
  database failure, not a mock, matching this file's existing "no mocks"
  convention. Verified it reaches the intended path:
  `[ERROR] [sync_api] insert till: constraint failed: tills insert blocked
  by test trigger (1811)`, plus an assertion that no till row survives.

### Fixed 3 — the primary's own name was resolved in the *caller's* locale

The handler used `httpx.ResolveLocale(w, r)` to resolve
`tillNameOrDefault`. That honours the request's `?lang=` / `ut_lang` cookie
— i.e. **the caller** chose which locale's default till name the primary
compared itself against. With `till.name` unset, a caller holding a valid
one-time token could POST `?lang=en` and enrol as `صندوق ۱` on a
Farsi-default primary, since the comparison would be against the English
default `Till 1`. That is precisely the collision this card exists to
prevent.

This is an identity comparison, not text rendered for the caller, so it
must use the primary's own configured locale. Changed to
`httpx.DefaultLocale()`. For the real machine-to-machine call (no cookie,
no query) the two are already identical, so no legitimate flow changes —
this is hardening that removes caller control over a server-side check. It
also drops a pointless `Set-Cookie` side effect on an API response.

### Accepted 1 — TOCTOU race between `NameTaken` and `InsertTill`

Real: there is no unique constraint on `tills.name`, so two enrolments with
the same name could both pass the check before either inserts. Accepted,
deliberately:

- Reaching it requires two *separately minted* one-time enrolment tokens
  (`tokens.consume` burns each) redeemed with the same name within the same
  few milliseconds, on a manager-driven pairing flow. Not machine-generated
  traffic.
- The consequence is exactly the pre-fix status quo — two similarly named
  tills — not corruption, data loss, or a security boundary crossed.
- The obvious hardening (a `UNIQUE INDEX ON tills(lower(name))`) is
  **actively wrong here**: migrations are append-only, and the shops that
  motivated ut-docs#1264 are precisely the ones that already *have*
  duplicate rows, so the index would fail to build and brick their upgrade.
  It would also reintroduce SQLite's ASCII-only `lower()`, the bug fixed
  above.

Worth a Backlog card only if enrolment ever becomes automated.

### Accepted 2 — renaming a till can still create a collision

`POST /api/settings/till-name` (`settings_page.go`) writes `till.name` with
no uniqueness check, and there is no rename path for a sibling's row. So
uniqueness is enforced at enrolment only, and a later rename can still
produce a duplicate. This is the part of ut-docs#1264 that BA/Architect
deliberately narrowed out; flagging it here so it is on the record rather
than assumed covered.

### Accepted 3 — an empty name still defaults to the literal `"till"`

Unchanged pre-existing behaviour, now interacting with the new check: the
*second* till to enrol with a blank name is rejected instead of silently
becoming a second `"till"`. That is the intended improvement, and the UI
asks for a name anyway. Pinned by `TestSyncEnroll_DefaultNameStillSucceeds`.

## BLOCKING prerequisite — `make docs-shots` has not been run

`scripts/ci/guard-docs-shots.sh` is a **CI-blocking guard in `ci.yml`'s
`build` job** and it was not in the review brief's gate list. It **fails on
this diff**:

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
                  changed since the manual's screenshots were last taken
guard-docs-shots: topic markdown changed since its screenshot was taken (locale/topic):
  - en/multitill
  - fa/multitill
  - ar/multitill
  - tr/multitill
guard-docs-shots: run `make docs-shots` and commit the result
```

Verified this is caused by *this diff* and is not pre-existing: stashing the
working tree makes the guard pass at the branch's base commit
(`✓ docs-shots guard: 23 routed topics × 4 locales screenshotted and fresh`),
and popping it back reproduces the failure. Both triggers are legitimate —
the four `multitill.md` topics changed, and `internal/pages/sync_api.go`
registers the screenshotted `GET /tills` route, so the surface hash moves.
(`guard-docs-shots_test.sh` also fails, purely as a knock-on of the real
guard failing; it goes green once the manifest is regenerated.)

**This cannot be fixed in this reviewing container:** there is no
pre-installed Chromium, and `npx playwright install chromium` is refused by
the environment's egress proxy (`403 ... no rule or allowlist entry allows
host "cdn.playwright.dev"`). `web/help/img/manifest.json` says "Do not edit
by hand" and hand-patching hashes is exactly what the guard exists to
prevent, so it was deliberately **not** done. The pixels almost certainly do
not change (the diff adds an API branch, nothing that alters the rendered
`/tills` page), but that must be established by regenerating, not asserted.

**Action for the orchestrator:** run `make docs-shots` on this branch in an
environment with a launchable Chromium and commit the result alongside this
diff.

**Resolved (orchestrator, same day):** ran `make docs-shots` in this
session's own environment, which does have a pre-installed, launchable
Chromium (`/opt/pw-browsers`, unlike the reviewer's isolated worktree). All
92 screenshots (23 topics × 4 locales) regenerated and passed; as
predicted, the rendered `/tills` pixels did not change for any topic —
only `web/help/img/manifest.json`'s recorded hash moved, plus one
unrelated, pre-existing rendering nondeterminism in `web/help/img/ar/sell.png`
(5 bytes of PNG-encoder jitter, same dimensions, not a topic this card
touches). `guard-docs-shots.sh` now passes:
`✓ docs-shots guard: 23 routed topics × 4 locales screenshotted and fresh`.
No other gate re-ran since; all of them were already green per the table
below and nothing in this step touches Go code.

## Gate output

Run in the isolated worktree, after all fixes:

| Gate | Result |
| --- | --- |
| `gofmt -l .` | empty |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./...` (full suite, 41 packages) | all `ok` |
| `guard-data-access.sh` | ✓ no inline SQL outside `internal/data` / `internal/db` |
| `guard-i18n.sh` | ✓ 1304 template keys resolve; all locales match en.json |
| `guard-help-topics.sh` | ✓ no route conflicts, all locales complete, every page route claimed |
| `guard-compliance-claims.sh` | ✓ 221 files scanned, no forbidden claims |
| every other guard in `ci.yml`'s `build` job | ✓ except `guard-docs-shots.sh` / `guard-docs-shots_test.sh` (see above) |

Relevant new/changed tests, all passing and all verified to hit their named
code path:

```
--- PASS: TestTillsRepo_NameTaken
--- PASS: TestSyncEnroll_NameCheckFailureIsLocalized
--- PASS: TestSyncEnroll_InsertTillFailureIsLocalized
--- PASS: TestSyncEnroll_RejectsDuplicateSiblingName
--- PASS: TestSyncEnroll_RejectsPrimaryOwnName
--- PASS: TestSyncEnroll_UniqueNameStillSucceeds
--- PASS: TestSyncEnroll_DefaultNameStillSucceeds
```

## Not verified

No driven run of the real app: the container has no launchable browser (see
the blocking section). The replica-side surfacing of the new error was
reviewed by reading the `completeJoin` → `joinError` → `friendlyJoinError`
→ locale-key path end to end, and the primary-side behaviour is covered by
handler-level tests against the real mux and a real SQLite database — but
the sentence an owner actually reads under the Join button was not observed
on screen.
