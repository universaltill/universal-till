# Code review — POS plugin store renders marketplace icons (2026-07-17)

Small follow-up to the marketplace icon work: the till's Plugin Store cards
now show each listing's icon. The catalog client already parsed
`iconUrl`/`icon_url`; the store page maps it into the card (relative paths
absolutized against the marketplace web base — endpoint minus /api) and the
template renders a 28px img beside the name (lazy-loaded; no icon = layout
unchanged). Offline note: icons are marketplace-hosted and only load while
browsing the store, which itself requires the marketplace — no offline-first
impact. Store page tests + i18n guard green.
