# Code review — plugin store shows the full catalog; remove deregister

Date: 2026-07-16
Branch: `fix/store-catalog-and-remove-deregister`

## Bug: "no plugins in the POS"
The Plugin Store fetched the catalog fine (HTTP 200, 8 plugins) but rendered
"No plugins". Root cause: the store page filtered browse listings to
*entitled-only* — `if entitledKnown && !entitled[listingID] { continue }`. A
freshly enrolled **anonymous** store has zero explicit entitlements, and the
entitlements endpoint now returns 200-but-empty, so `entitledKnown=true` with an
empty set hid **every** plugin. This contradicts the intended model (Farshid:
"anonymous POS should see all plugins").

Fix (`internal/pages/plugins_store_page.go`): browse shows the **whole public
catalog**; access is enforced at install/download time (free = self-serve,
registered/paid = claimed store), not by hiding listings. Removed the
browse-time entitlement filter and the now-unused `fetchEntitledListings`.
Verified against prod: the store lists AI Assistant, themes, FAQ, etc.

## Removed: deregister button
Farshid: "deregistration is not working, remove it — I don't want it." Reverted
`enroll.Reset`, `POST /api/enrol/reset`, the Settings "deregister" control, and
the `settings.enrol.reset_*` i18n keys. Auto-enrolment on first boot stays.

## Checks
`go build ./...`, `go test ./...`, i18n guard (603 keys), data-access guard —
green. Live store-page smoke test lists the catalog.
