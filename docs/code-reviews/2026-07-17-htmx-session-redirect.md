# Code review — PIN pad rendering inside the header (field bug) (2026-07-17)

Branch `fix/htmx-session-redirect`. Farshid's screenshot (v0.2.19): the PIN
sign-in card rendered INSIDE the nav header while the sale screen stayed
below.

## Root cause
The nav carries self-loading htmx fragments (`#sync-chip` polls every 30s,
`#session-chip` on load). When the operator session EXPIRES, the auth
middleware answered those fragment requests with `302 → /login`; htmx
follows redirects transparently and swapped the ENTIRE login page (logo +
PIN pad) into the fragment's slot — hence the login card embedded in the
header, recurring with every poll.

## Fix
The middleware now detects htmx (`HX-Request: true`) and answers
`401 + HX-Redirect: /login` — htmx performs a real browser navigation to
the login page instead of an in-place swap. Regular page loads keep the
302; API calls keep 401 JSON. Class-wide: covers every fragment (chips,
basket, buttons, held sales, any plugin panel) for both idle expiry and
lock-from-another-tab.

## Tests
Middleware test extended: fragment request without a session ⇒ 401 +
HX-Redirect and explicitly NOT 302. Full suite + both guards green.
