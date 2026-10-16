import { test, expect } from './fixtures';
import { createTable, deactivateAllTables } from './helpers';

// ut-docs#4458: deactivateAllTables used to loop `for (;;)` while a
// Deactivate button existed, so a Deactivate POST that kept failing spun
// until the test timeout. The refusal is mocked at the network layer (same
// test hook as htmx-panel-swap-error-banner-2179.spec.ts): every Deactivate
// POST answers like the replica guard does, a 303 to `/tables?err=x`, and
// the table stays active. The helper must throw after that one refused POST,
// naming `x` (the old loop spun to the 45 s test timeout instead).

test('deactivateAllTables fails fast, naming the ?err= key, when Deactivate keeps failing', async ({ page }) => {
  await deactivateAllTables(page);
  await createTable(page, 'E2E Fail Fast Table 4458');

  let refused = 0;
  await page.route('**/api/tables/*/active', (route) => {
    refused++;
    return route.fulfill({ status: 303, headers: { location: '/tables?err=x' } });
  });

  const err = await deactivateAllTables(page).then(() => null, (e: Error) => e);
  expect(err, 'a Deactivate that never takes effect must throw, not return').not.toBeNull();
  expect(err!.message).toContain('made no progress');
  expect(err!.message).toContain(': x');
  expect(refused, 'the helper gives up after the first pass that deactivates nothing').toBe(1);

  // Clean up for the next spec on this worker's till.
  await page.unroute('**/api/tables/*/active');
  await deactivateAllTables(page);
});
