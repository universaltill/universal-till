# Review: plugin store tile grid and listing detail page (ut-docs#4180)

**Shipped.**
- `/plugins/store` shows a grid of small tiles. Each tile has a 56px logo, or a lettermark when the plugin has no icon or the icon fails to load. It also has the name (2 lines at most), the type, the trust and paid badges, and the install-state chip. The whole tile is one link, at least 44px tall.
- A new deep-link page, `/plugins/store/{listing_id}`, carries what used to be on the card:
  - the full description;
  - the permission badges and their plain-language lines (ADR-0121 §2);
  - Download / Install / Delete download / Manage, using the same `/api/plugins/store/*` calls;
  - the unverified-publisher consent prompt (ADR-0006) and the manager-approval notice.
- Both pages use the same `plugin_management` gate and the same cached catalogue snapshot, so they work offline. An unknown id returns 404.
- The help topic `plugins` claims the route and step 2 is rewritten, in en/de/tr/fa/ar.
- No new locale keys.

**Scope.** This is task one of epic #2845, built on today's catalogue data. The listing data (#4211), rendering it on the till (#4212) and the first-party assets (#4213) are filed as their own cards.

**Route.** `GET /plugins/store/{id}` overlaps `GET /plugins/{id}/settings`: both match `/plugins/store/settings`, and Go's ServeMux panics when neither pattern is more specific. So the route is registered as `GET /plugins/{section}/{listing_id}`, which serves only `section == "store"`. Any other `/plugins/x/y` gets the same 404 the `/` catch-all gave before. A test pins that the two routes coexist, and the demo-mode deny list carries the pattern.

**Reviewer.** Independent review on Fable; the author model was Opus 5.5. It ran build, vet, the pages/httpx/manual tests and 16 guards, and probed the ServeMux with every overlap case: escaped ids, HEAD, POST, trailing slash and three segments. Verdict: safe to merge.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | `printf "%q"` inside a `<script>` double-encoded the consent text, so `confirm()` showed literal `"` around the prompt. This was carried over from the old page. | Fixed: plain `{{ T … }}`. The test now asserts the exact JS literal; it failed before the fix and passes after. |
| 2 | Minor | The phone sweep entry `/plugins/store/sweep-listing` only ever renders the error page, because the e2e till has no catalogue, so it tests nothing it claims to. | Removed. The detail page is covered by Go render tests and by the driven run below. Sweeping error pages is #4226. |
| 3 | Minor | No e2e test clicks the moved actions or the consent prompt. The gap predates this change. | Accepted; the driven run below covered both. Filed as ut-docs#4228. |
| 4 | Nit | The detail page `<title>` was the bare listing name. | Fixed: "<name> — Plugin Store". |
| 5 | Nit | The pattern string appears in three places. | Accepted: tests tie them together. |
| 6 | Nit | The help `routes:` entry also claims the broad registered pattern. | Accepted: guard coverage requires it. |
| 7 | Nit | "Manage" on an installed listing goes to `/plugins`, not to that plugin's settings, the same as the old card. | Accepted: enhancement, folded into #4212. |

**Verified beyond unit tests.** Tester ran the real till against a fake marketplace with 5 listings: a working logo, a broken logo, no icon, a paid listing and an unverified one. It drove both pages with Playwright at 1280×800 en, 390×844 en and fa, 1024×600 ar and 1280×800 fa, plus dark theme. 174 checks passed:
- no horizontal scroll, and tap targets at least 44px;
- the lettermark replaces a broken logo;
- search and chips filter correctly;
- tile → detail navigation works;
- consent Cancel sends nothing, and OK posts;
- an unknown id returns 404, and `/plugins/<id>/settings` still returns 200.

The screenshots were looked at: Arabic at 1024×600 and the English phone detail page. They led to one fix: catalogue text now carries `dir="auto"` on RTL tills.

**Not checked.**
- German text lengths: de ships as a language pack, not in core.
- Real touch hardware.

**Gate.**
- `gofmt` clean; `go build ./...` passes.
- `golangci-lint` reports 0 issues.
- `go test ./...`: every package passes. `internal/plugins` needs the 20-minute timeout CI uses.
- Every `build`-job guard passes.
- `make docs-shots`: no screenshotted page changed, so the surface hash was refreshed (`Docs-Shots-Unchanged: true`).
