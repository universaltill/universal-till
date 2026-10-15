# Review — /backoffice Problems lines wrap on a phone (ut-docs#4226)

## What shipped
- **Root cause.** The issue looked like client state left behind by an error page. It is server-side:
  - A 503 on `/plugins/store/<id>` (the e2e till has no catalogue) logs `[page-error] … Get "http://127.0.0.1:8081/api/v1/catalog/plugins?device_arch=…&locale=…": dial tcp …`.
  - `/backoffice`'s Recent problems card lists that line in a header-less table. Its message cell could not break the URL, so the page was 558px wide on a 390/440px phone.
  - Every popup measured on `/backoffice` afterwards then failed phone-popup-fit-3351.
  - This hits a real shop the same way: any failed fetch logs its URL.
- **Fix.** `.backoffice-problem-msg { overflow-wrap: anywhere; }` (`web/public/app.css`) on the message cell (`web/ui/pages/backoffice.html`).
  - `anywhere`, not `break-word`: only `anywhere` lowers the cell's min-content, which table auto layout sizes columns from.
  - Same precedent as `#users-list td:first-child` (#3518).
  - The table has no `<thead>`, so the phone tier's `table-cards.js` never reaches it.
- **Spec.** `phone-popup-fit-3351.spec.ts` gets an error-page route back (`ERROR_ROUTE = '/plugins/store/any-listing'`), the detail route the card asked for.
  - It is ordered before `/backoffice`, so the sweep itself seeds the long line.
  - The loop asserts the route still returns 503, so the guard can't go vacuous if the e2e till gains a catalogue.
  - The distinct-popup floor moves from 28 to 30. 31 distinct are measured today; the rule stays "at most one drops out silently".

## Review
The planned Fable review was refused (usage credits exhausted). Sonnet stands in, per MODEL-ROUTING. It ran `go build`/`go vet` in an isolated worktree.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The "29 distinct" comment was stale (31 now), and floor 28 allowed three to drop. | **Fixed.** Comment now says 31; floor raised to 30. |
| 2 | nit | The guard was implicit: if the route stopped returning 503, the sweep would pass without testing the wrap. | **Fixed.** The loop asserts 503 on `ERROR_ROUTE`. |
| 3 | nit | The CSS comment's "~120px" was true only at 440px. | **Fixed.** It now gives the measured 558px width. |
| 4 | nit (optional) | No `min-inline-size` floor on the message column. | **Accepted.** The column takes all the leftover width; the time and level cells stay max-content. |

The reviewer checked that no other page renders the Problems ring. `logging.OpenProblems`/`Recent` feed the heartbeat payload, the issue-report bundle and server logic, none of which is a page. `error_page.html` prints a user-facing message, which the sweep already covers.

## Verified beyond automated tests
- **Reproduction.** A two-route sweep (error route, then `/backoffice`) failed at 440px; each route alone passed. Dumping localStorage, sessionStorage and cookies ruled out client state. Reading the wide `/backoffice` cell found the log line.
- **TDD, re-verified.** With only the template change reverted, the final spec fails at 390px (`/backoffice … page scrolls sideways while open (scrollWidth=558)`). With it restored, the spec passes at both 390 and 440px (31 distinct popups each).
- **Screenshots, looked at.**
  - At 360px, the message wraps inside the card and `scrollWidth` is 360.
  - At 1024×600, the layout is unchanged and `scrollWidth` is 1024.
  - Emulated touch only. The change is CSS wrapping, with no touch handlers involved.
- **No strings changed.** `overflow-wrap` doesn't depend on text direction, so RTL is unaffected.
- **Guards.** 45 of 46 build-job guards pass. `guard-shellcheck-version.sh` can't run in this container (no `shellcheck` binary), and no shell script changed.

## Verdict
Safe to merge.
