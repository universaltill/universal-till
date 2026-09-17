import { test, expect } from './fixtures';

// ut-docs#2345: the `default` project no longer shares ONE till across the
// whole run — every Playwright worker boots its OWN server (see the
// `workerServerURL` fixture in fixtures.ts) on the 9091+ band, so its ~134
// spec files can run in parallel without racing each other's server-side
// basket/settings state. This spec pins the contract that makes that safe:
// the URL every test in this worker resolves `page.goto('/')` against is
// the worker's own server, not a hardcoded shared port.
//
// Runs on the `default` project only (it is not in any other project's
// testMatch) — the other four projects keep their static per-project
// `baseURL` (8092-8095) untouched, and their own existing specs are the
// evidence that pass-through still works there.
test('each default-project worker drives its own till on 9091 + parallelIndex', async ({ baseURL, request }) => {
  const expected = `http://127.0.0.1:${9091 + test.info().parallelIndex}`;
  expect(baseURL).toBe(expected);

  // `request` inherits the same baseURL, so this proves the server behind
  // the worker's URL is actually up (a wrong-but-well-formed URL would
  // pass the string assertion above and fail here).
  const res = await request.get('/healthz');
  expect(res.status()).toBe(200);
});
