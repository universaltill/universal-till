# Code review: the guest page's language reaches the receipt (ut-docs#4715)

- **Date:** 2026-10-23
- **Card:** universaltill/ut-docs#4715 (complexity:medium, lane:cloud-41)
- **Change set:** ut-cloud `feat/4715-guest-order-locale`, universal-till
  `feat/4715-guest-order-locale`, ut-docs `docs/4715-guest-order-locale`
  (ADR-0159 amendment 2026-10-23). This record is identical in all three
  repos.
- **Author model:** Sonnet. **Reviewer:** Opus 5.5, in a fresh context and
  in isolated worktrees.

## What shipped

- **ut-cloud.**
  - `guestorder.Create` takes the guest page's language and adds it to the
    `guest_order` directive payload as optional `locale`. It is left out when
    empty, and no `GuestOrder` schema field is added.
  - `POST /t/{token}/orders` negotiates that language the way the GET page
    does (`guestText`: Accept-Language, then the main till's reported store
    locale). A cloud with no locales loaded sends none.
- **universal-till.**
  - `decodeGuestOrder` reads `locale` as optional untrusted input: a string
    of at most 35 bytes in `[A-Za-z0-9_-]`. A bad value is dropped and never
    refuses the order.
  - `cloudGuestOrder` sets the parked snapshot's `CustomerLocale` to
    `httpx.MatchLocale(locale, AvailableLocales())`: the exact code, else the
    base language, else `""` (the shop's language). The #4705 print path then
    prints the receipt at payment in that language.
  - `web/help/img/manifest.json`: only the `surface_sha256` line changed,
    because no rendered pixel changed (`Docs-Shots-Unchanged: true`).
- **ut-docs.** ADR-0159 amendment covering §4's payload field and §5's
  handling.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | When the cloud's i18n loader is degraded, `guestText` returns `en-GB` while the page shows raw keys. A non-English shop would then print an English receipt, a language the guest never read. | **Fixed.** The handler sends no locale when `guestI18n == nil`. |
| 1b | minor | When negotiation matches nothing, it falls back to `en-GB`. That is the language of the page the guest actually read, so it is consistent with this card. It is not reachable with today's locales: every till locale has a cloud counterpart. | Accepted, and documented in the ADR. |
| 2 | nit | The ADR amendment left out the final `en-GB` default. | **Fixed.** |
| 3 | nit | One over-long comment line in `orders.go`. | **Fixed.** |
| 4 | nit | No till-side test pins `fa-IR`→`fa` or `en-GB`→`en`. | Accepted. `MatchLocale` is shared and already tested, and the new test pins `tr-TR`→`tr`. |

The reviewer also checked these, with no finding:
- every other reader of `CustomerLocale`: auto-print, the receipt-screen print
  copy and the tracking QR. Each skips a value equal to the shop or screen
  language. No kitchen-ticket or Z-report path reads it;
- redelivery and orphan re-place, where the locale carries through;
- kiosk-engine isolation (the detached engine is used);
- bounds on the untrusted value.

## Verified

- **TDD.** The reviewer broke each production change in turn and re-ran its
  test. Each test failed with a real assertion, then passed again once the
  code was restored:
  - `TestCloudGuestOrder_RecordsTheGuestsLanguageOnTheParkedOrder`:
    `CustomerLocale = "", want "tr"`
  - `TestDecodeGuestOrder_Locale`: `Locale = "", want "tr-TR"`, plus the
    bypassed-validation cases
  - `TestCreateDirectiveCarriesLocale`
  - `TestGuestOrderDirectiveCarriesPageLanguage`:
    `map[:true], want tr-TR and de-DE`, and `map[en-GB:true tr-TR:true]` with
    the store-locale fallback removed
- **Gates.**
  - universal-till: gofmt, build, vet and golangci-lint (0 issues) all pass,
    and so does the full `go test ./...`. `internal/plugins` passed on a
    standalone re-run with `-timeout 25m`; it had hit the default 10-minute
    timeout while running next to the other gates. 118 of 123 `build`-job
    guard scripts were run and passed. `shellcheck` is not installed here,
    and two of the scripts are helpers, not guards.
  - ut-cloud: gofmt, vet and golangci-lint (0 issues) all pass, as do the
    `guestorder` and `httpapi/...` tests.
- **Known red on ut-cloud `main` (not caused by this change).**
  `internal/iconid.TestRegistryMirrorsTillLibrary` fails because
  universal-till#2115's barber icons (ut-docs#4840, another lane) have no
  ut-cloud mirror yet. It fails the same way with this change stashed.
- **Not exercised end to end:** a real till printing a receipt for a guest
  order from a phone. No UI surface changed.

## Verdict

Safe to merge. The till and ADR halves are additive and can land on their
own. Merging the ut-cloud half waits for its `manifest-contract-guard` to
go green, which needs the #4840 icon mirror on ut-cloud.
