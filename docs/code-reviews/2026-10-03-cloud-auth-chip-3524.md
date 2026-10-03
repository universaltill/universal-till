# Review: 3 × 401 → sell offline, hourly retry, cloud-credential chip (ut-docs#3524, ADR-0116 D6)

- **Branch**: `fix/3524-401-chip-hourly-retry`
- **Author**: Opus, the pipeline's Dev subagent.
- **Tester**: the session model — four TDD claims re-verified by mutation, a
  real driven run (real binary, two real tills against a fake 401-answering
  cloud, the real 2-minute scheduler reaching a genuine third 401, real
  screenshots of two chip variants, a real offline sale with SQLite rows),
  two Playwright status-bar specs (26/26), contrast 4.70:1, 360/375 px
  widths, the i18n guard. Found the one blocking bug (icon collision with
  `TestNoTwoNavDestinationsShareAnIcon`), fixed by the orchestrator before
  this review (`sync` → `sparkles`, the icon `sb-enrol` already uses for the
  same `/settings#registration` destination).
- **Reviewer**: Fable, as an independent subagent with a fresh context, a
  different model from the author.
- **Date**: 2026-10-03

## Scope

Diff `81da0a1..HEAD`, 23 files, +876/−12. Binding spec: ADR-0116 §D6.

- `internal/cloudsync/{cloudsync,checkin,diagnostics,schedule}.go` and the
  new `auth_lockout_test.go`
- `internal/pages/cloud_auth_chip.go` (+test), `demo_mode.go`, `init.go`
- `internal/auth/middleware.go`
- `web/ui/partials/cloud_auth_chip.html`, `web/ui/layouts/base.html`,
  `web/public/app.css`
- `web/locales/{en,ar,fa,tr}.json` (4 new keys), `web/help/{en,ar,de,fa,tr}/claim.md`,
  `web/help/img/manifest.json`

## What shipped

- **The machine code on the wire.** `post()` and `getCheckin()` decode the
  `{error:{code}}` envelope into `statusError.Code` for a 401 only;
  `parseCloudErrorEnvelope` is factored out of `decodeCloudError` so both
  parse the same bounded body. Every other status leaves `Code` empty, so
  no existing caller's view of a 429/503 changes (tested).
- **The streak.** `scheduler.next` folds each tick's outcome into an
  `authTracker` (`tillAuth`, package-level, mutex-guarded): a success
  resets it, a 401 extends it, anything else (503 `auth_unavailable`, 429,
  5xx, transport) is neutral. At three consecutive 401s every wait is a
  flat `authRetryWait` (1 h) until a tick succeeds, whatever fails in
  between. The generic `fails` backoff is untouched for non-401 failures.
- **The chip.** `GET /ui/cloud-auth-chip` (polled every 60 s from
  `base.html`'s status bar, listed in `shellGetPaths`,
  `backgroundPollPaths` and the demo allow-list) returns an empty 200
  unless locked out, else the partial: `revoked` for `device_revoked`,
  `repair` for `token_retired`/`unauthorized` on a store with an owner,
  `register` on an anonymous one. Every role sees it; only a viewer who
  may open Settings gets the `href` (the diagnostics-chip gating). It is
  the same amber pill as `sb-main-till`, wraps for long locales, and is
  added to the sale screen's `:has()` rule so the status row comes back
  when it is the only problem.
- **Manual and locales.** A "When the cloud stops accepting this till"
  section in `claim.md` for en/ar/de/fa/tr; the four keys in en/ar/fa/tr
  with real translations (not English fallback — read, not just
  guard-checked).

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **should-fix (CI red)** | `web/help/img/manifest.json`'s `surface_sha256` was computed before the orchestrator's `sync`→`sparkles` icon fix changed `cloud_auth_chip.html`, so `scripts/ci/guard-docs-shots.sh` failed on the commit under review. The CI-equivalent `go test` command the pipeline treats as "all green" does not run the guard scripts, which is how a thorough Tester pass and a re-run after the fix both missed it. | **Fixed.** The chip appears in no manual screenshot (the docs-shots harness has no 401-refusing cloud), so the icon swap alters no rendered screenshot pixel — the case the guard's own message routes to `scripts/ci/update-docs-shots-surface-hash.sh`. Refreshed, guard green, committed separately with the house `Docs-Shots-Unchanged: true` trailer. |
| 2 | nit (Tester's) | `Start`'s `kick = nil` while backing off also silences kicks for the whole hourly lock-out wait; nothing tied that to this card. | **Fixed.** Comment at the `kick = nil` site: a credential written during the wait is first tried on the next hourly tick, the chip stays until that tick succeeds, and the earlier wake belongs to the "Pair with a shop" screen (ut-docs#3523) — a cloud nudge cannot be it because the link itself is refused on a 401. |
| 3 | design, accepted | Known gap (a): `storeHasOwner` infers an owner from the cached entitlement (a paid plan, or `active`/`lapsed` status). A claimed store that never subscribed reads as anonymous and gets "Register as a new store" instead of "This till needs pairing again". I looked for a better local signal: `marketplace.merchant_id` is written once at registration and falls back to `store_id`, and the sync/check-in contract carries no owner field — so the Dev's "no real flag" is right, not a shortcut. | **Accepted for this card.** The population with no entitlement evidence is dominated by auto-registered anonymous tills (ADR-0071 opt-in), so the default errs the right way; and today both variants link to the same page, so a misclassification changes words only. It becomes real when #3523 gives the two variants different destinations. **Deferred (backlog, cross-repo):** the cloud should send a claimed/owner signal on check-in or sync and the till should store it; the inference goes once it does. |
| 4 | design, accepted with a dependency | The chip's only link is `/settings#registration`. For an enrolled till that card shows "Registered", the store/device ids, the fleet list and a claim-code button — no pairing action and no "register as a new store" action. So "Register as a new store" promises an action the destination does not have, and "needs pairing again" lands where pairing cannot be done. The partial's comment says this is interim until ut-docs#3523. | **Accepted.** The chip is still a strict improvement: before this card a revoked till showed nothing and retried every 2–10 min. ADR-0116 D6 itself names "Pair with a shop" as the link target, and that screen is #3523. **Deferred to #3523, explicitly:** (a) point the chip at the pair screen; (b) give the anonymous-store variant a real "register as a new store" path; (c) see finding 5. |
| 5 | design, accepted with a dependency | Known gap (b): the chip and the hourly wait persist up to an hour after a working credential arrives, because only a successful tick clears `tillAuth` and nothing wakes the scheduler. By contrast `cloudlink`'s `Run` re-reads its gate every 30 s and redials as soon as the bearer changes (`revokedFor`), so the link recovers within 30 s while sync takes up to an hour. | **Accepted, cannot be fixed here:** there is no pairing flow in the till yet to hook. **Deferred to #3523 (c):** on a new credential, wake Start's loop (or reset `tillAuth` and kick) — the cloud link's `revokedFor` gate is the model. Note that a wake must *not* bypass the lock-out on an unchanged credential, or a pairing-screen retry button becomes a way to hammer the cloud. |
| 6 | nit, accepted | The explanation (`status.cloud_auth.hint`) lives only in `title=`; the house precedent `main_till_status.html` explicitly says a title tooltip has no hover on a touchscreen till. | Accepted: the ADR fixes the chip's words, the words carry the state, and the help topic the chip's page links to (`helpLink "claim"`) has the explanation. A cashier gets the words and no link, which is the intended gating. |
| 7 | nit, accepted | Non-401 failures *while locked out* (a 503, a dead network) keep the hourly wait rather than returning to the 10-minute cap. | Correct, not a bug: D6 says the till slows to hourly until pairing, and a 503 or an unreachable host says nothing about the credential. Tested (`TestScheduler503NeverCountsTowardOrResetsThe401Streak`). |
| 8 | checked, fine | An unregistered till's tick returns `(false, nil)` and that resets the streak. | Right behaviour: a till whose credential was removed locally has nothing to be refused; the chip should go. |
| 9 | environmental | `guard-deadcode-baseline.sh` fails on `internal/logging/file.go` (`Stderr`, `timestampWriter.Write`); `guard-shellcheck-version.sh` finds no `shellcheck` binary here. | Not this card's: the branch deletes nothing in `internal/logging` or its callers (`git diff 81da0a1..HEAD` has no `-` line touching them), so the deadcode result is pre-existing or tool-version-dependent; shellcheck is a local tooling gap. Both will show their true state on CI's `ubuntu-latest`. |
| 10 | process, deferred to the orchestrator | Four brand-new `en.json` keys → `ut-plugin-language-{de,es}` follow-up PRs (reviewer skill step 4: merge core first, `lang-pack-drift` on `main` goes red until the pack PRs land in the same cycle). | Deferred, expected. |
| 11 | nit, deferred | No manual screenshot of the chip: the docs-shots harness cannot put a till into the refused state. The Tester's real screenshots are the evidence for now. | Deferred; a harness fixture (fake cloud answering 401) would make it screenshot-able and is small, but belongs with #3523's screen anyway. |

Also checked and found fine:

- **The two recurring bugs.** No file write anywhere in the diff, so no
  `os.MkdirAll` to miss; no path at all, so no cwd-relative path where
  `paths.Data(...)` belongs. The e2e test's only path is `t.TempDir()`.
- **Money/tax.** None in production code. The driven test's sale uses
  `money.FromMinor` at the boundary and `TaxRateBasisPoints: 1900` as
  `int64`, per CLAUDE.md.
- **Secrets, client names.** None: fixtures are `store-1`/`tok-1`,
  `Coffee Beans`/`SKU1`, `no route to host`.
- **Concurrency.** `tillAuth` is a pointer never reassigned; one writer
  (Start's goroutine via `next` → `observe`) and readers (`AuthChipStatus`
  from handlers) each take the mutex; `next` calls `observe` then
  `lockedOut` as two locked sections, which is fine because only the
  writer's own goroutine sits between them. `-race` on the new tests and
  the whole `cloudsync`/`auth` packages is clean. `cloudsync` has no
  `t.Parallel`; `pages`' two parallel tests cannot overlap the sequential
  chip tests that swap `cloudAuthChipStatus`. The driven test resets the
  package-level tracker on entry and on cleanup.
- **Which 401s count.** `tick` returns `planCheckin`'s and `pushSync`'s
  error directly, so both the check-in GET and the sync POST feed the
  streak; the post-sync pushes (snapshot, order tracking, rollups, directive
  results) only run after a successful sync, so a locked-out till really
  does make one round of cloud calls per hour. The issue-report and
  diagnostics uploads at the top of `tick` ride the same hourly tick.
  `alerts` ticks every 24 h. `cloudlink` stops dialling on a 401 until the
  bearer changes. So "slows cloud calls to hourly" holds across every
  caller that uses the till credential, not just the scheduler.
- **No raw cloud string reaches HTML.** `normaliseAuthCode` maps anything
  but the two named codes to `unauthorized`, and the template branches on
  the three fixed variant constants.
- **Bounded reads.** `checkin.go` reads the 401 body through
  `decodeCloudError`'s 64 KiB limit and then `drainBody`; `post` already
  buffered the whole body before this card and now truncates to the same
  limit before parsing.
- **UX checklist** (`reference/ux-guidelines.md`): `var(--warning)` and
  the identical ink to the `sb-main-till` pill (pattern reuse, not a new
  colour); `white-space: normal` so the de/tr sentences wrap instead of
  overflowing (Tester checked 360/375 px); no hover-only affordance (the
  title is supplementary, see finding 6); logical properties only
  (`margin-inline-start`), `inline-flex` so icon/text order follows
  `dir`, symmetric padding and radius — RTL-safe for ar/fa; the test
  asserts no `<dialog>`/`showModal`/`alertdialog`; the sale-screen status
  row's `:has()` list gains `.sb-cloud-auth` so status stays reachable
  when this is the only problem; the help section's three bullets match
  the three chip strings in every locale.
- **Manual shipped with the feature (#324).** The `claim.md` section is
  present and substantive in all five shipped help locales (what the chip
  means, that selling continues, the three variants, who can tap it, what
  clears it); `guard-help-topics.sh` and `guard-help-drift.sh` pass.
- **Help manifest.** `claim` has no entry under `topics` because it is not
  a screenshotted topic; only `surface_sha256` moves (finding 1).

## Verification (run by the reviewer, not re-read)

- **TDD claims re-verified by mutation, five, each revert → run → confirm
  the right failure → restore:**
  - A. `checkin.go` without `se.Code = decodeCloudError(resp).Code` →
    `TestCheckin401DecodesMachineCode` fails (`Code:""`, want
    `token_retired`).
  - B. `schedule.go` without the `lockedOut()` → `authRetryWait` override →
    `TestSchedulerGoesHourlyOnTheThirdConsecutive401` (wait 9m59s, want
    1h), `TestScheduler503NeverCountsTowardOrResetsThe401Streak` and the
    driven `TestRevokedTillLocksOutHourlyAndStillSellsOffline` (waits
    `[2m 4m 5m 5m]`) all fail.
  - C. `observe` counting any `statusError` (503 included) →
    `TestScheduler503NeverCountsTowardOrResetsThe401Streak` fails (1h after
    two 401s around 503s) and the driven test fails ("chip shown after
    2×401 + 1×503").
  - D. `storeHasOwner` without the subscription-status branch →
    `TestStoreHasOwner/local_plan,_lapsed_subscription` fails.
  - E. `/ui/cloud-auth-chip` removed from `shellGetPaths` →
    `TestShellRequestsAreBoardScoped` fails naming the route.
  - Restored after each round; `git diff --stat` showed only the two
    review fixes before committing.
- `gofmt -l .` — empty. `go build ./...` — ok. `go vet ./...` — ok.
  `golangci-lint run ./...` — 0 issues.
- CI-equivalent suite,
  `go test $(go list ./... | grep -vE '/internal/plugins$|/internal/plugins/(oauth|marketplace)$|/internal/pages$|/internal/(enroll|logging|netreach)$')`
  — see the gate tail below.
- `go test -timeout 30m ./internal/pages/` and `./internal/plugins/` — ok.
- `go test -race ./internal/enroll/ ./internal/logging/ ./internal/netreach/`
  — ok; `go test -race ./internal/cloudsync/ ./internal/auth/` — ok.
- `bash scripts/ci/guard-i18n.sh` — clean.
- Every `guard-*.sh` and `check-brand-assets.sh` the CI `build` job runs
  (ADR/Android/iOS ones excluded: need tokens or SDKs): all pass except
  the two environmental ones in finding 9; `guard-docs-shots.sh` passes
  after finding 1.
- **Not exercised by me:** the rendered chip in a browser and the real
  scheduler reaching a third 401 on the wall clock — the Tester's driven
  run covers both with real screenshots and real DB rows; I re-ran the
  in-process driven test (`TestRevokedTillLocksOutHourlyAndStillSellsOffline`)
  under `-race` instead.

Gate tail, after both fixes:

```
GOFMT: clean / BUILD: ok / VET: ok / LINT: 0 issues
CI-EQUIV rc=0, ok packages: 71
ok  internal/pages    168.364s
ok  internal/plugins  272.504s
ok  internal/enroll 2.563s   ok  internal/logging 8.298s   ok  internal/netreach 1.380s   (-race)
ok  internal/cloudsync 444.055s   ok  internal/auth 33.756s   (-race)
✓ i18n guard: 1994 template keys resolve; all locales match en.json; …
guards failed: 2  (guard-deadcode-baseline, guard-shellcheck-version — finding 9)
```

## Verdict

**Safe to merge**, with the deferred items above filed rather than lost.
The lock-out logic is correct against ADR-0116 D6 on every axis I could
break it on (the third 401, 503 neutrality, success reset, latest-code-wins,
hourly flatness, offline sale); the chip is a status chip and never a
modal, reachable on the sale screen, translated, RTL-safe and documented in
all five help locales. The one real defect found at review was a CI-red
stale surface hash (fixed). The two known gaps are honestly stated in the
code, are not harmful in this card's shipped state because both chip
variants land on the same page, and both sharpen into real requirements on
ut-docs#3523 — that card must pick up findings 3–5 (owner signal, chip
destination and register path, scheduler wake on a new credential).

## Deferred items

1. **ut-docs#3523 ("Pair with a shop")** — must: point the chip at the pair
   screen; give the `register` variant a real register-as-new-store path;
   wake the sync scheduler / clear `tillAuth` when a new credential is
   written, without letting a retry button bypass the lock-out on an
   unchanged credential (finding 5's model: `cloudlink.Run`'s `revokedFor`
   gate).
2. **Backlog, cross-repo (ut-cloud + till)** — a claimed/owner signal on
   check-in or sync, stored on the till, replacing `storeHasOwner`'s
   entitlement inference (finding 3).
3. **Language packs** — `ut-plugin-language-{de,es}` PRs for the four new
   `status.cloud_auth.*` keys (finding 10).
4. **Docs-shots fixture** for a refused-credential till so the chip gets a
   manual screenshot (finding 11); with #3523.
5. **Pipeline note** — the "CI-equivalent `go test`" command is not the CI
   build job: it skips every `scripts/ci/guard-*.sh`. A fix applied after
   the Tester's pass (here, the icon swap) needs the guards re-run, not
   just the tests.
