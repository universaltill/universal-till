import { test, expect } from './fixtures';

// ut-docs#2913 slice 1: a report-only Content-Security-Policy, driven on the
// `csp` project's till (run-till-csp.sh, UT_CSP_REPORT_ONLY=1). Every
// response carries the header and the browser's violation reports land in
// /csp-report, whose GET inventory is what the later enforcement slices are
// planned from — so this spec attaches it.
//
// Deliberately NO watchConsole here: a report-only violation is logged as a
// console error by design, and collecting them is the point.

const POLICY =
  "default-src 'self'; script-src 'self' 'report-sample' 'wasm-unsafe-eval'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; report-uri /csp-report";

// The main operator surfaces: the sale screen, catalogue, settings, reports,
// tables and a few admin pages.
const SURFACES = [
  '/',
  '/items',
  '/categories',
  '/inventory',
  '/settings',
  '/reports',
  '/tables',
  '/menu',
  '/admin',
  '/users',
  '/promotions',
  '/journal',
  '/help',
];

type Violation = {
  effective_directive: string;
  blocked_uri: string;
  document_path: string;
  source_file: string;
  line: number;
  sample: string;
  count: number;
};

test('every surface carries the report-only CSP and violations are collected', async ({ page, request }, testInfo) => {
  for (const path of SURFACES) {
    const resp = await page.goto(path);
    expect(resp, `no response for ${path}`).not.toBeNull();
    expect(resp!.status(), `${path} status`).toBeLessThan(400);
    // The final document's own response (after any redirect) carries it.
    expect(resp!.headers()['content-security-policy-report-only'], `${path} header`).toBe(POLICY);
    await page.waitForLoadState('networkidle');
  }

  // Non-HTML responses carry it too (the middleware sets it on everything).
  const health = await request.get('/healthz');
  expect(health.headers()['content-security-policy-report-only']).toBe(POLICY);

  // Reports are POSTed asynchronously by the browser; poll the inventory.
  let inventory: Violation[] = [];
  await expect
    .poll(
      async () => {
        const r = await request.get('/csp-report');
        expect(r.status()).toBe(200);
        const body = await r.json();
        expect(body.error).toBeNull();
        inventory = body.data as Violation[];
        return inventory.length;
      },
      { timeout: 15_000 },
    )
    .toBeGreaterThan(0);

  for (const v of inventory) {
    expect(v.effective_directive, JSON.stringify(v)).not.toBe('');
    expect(v.document_path, JSON.stringify(v)).toMatch(/^\//);
    expect(v.document_path, 'query strings are stripped').not.toContain('?');
    expect(v.count).toBeGreaterThan(0);
  }

  const byDirective: Record<string, number> = {};
  for (const v of inventory) byDirective[v.effective_directive] = (byDirective[v.effective_directive] ?? 0) + 1;
  const summary = { unique_violations: inventory.length, by_directive: byDirective };
  console.log(`csp inventory: ${JSON.stringify(summary)}`);

  await testInfo.attach('csp-inventory.json', {
    body: JSON.stringify({ summary, data: inventory }, null, 2),
    contentType: 'application/json',
  });
});
