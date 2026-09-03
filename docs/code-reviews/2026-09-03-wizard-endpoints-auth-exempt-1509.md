# Code review — wizard endpoints and the session wall (ut-docs#1509)

**Date:** 2026-09-03
**Branch:** `fix/1509-wizard-endpoints-auth-exempt`
**Scope:** `internal/auth/middleware.go` (one exempt path), `internal/auth/auth_test.go` (one new test)

## What was wrong

The product owner could not import items in the setup wizard: *"it said
unauthorized."* Reproduced on the pilot tablet running v0.10.1:

```
POST /api/import  ->  401  {"error":{"code":"unauthorized","message":"sign in required"}}
```

`POST /api/import` is what `web/ui/pages/setup.html` posts to
(`hx-post="/api/import"`) for the restore/import step, and it was not in the
auth middleware's exempt switch.

The handler already implements a first-boot exemption (ut-docs#1168,
`internal/pages/import_page.go:150`), and a well-designed one:

- preview only — the wizard's panel never sends `commit=1`
- **refuses** any request that carries a session, so a cashier denied
  `import_export` stays denied even in the odd state where no PIN-bearing
  user exists
- requires `NeedsFirstBoot`, which flips false the moment the PIN step
  creates the admin
- fails closed when `AuthSvc` is nil

None of it ever ran, because the middleware answered 401 first — and a
first-boot till has no operators, so the request could never have carried a
session. Unconditional failure, not a race.

## Fourth occurrence — the class, not the instance

Same shape as the first-boot pairing trio (ut-docs#289), `/api/setup/language`
(ut-docs#1092) and `/api/setup/tax-plugin` (ut-docs#1507). Every one was found
by a person hitting a 401 on real hardware.

The cause is structural and nobody's carelessness: `internal/pages` mounts
handlers on a bare `http.ServeMux` with **no auth middleware in front**, so
wizard endpoints are green in unit tests and 401 in the real app. The test
design cannot see this class.

## Change

1. `middleware.go`: add `"/api/import"`, with a comment recording both the
   reproduction and why it is safe.
2. `auth_test.go`: `TestSetupWizardEndpointsClearTheSessionWall` — reads
   `web/ui/pages/setup.html`, extracts every `action=` / `hx-post=` /
   `hx-get=` / `fetch()` target under `/api/`, and asserts each clears
   `Middleware`.

The test is deliberately **derived from the template**, not a hand-maintained
list: the previous fixes all added one path to a list that the next endpoint
was never added to. A new wizard endpoint is now checked the day it is
written.

Two guards against the test going quietly useless:

- a missing template is `t.Fatalf`, never `t.Skip` — a skip would restore the
  exact blind spot this closes;
- parsing zero endpoints is also fatal, so a stale regex fails loudly instead
  of passing vacuously.

## TDD — verified personally

Written before the fix, and it found the offender on its own rather than
being told:

```
--- FAIL: TestSetupWizardEndpointsClearTheSessionWall
    auth_test.go:573: wizard endpoint /api/import = 401, want 200 ...
```

Only `/api/import` failed; the other seven wizard endpoints already passed,
which independently confirms the exempt list was correct apart from this one.
After the change: `internal/auth` ok, `internal/pages` ok (46.5s),
`go build ./...` clean.

## Review findings

**Security — this is the part that deserved real scrutiny**, because
widening an auth exemption for a *catalog-import* endpoint sounds alarming.
It is safe, and unlike the `/api/setup/*` routes the reasoning is not "the
handler is first-boot-only" but "the handler does full authorization itself":

- **Configured till, unauthenticated request:** `canPerform` fails → no
  session present → `NeedsFirstBoot` false → **403**. Verified by the
  existing `internal/pages` import gate tests, which still pass.
- **Configured till, under-privileged session:** the `hasSession` branch
  denies before `NeedsFirstBoot` is even consulted — deliberately narrower
  than a bare first-boot check, and that nuance is preserved.
- **First-boot till:** preview only. `commit=1` is refused through the
  exemption; the wizard's real commit rides the final submit as the
  just-created admin and needs no exemption at all.

This is the same tier already accepted for `/api/sync/pair-request` and
`/api/settings/exit-to-os` — both exempt precisely because the handler
authenticates itself. Least privilege holds: one exact path, and the
authorization that matters was never in the middleware to begin with.

**Residual risk worth stating plainly.** The new test covers what
`setup.html` calls. An endpoint the wizard reaches by some other route — a JS
variable, a redirect chain, a different template — is still invisible to it.
That is a real narrowing of the class, not its elimination. The complete fix
would be to exercise the wizard against the fully-composed app (middleware
included) in an e2e test; worth a follow-up card, out of scope for a p1.

**Not fixed here:** ut-docs#1506 (step 3's "Skip for now" silently skipping
the fiscal plugin) and ut-docs#1508 (a raw error page leaving the pinned
kiosk with no route back to the OS). Both still open.
