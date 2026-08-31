import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#373: web/ui/pages/tills.html's inline <script> had an unbalanced
// brace — the .then(function (j) { ... }) callback in the discover-primaries
// fetch chain was never closed before .catch(...), a genuine JS SyntaxError.
// A <script> block that fails to parse never executes ANY of it, so
// #discover-btn's addEventListener never ran and "Find a primary on this
// network" did nothing at all — silently, since nothing exercised this
// page's actual inline script in a real JS engine (the Go handler tests
// only cover the backend endpoints, which were always fine). watchConsole()
// alone would have caught the old bug (browsers surface a <script> parse
// error as an uncaught exception), but this drives the real click end to
// end so a future regression here fails on behaviour, not just on "no JS
// errors happened to fire".
//
// The scan itself is a real mDNS broadcast (discoverBrowseTimeout, 4s) —
// this suite's own second (auth) project till shares the sandbox's network
// namespace and, being a standalone/primary itself, is a legitimate
// candidate the scan can find. So this asserts the chain reaches ONE of
// its two real terminal states (a rendered result list, or the explicit
// "none found" message) rather than assuming which — the old, broken
// script reached neither: msg stayed stuck on "Searching…" forever because
// the click handler that would have updated it was never even attached.
test('Find a primary on this network runs a real scan through to a result', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/tills');

  const btn = page.locator('#discover-btn');
  const msg = page.locator('#discover-msg');
  const results = page.locator('#discover-results');

  await expect(btn).toBeEnabled();
  await btn.click();
  await expect(msg).toHaveText('Searching…');

  // Both .then() branches disable-then-re-enable the button before doing
  // anything else, so waiting on this alone proves the callback that used
  // to never run (the syntax error broke the whole script, not just the
  // .catch()) actually executed. Timeout is generous relative to the
  // server-side discoverBrowseTimeout (4s, internal/pages/discovery_api.go)
  // to leave real headroom for the HTTP round trip + render under CI load,
  // not just the bare scan itself (independent review, ut-docs#373).
  await expect(btn).toBeEnabled({ timeout: 10_000 });

  const noneFound = await msg.textContent();
  const resultItems = await results.locator('li').count();
  expect(
    noneFound === 'No primaries found on this network.' || resultItems > 0,
    `expected either the "none found" message or at least one rendered result; got msg="${noneFound}", ${resultItems} result item(s)`,
  ).toBe(true);
  if (resultItems > 0) {
    await expect(results.locator('button', { hasText: 'Request to pair' }).first()).toBeVisible();
  }

  assertClean();
});
