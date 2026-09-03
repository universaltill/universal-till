# Code review — `/api/setup/tax-plugin` auth exemption (ut-docs#1507)

**Date:** 2026-09-03
**Branch:** `fix/1507-setup-tax-plugin-auth-exempt`
**Scope:** `internal/auth/middleware.go`, `internal/auth/auth_test.go` (+22 lines, no production logic changed beyond one switch case)

## What was wrong

`POST /api/setup/tax-plugin` — the setup wizard's step-3 "Germany tax plugin"
Install action (ut-docs#1180) — was never added to the auth middleware's
exempt-path switch. That switch is an **exact-path** match, deliberately so
(`TestMiddlewareExemptsFirstBootPairingRoutes` pins
`/api/setup/anything-else` → 401 to stop anyone turning it into a prefix
match), so an unlisted sibling under `/api/setup/` stays behind the session
wall.

The handler at `internal/pages/setup_tax_catalog.go:216` already documents
itself as *"Auth-exempt on the same first-boot-only window as POST
/api/setup/language — NeedsFirstBoot is the gate."* That statement is only
true if the path is listed in the middleware; it was not. The comment
described an exemption that did not exist.

The failure is **total, not intermittent**: a first-boot till has no
operators, so no session can be minted, so the middleware 401s every request.
The tile has never worked for any German shop since it shipped.

## Evidence

Reproduced on the pilot TECLAST tablet (v0.10.0, fresh first boot), same
till, same boot, two POSTs:

```
POST /api/setup/language     -> 303   (listed, works)
POST /api/setup/tax-plugin   -> 401   {"data":null,"error":{"code":"unauthorized","message":"sign in required"}}
```

That JSON is exactly what the product owner saw on screen.

## Change

1. `middleware.go`: add `"/api/setup/tax-plugin"` to the exempt switch,
   immediately after `/api/setup/language`, with a comment recording the
   reproduction so the next reader does not have to rediscover it.
2. `auth_test.go`: extend `TestMiddlewareExemptsFirstBootPairingRoutes`'s
   table with the route, alongside the `/api/setup/language` entry that was
   added for the identical bug (ut-docs#1092).

No behaviour changes for any other path; the exact-path posture and its
negative assertion are untouched.

## TDD — verified personally, not claimed

Test written and run **before** the fix:

```
--- FAIL: TestMiddlewareExemptsFirstBootPairingRoutes (0.01s)
    auth_test.go:346: exempt /api/setup/tax-plugin = 401, want 200
        (first-boot till can never hold a session)
FAIL
```

After the one-line middleware change:

```
ok  github.com/universaltill/universal-till/internal/auth    0.458s
ok  github.com/universaltill/universal-till/internal/pages  70.563s
go build ./...  clean
```

## Review findings

**Correctness — the fix is right but narrow.** It closes this route and
nothing else. That is the correct scope for a p1 field break, but it is the
**third** occurrence of one failure mode: the pairing trio (ut-docs#289),
`/api/setup/language` (ut-docs#1092), and now this. The middleware's own
comment already names the pattern — *"every bare-mux test green, real app
401ing"* — which is a strong signal the class, not the instance, is the real
defect.

**Root cause of the blind spot.** `internal/pages`' tests mount handlers on a
bare `http.ServeMux` with no auth middleware in front, so they exercise
`NeedsFirstBoot` and never touch the wall. Every new `/api/setup/*` route is
therefore born green and broken. Recommended follow-up (raised on
ut-docs#1507, deliberately **not** done here to keep a p1 fix small and
reviewable): a table-driven test that enumerates the wizard's registered
`/api/setup/*` routes and asserts each is either exempt or explicitly
declared session-required, so a new route cannot be added without making the
choice consciously.

**Security.** Widening an auth exemption deserves scrutiny. This one is
safe and does not widen the reachable surface in practice:

- The handler's own `svc.NeedsFirstBoot` gate still runs first and redirects
  to `/login` once any operator exists, so the window closes permanently
  after setup.
- The request body cannot pick a listing: the handler re-derives the locale
  from `countryTaxLocale[country]` server-side and re-resolves the match via
  `setupInstallableTaxPlugin` before installing, rejecting a forged or stale
  POST.
- Install still goes through the existing Ed25519-verified
  `cloudInstallPluginVersion` path — no second install path.
- It matches the posture already accepted for `/api/setup/language`, which
  installs from the same catalog through the same code.

Least privilege is preserved: one exact path, gated by the same first-boot
condition as its siblings.

**What this fix does NOT do.** ut-docs#1506 (step 3's "Skip for now"
silently skipping a legally-required plugin, and the false background-retry
promise) is untouched and still open — this only makes the Install button
work for an operator who presses it. ut-docs#1508 (the JSON error page left
the pinned kiosk with no route back to the OS) is also untouched.
