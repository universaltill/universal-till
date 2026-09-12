import { test, expect } from './fixtures';

// See fixture-drain-2141-a-leaves-a-held-row.spec.ts. This file asserts
// "nothing is parked" WITHOUT calling drainParkedOrders (or any other
// cleanup) itself -- the whole point is to prove `resetPosOncePerFile`'s
// own drain is what makes this pass, not an explicit per-test/per-file
// cleanup this file does on its own. If this ever starts failing again,
// something removed the fixture's drain, not this file's own hygiene.
//
// Uses the top-level `request` fixture, not `page.request` -- this test
// makes no browser assertion, so there is no reason to pay for a page at
// all (an independent review's own observation).
test('sees no held rows left behind by an earlier spec file (ut-docs#2141 fixture regression, part b)', async ({
  request,
}) => {
  const listing = await request.get('/ui/parked-orders');
  expect(listing.ok(), `GET /ui/parked-orders returned ${listing.status()}, expected 2xx`).toBe(true);
  expect(await listing.text()).not.toMatch(/data-held-id="/);
});
