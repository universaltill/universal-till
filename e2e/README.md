# UI / E2E suite (Playwright)

Real-browser tests against a REAL till: `playwright.config.ts` boots the
server via `run-till.sh` (throwaway data dir, demo catalog, auth off) and
drives Chromium through the operator flows.

    cd e2e && npm ci && npx playwright install chromium
    npx playwright test            # headless
    npx playwright test --headed   # watch it

Rules learned the hard way:
- **workers: 1** — the till's basket and settings are SERVER-side state
  shared by every spec; parallel workers race each other.
- A spec that adds basket items must COMPLETE its sale; one that flips a
  server setting (OSK) must restore it.
- `watchConsole(page)` fails a spec on any JS/console error — keep it in
  every spec; it's the layer Go tests can't see.
