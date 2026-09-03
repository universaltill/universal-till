# Code review — /api/import needs optional auth, not exemption (ut-docs#1516)

**Date:** 2026-09-03
**Branch:** `fix/1516-import-optional-auth`
**Scope:** `internal/auth/middleware.go`, `internal/auth/auth_test.go`

## What was wrong — a regression from ut-docs#1509

ut-docs#1509 added `/api/import` to `exempt()` so the first-boot wizard
restore could reach its handler. `Middleware` short-circuits on exempt paths
**before** reading the session cookie:

```go
if exempt(r.URL.Path) {
    next.ServeHTTP(w, r)   // cookie never read — no user in context
    return
}
```

So from that change onward a **signed-in manager or admin** arrived at
`import_page.go` anonymous: `canPerform` false, the handler's `hasSession`
branch not taken, `NeedsFirstBoot` false on a configured till, answer **403**.
The ordinary import — the one a manager is fully entitled to perform — became
impossible.

Because htmx does not swap non-2xx, the operator saw the ut-docs#1510 spinner
flash and disappear, with no message at all. The attempt also **consumed the
staged upload**, so the previewed file had to be uploaded again.

Reproduced on the pilot tablet with an authenticated admin session:

```
GET  /import      -> 200
POST /api/import  -> 403 "Manager oder Administrator erforderlich"
```

## Why the original review missed it

The ut-docs#1509 review asked whether the exemption was too *permissive* and
concluded it was safe because the handler authorises itself. It never asked
the opposite question: exemption does not only skip the 401, it removes the
identity the handler authorises **with**. The change made the route strictly
less usable rather than more permissive — a failure direction the review had
no prompt to consider.

`/api/import` is the only route with this shape. Every other exempt path
either authenticates out-of-band (bearer tokens on `/api/sync/*`, a live
manager PIN on `/api/settings/exit-to-os`) or is genuinely first-boot-only
(`/api/setup/*`, which refuse once an operator exists). Import needs both:
anonymous during first boot (preview only) **and** session-aware afterwards.

## Change

New `optionalAuth(path)` tier: attempt cookie resolution first, attach the
user when it resolves, and fall through to the handler rather than 401 when it
does not. `/api/import` moves out of `exempt()` into it.

Placement matters and is deliberate — the fall-through sits **after** the
existing cookie block, so a valid session is always attached first and the
anonymous path is only reached when there is genuinely no usable session.

## TDD — both halves pinned in one test

`TestImportKeepsTheSessionWhileStayingReachableAtFirstBoot` asserts:

1. anonymous `POST /api/import` reaches the handler (200, no user) — what
   ut-docs#1509 existed to fix;
2. a signed-in manager's `POST /api/import` reaches the handler **with the
   user in context** — what ut-docs#1509 broke.

Written first; failed with the exact diagnosis before the fix:

```
signed-in POST /api/import reached the handler with NO user in context —
exempt() skips cookie resolution; /api/import needs optional auth, not exemption
```

Green after. `TestSetupWizardEndpointsClearTheSessionWall` (also from #1509)
still passes — optional auth satisfies "no 401 from the middleware".
`internal/auth` ok, `internal/pages` ok (89s), `go build ./...` clean.

## Review findings

**Security.** This is strictly narrower than what it replaces. Before,
`/api/import` bypassed the middleware entirely; now an authenticated caller is
identified and the handler's real authorization (`canPerform("import_export")`,
the `hasSession` denial branch, `NeedsFirstBoot`, preview-only in the
first-boot window) applies as designed. The anonymous path is unchanged from
what #1509 shipped, so no new surface is opened; the change only *restores*
identity that should never have been dropped.

**The tier is a loaded footgun and is documented as one.** Anything added to
`optionalAuth` must authorise itself in the handler — the tier promises only
"no 401 from the middleware", never "no authorisation required". The doc
comment says exactly that, because the next person to add a path here will be
in the same hurry I was.

**What this does NOT fix.** The wizard's own auto-commit
(`commitStagedImportForSetup`) constructs its request by hand and calls the
unwrapped mux with `auth.WithUser`, so it never passes through this middleware
and is not explained by this bug. The operator still lands on `/import` after
finishing the wizard with a backup selected — ut-docs#1515, still open and
still unexplained.

**Process note.** Two field-reported failures in one day traced to the same
root shape: a handler that authorises itself, sitting behind middleware whose
behaviour the handler's author assumed rather than verified. ut-docs#1509's
template-derived test catches "route added, exemption forgotten". Neither it
nor this one catches "exempted, identity lost". An e2e that drives the real
composed app as a signed-in manager would catch both, and remains the honest
gap.
