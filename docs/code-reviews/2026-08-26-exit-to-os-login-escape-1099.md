# Code review: exit-to-OS escape hatch on the login screen (ut-docs#1099)

**Branch:** `fix/1099-exit-to-os-login-escape` (commits `d5164cd`, `20d578a`)
**Reviewer:** independent Opus pass (complexity:hard → Opus, deliberately not
the authoring model, per scrum-master's model routing) · **Author:** Fable
(this pipeline cycle)

## What shipped

`POST /api/settings/exit-to-os` — the manager's "leave kiosk/fullscreen and
hand the window back to the OS desktop" action — sat behind the session
middleware, so the one screen where a till actually gets stranded (the login
screen, where by definition nobody is signed in) was the one screen it could
not be reached from. On a fullscreen/kiosk Linux till with no OS chrome, that
made the sign-in gate a trap.

- **`internal/auth/middleware.go`** — `exempt()` now returns true for the
  exact path `/api/settings/exit-to-os`. The handler is untouched: its own
  live `AuthorizeManager` PIN check (sharing the device-wide keypad lockout)
  remains the real authorization, exactly as ADR-0064 and the product owner's
  #549 requirement already describe it. Same "handler authenticates itself"
  shape as the already-exempt `/api/sync/pair-request` and `/api/window-mode`.
- **`web/ui/pages/login.html`** — a collapsed-by-default `<details>`
  disclosure ("Locked out? Return to desktop") with a manager-PIN field, plus
  a page-local `<script>` that mirrors `settings.html`'s fetch + branch-on-
  response contract for this endpoint. `renderNotice` isn't available on this
  standalone document, so the message is written with `textContent` (never
  markup) onto the page's own `.login-error`/`.muted` classes.
- **`web/locales/{en,tr,fa,ar}.json`** — nine new `auth.exit_to_os.*` keys,
  including the three honest 503 sub-case messages.
- **`internal/auth/middleware_test.go`** — `TestExitToOSIsExemptButSettings
  SurfaceStaysGated` (exemption scoped to exactly this path) and
  `TestExitToOSReachableWithoutSessionCookie` (the real `Middleware()`, no
  cookie, must reach `next`).
- **`e2e/tests/login.spec.ts`** — a new Playwright case driving the real
  login page: collapsed by default, opens, a wrong PIN is visibly rejected,
  and the real admin PIN reaches the handler's no-shell 503 rather than a PIN
  rejection.
- **`web/help/{en,tr,fa,ar}/{users,display}.md`** + regenerated
  `make docs-shots` manifest.

## Verification (re-run personally, not taken on report)

- `go build ./...`, `gofmt -l .` (empty), `go vet ./internal/auth/...
  ./internal/pages/...`, and the **full** `go test ./...` — all clean.
- Guards run directly: `guard-i18n`, `guard-data-access`, `guard-help-topics`,
  `guard-compliance-claims`, `guard-docs-shots`, `guard-autofill-suppression`,
  `guard-htmx-loaded`, `guard-kiosk-engine`, `guard-emoji-font` — all pass.
- **TDD re-verification, Go side (revert → run → restore, atomically):**
  removed the `exempt()` block, re-ran the two new tests — both fail with the
  claimed reason (`middleware answered 401 itself — the exit-to-os handler
  … was never reached`); restored, both pass. `git diff --stat` confirmed the
  file returned byte-identical.
- **TDD re-verification, e2e side:** removed the `exempt()` block and re-ran
  the whole `auth` Playwright project. The new case fails exactly as designed
  — `Expected substring: "can't be reached" / Received: "Incorrect PIN or not
  authorized."`, i.e. the 401 the middleware would answer, rendered as the
  generic rejection. Restored → **11/11 pass** (run twice by me).
- **Touch operability, checked by hand rather than assumed** (ut-docs#1099's
  "operable with no keyboard" criterion): appended a throwaway case to the
  auth project on a 480×800 touch context — tapping the new PIN field opens
  `#osk`, the layout is the **numeric** pad (`data-k="4"` present, no letter
  keys), and tapping keys types into the field. 12/12 with the temp case;
  temp case then removed, tree verified clean. Confirms `inputmode="numeric"`
  + osk.js's document-wide `guardSweep`/click handling reach a field that
  starts life inside a closed `<details>`.
- i18n parity checked independently of the guard: all four locale files hold
  an **identical 1711-key set**, all nine new keys present and non-empty in
  every locale, and every `{{ T }}` key `login.html` references exists in
  `en.json` (no silent-typo lookup).
- `ServeMux` method behaviour confirmed experimentally (throwaway program):
  with only `POST` registered, `GET/HEAD/PUT/DELETE` on the exempted path
  answer **405**, so the widened surface is exactly one POST handler.

## Findings

### 1 · MAJOR (process) — the e2e coverage was, for a window, not on disk

At the start of this review the working tree held 17 modified files and
`e2e/tests/login.spec.ts` was **unmodified**; `grep` over `e2e/tests/*.ts`
for `exit-to-os` matched nothing. The handoff claimed "a real Playwright run
of `login.spec.ts` (11/11 including the new test) passed twice" — for that
window, nothing on disk backed the claim. It was restored mid-review as
commit `20d578a`, whose own message confirms the file had been dropped by a
`git checkout --` during Tester verification.

**Resolution:** no code change needed — the restored test is real, compiles
against the pre-existing `helpers.ts` exports it uses (`ADMIN_PIN`,
`watchConsole`'s optional `extraExempt`), and I verified it both passes with
the fix and fails without it. Recorded here because the near-miss is the
finding: a green Tester report survived the artifact it was based on being
deleted. Worth noting alongside ut-docs#386's shared-checkout hazard — this
review's own revert/restore steps were kept atomic within single shell calls
for the same reason, and the tree was re-verified clean after each.

### 2 · Security analysis of the widened surface — accepted, no capability gained

The question that matters: does exempting this path give an anonymous LAN (or
cross-origin) caller anything it did not already have?

- **PIN-guessing oracle:** no. `/api/auth/` is already prefix-exempt, and
  `POST /api/auth/login` feeds the *same* `Service.checkPIN` counter with no
  per-source rate limiter of its own. `AuthorizeManager` is strictly the
  narrower oracle (it additionally requires `manager`/`admin`/`super_admin`,
  and calls `recordFailure()` on a valid-but-under-privileged PIN).
- **Lockout DoS:** no new vector. The 5-failure/30s lockout is device-wide
  and unconditional — `checkPIN` short-circuits on `s.locked()` before any
  lookup, with no session-state gate anywhere in that path — and an anonymous
  caller could already burn it through `/api/auth/login`.
- **CSRF:** the request is a simple form POST, so no preflight — but it
  cannot succeed without the manager PIN, and the only cross-site effect
  available is the same lockout burn already reachable via the login endpoint.
- **Exactness:** `path == "/api/settings/exit-to-os"`, not a prefix. Pinned by
  test against `/api/settings`, `/api/settings/window-mode`,
  `/api/settings/osk`, `/api/settings/exit-to-os/extra` and
  `/api/settings/exit-to-os-not-really`. The middleware wraps the mux, so it
  sees the **raw**, un-normalised path: a traversal-style
  `/api/settings/./exit-to-os` fails the equality check and is 401'd — the
  fail-closed direction. A trailing slash is likewise not exempt.
- **Audit trail:** unchanged and correct. The handler audits with
  `approver.ID` from the live PIN check — never `auth.UserID(r)`, which now
  resolves to `"system"` on this path — so every real exit still records the
  authorizing manager. The `not_confirmed` 503 still writes its audit row
  (the lockdown break really happened); `kiosk_appliance` and `no_shell` still
  correctly write none.
- **ADR conflict:** none. ADR-0064 already describes this endpoint's gate as
  the manager PIN throughout, and already ships an auth-exempt window
  endpoint (`/api/window-mode`) on the same "the shell reads this before
  anyone signs in" reasoning.

**Verdict on the security question: safe as implemented.**

### 3 · MINOR — a 429 lockout is rendered as "Incorrect PIN or not authorized."

The handler answers `429` when the device is locked out, but the page's JS
funnels every non-2xx, non-503 status into `T.error`. An operator locked out
by *someone else's* failed keypad attempts is told their PIN is wrong. This is
copied faithfully from `settings.html` (so it is a pre-existing product
behaviour, not a regression), and `auth.error.locked` already exists as a
string — but it reads slightly worse here, on the screen an operator reaches
precisely when they are already stuck. **Not fixed:** out of scope for this
card, and it would want the same treatment on both surfaces at once.
Suggested as a small follow-up card rather than a silent drive-by change.

### 4 · MINOR — the delivered e2e case doesn't itself assert touch operability

The restored case runs in a non-touch context, so the "usable on a
keyboard-less kiosk" acceptance criterion had no direct browser assertion.
I verified it by hand instead (see Verification above) and it holds. **Not
fixed:** the ad-hoc check I ran would be a reasonable permanent addition, but
adding a test to a committed branch during review, for a criterion that
demonstrably passes, isn't worth the churn. Noted for whoever next touches
this spec.

### 5 · Nits — accepted as-is

- `.login-exit-os` has no rule in `web/public/app.css`; it's a selector hook
  only (the e2e case uses it). Layout comes from the reused `.login-setup`
  and two inline styles.
- Those two inline styles (`margin-block-start:1rem`, `text-align:start`,
  plus `min-height:1.2rem` on the message div) are hardcoded rather than
  tokens — identical to the `settings.html` block they mirror, and both use
  **logical** properties, so RTL (fa/ar) mirrors correctly with no extra CSS.
- The regenerated `web/help/img/ar/translations.png` differs by 10 bytes with
  no visible change — incidental `make docs-shots` re-render noise, not scope
  creep. The manifest diff is exactly the surface hash plus the two edited
  topics' hashes across four locales.

### 6 · Required follow-up outside this repo — `lang-pack-drift`

Nine new `en.json` keys land here; per this repo's CLAUDE.md the matching
additions are owed in the external `ut-plugin-language-de` and
`ut-plugin-language-es` packs. The PR-time check is advisory, but the
**push-to-`main` check is blocking** — so the packs need updating with, or
immediately after, this merge, or `main` goes red. Not a defect in this diff;
flagged so it isn't discovered by a red build.

## Confirmed clean (checked, no change needed)

- **Handler correctness / 503 sub-cases.** All three marker tokens
  (`kiosk_appliance`, `not_confirmed`, `no_shell`) are handled, and the
  fallthrough for `no_shell`/anything else maps to the generic "can't be
  reached" — matching the reference implementation exactly, with no
  copy-paste drift. `.catch()` on both the outer fetch and the body read.
  `btn.disabled` is re-enabled on every path.
- **First-boot branch.** The disclosure sits inside the `{{ else }}` of
  `{{ if .firstBoot }}` — correct: a brand-new till has no manager PIN, so
  offering the control there could only ever produce a rejection. The script
  is emitted unconditionally but guards on `if (!form) return;`.
- **i18n rules.** Every visible string goes through `{{ T }}`, including the
  JS ones via the page-local `var T = {…}` lookup object CLAUDE.md prescribes.
  Templates are `html/template`, and `T` returns a plain `string`, so those
  values are contextually escaped for JS-string position — a future language
  pack containing a quote can't break the script.
- **Offline-first / kiosk rules.** No modal blocker added; a collapsed
  `<details>` on a screen that previously offered nothing. This change makes
  "status/lock/exit must always be reachable" *more* true, not less.
- **Manual shipped with the feature.** `users.md` gains an accurate step 12
  and `display.md` an accurate closing sentence, in all four locales, and
  `users.md` already claims `/login` in its `routes:` so the `?` link
  resolves. Prose read for truthfulness against the actual behaviour — it is
  correct, including the "no sign-in needed first" claim.
- No real client/shop names as test data; no secret-shaped literals (the PINs
  in the e2e spec are the fixture's own `ADMIN_PIN` and an obvious wrong
  value).
- No SQL outside `internal/data`; no `os.MkdirAll`-class file-write handler
  and no cwd-relative path where `paths.Data(...)` belongs (this diff writes
  no files at all) — the two recurring bug classes this pipeline keeps
  finding do not apply here.

## Verdict

**Approve — safe to merge.** No blocker, and no code change made by this
review. The security question the card exists to ask ("is widening this
surface safe?") resolves cleanly: the exemption is exact-match, method-narrow,
fail-closed on unnormalised paths, grants no capability an anonymous caller
did not already have via the already-exempt login endpoint, and leaves the
audit trail intact. Both the Go and the browser-level regression claims were
re-verified by revert-then-restore, and the touch criterion was verified in a
real browser rather than inferred.

Deferred, explicitly: the 429-renders-as-wrong-PIN wording (finding 3, both
surfaces, as its own card), a permanent touch assertion in the spec
(finding 4), and the `ut-plugin-language-{de,es}` key additions (finding 6,
needed before/with the merge to `main`).
