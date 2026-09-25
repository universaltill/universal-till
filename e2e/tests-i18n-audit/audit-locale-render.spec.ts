import { test, expect, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

// scripts/ci/audit-locale-render.sh (ut-docs#2300): renders every help-topic
// page route (reusing tests-docs/lib.js's routedTopics(), the same
// CI-guaranteed-complete route source the docs-shots screenshot harness
// uses) in German (?lang=de) against a till whose I18n has the REAL
// ut-plugin-language-de pack loaded as a test-only overlay
// (UT_TEST_I18N_OVERLAY_DIR — see internal/pages/init.go and
// loadTestI18nOverlays), and fails if any rendered, visible line of text is
// an EXACT match for a value that exists in the base web/locales/en.json
// catalog. That directly catches two real bug classes:
//   - a key present in en.json but MISSING (or not yet translated) from
//     de.json — T() falls back to the literal English string.
//   - a hardcoded English literal in Go template data or an inline <script>
//     that happens to equal a real catalog string (a template-data map
//     literal like `"title": "Some English"` with no i18n key at all —
//     exactly the class of bug ut-docs#2297 fixed).
//
// NON-GOAL, explicitly: this is NOT a free-form English-word detector. A
// hardcoded literal that is NOT also a value anywhere in en.json (e.g. a
// brand-new string nobody ever added to the catalog) will not be flagged —
// that is a real, different, more expensive problem (would need actual
// language detection), and is left as a documented gap, not silently
// claimed as solved.
//
// Also out of scope, matching this repo's own accepted gaps:
//   - any locale other than German (`de`) — other packs are separate cards.
//   - a topic's secondary routes (routes[1:]) — same accepted gap as the
//     docs-shots harness itself (ut-docs#900); only routes[0] is checked.
const { routedTopics } = require('../tests-docs/lib');
// PR-only carve-out for keys that are NEW in the PR (ut-docs#2805) — the
// rule and the reason live in lib/new-keys.js, unit-tested by
// lib/new-keys_test.js.
const { partitionFindings } = require('./lib/new-keys');

// Same special case as tests-docs/docs-shots.spec.ts: GET /users requires a
// real manager session (UT_AUTH=off has no operator in the request
// context), so it 403s on the default till. Captured against the AUTH
// server (8092) instead, after the same wizard/PIN-login flow
// login.spec.ts and docs-shots.spec.ts's own ensureOperator() drive.
const AUTH_BASE = 'http://127.0.0.1:8092';
const ADMIN_PIN = '482913'; // same PIN docs-shots.spec.ts/login.spec.ts set

const AUTH_TILL_TOPIC_IDS = ['users'];

// The base locale's own string VALUES (trimmed, deduped) — the deterministic
// "known English text" signal. Loaded once, at module scope: every test in
// this file checks rendered text against the exact same catalog snapshot.
function loadEnglishCatalog(): Record<string, string> {
  const enPath = path.join(__dirname, '..', '..', 'web', 'locales', 'en.json');
  return JSON.parse(fs.readFileSync(enPath, 'utf8')) as Record<string, string>;
}

function loadEnglishCatalogValues(en: Record<string, string>): Set<string> {
  const values = new Set<string>();
  for (const v of Object.values(en)) {
    const trimmed = v.trim();
    if (trimmed) values.add(trimmed);
  }
  return values;
}

// Exact-match allow-list: brand names, codes, established loanwords — a
// reviewed human decision each, same convention as
// audit-nav-i18n-parity.sh's own ALLOWLIST.
function loadAllowlist(): Set<string> {
  const allowlistPath = path.join(__dirname, 'allowlist.json');
  const entries = JSON.parse(fs.readFileSync(allowlistPath, 'utf8')) as { text: string; reason: string }[];
  return new Set(entries.map((e) => e.text));
}

// The base BRANCH's en.json (ut-docs#2805) — set by
// locale-render-audit.yml on pull_request runs only (AUDIT_BASE_EN_JSON),
// so findings for keys this PR adds can be deferred until the de pack PR
// that follows the merge. Unset/empty (push to main, workflow_dispatch, a
// local run) => null => strict, nothing deferred. Set but unreadable is a
// workflow bug, so it throws rather than quietly going strict.
function loadBaseEnglishCatalog(): Record<string, string> | null {
  const basePath = process.env.AUDIT_BASE_EN_JSON;
  if (!basePath) return null;
  return JSON.parse(fs.readFileSync(basePath, 'utf8')) as Record<string, string>;
}

const EN_CATALOG = loadEnglishCatalog();
const BASE_EN_CATALOG = loadBaseEnglishCatalog();
const ENGLISH_VALUES = loadEnglishCatalogValues(EN_CATALOG);
const ALLOWLIST = loadAllowlist();

// A line with no letters at all (pure numbers, currency, punctuation,
// whitespace) can never be "English text" in any meaningful sense — skip it
// before checking the catalog/allow-list, same reasoning
// audit-nav-i18n-parity.sh's key/value diff doesn't need but a rendered-DOM
// text diff does.
function isPunctuationOrNumericOnly(line: string): boolean {
  return !/[A-Za-z]/.test(line);
}

type Flag = { topicId: string; route: string; text: string };

// Navigates to route (?lang=de) on whatever baseURL the page's context is
// already using, settles the page the same way tests-docs/docs-shots.spec.ts's
// own capture() does (networkidle + the /orders SSE-stream special case +
// the htmx-settle wait), and returns every flagged line found on it.
async function auditRoute(page: Page, topicId: string, route: string): Promise<Flag[]> {
  const sep = route.includes('?') ? '&' : '?';
  const url = `${route}${sep}lang=de`;

  // ADR-0079 (ut-docs#1571) / mirrors tests-docs/docs-shots.spec.ts's own
  // capture(): GET /orders opens a live SSE stream (GET /api/orders/stream)
  // that never completes, so goto's `networkidle` would hang forever on
  // that one route. Answer it with a non-200, non-5xx status so the browser
  // treats the connection as closed (no reconnect-on-timer), not errored.
  await page.route('**/api/orders/stream', (r) => r.fulfill({ status: 204 }));
  await page.goto(url, { waitUntil: 'networkidle' });

  // Several topics lazy-load their body after first paint via an
  // hx-trigger="load" fragment (e.g. /translations' key table) — wait for
  // any in-flight htmx request/swap/settle to finish, same wait
  // docs-shots.spec.ts's capture() uses, so text extraction doesn't race a
  // mid-swap DOM.
  await page
    .waitForFunction(
      () => !document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling'),
      null,
      { timeout: 10_000 },
    )
    .catch(() => {}); // no htmx on the page at all -> nothing to wait for

  const bodyText = await page.evaluate(() => document.body.innerText);
  const lines = new Set(
    bodyText
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean),
  );

  const flags: Flag[] = [];
  for (const line of lines) {
    if (isPunctuationOrNumericOnly(line)) continue;
    if (ALLOWLIST.has(line)) continue;
    if (ENGLISH_VALUES.has(line)) flags.push({ topicId, route, text: line });
  }
  return flags;
}

// The auth till (8092) is a genuinely fresh install: complete the
// first-boot wizard, or PIN-login if the server was reused from a prior
// local run. Mirrors tests-docs/docs-shots.spec.ts's own ensureOperator()
// exactly (same steps, same PIN) — this harness needs the identical
// wizard walk to reach a real manager session for /users.
async function ensureOperator(page: Page) {
  await page.goto('/');
  if (page.url().includes('/setup')) {
    const step = (n: number) => page.locator(`[data-step="${n}"]`);
    await step(1).locator('.setup-nav button', { hasText: 'Next' }).click(); // language
    await step(2).locator('h1').waitFor();
    const showAllBtn = step(2).locator('button', { hasText: 'Show all countries' });
    if (await showAllBtn.isVisible()) await showAllBtn.click();
    await step(2).locator('button.picker-tile[value="GB"]').click();
    await step(2).locator('.setup-nav button', { hasText: 'Next' }).click(); // country
    await page.locator('input[name=store_name]').fill('Demo Shop');
    await step(4).locator('.setup-nav button', { hasText: 'Next' }).click(); // shop name (GB skips step 3)
    await step(5).locator('.setup-nav button', { hasText: 'Next' }).click(); // shop type + demo data
    await step(6).locator('.setup-nav button.primary', { hasText: 'No' }).click(); // restore from another POS? No
    await step(7).locator('input[name=pin]').fill(ADMIN_PIN);
    await step(7).locator('input[name=pin_confirm]').fill(ADMIN_PIN);
    await step(7).locator('.setup-nav button', { hasText: 'Next' }).click(); // PIN
    await Promise.all([
      page.waitForURL((u) => !u.pathname.includes('/setup')),
      step(8).locator('button[type=submit]', { hasText: 'Start selling' }).click(),
    ]);
  } else if (page.url().includes('/login')) {
    for (const d of ADMIN_PIN.split('')) {
      await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
    }
    await page.locator('button[type=submit].pin-key').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));
  }
  await expect(page.locator('#basket')).toBeVisible();
}

function formatReport(flags: Flag[]): string {
  const lines = [`Found ${flags.length} English string(s) rendered on a German (?lang=de) page:`];
  for (const f of flags) {
    lines.push(`  [${f.topicId}] ${f.route} -> "${f.text}"`);
  }
  lines.push(
    '',
    'Each is a value that exists verbatim in web/locales/en.json — either de.json is',
    'missing/behind on that key (T() fell back to English), or a hardcoded English',
    'literal happens to match a real catalog string. Fix the translation/hardcode, or',
    'if this is a genuine loanword/brand name, add it to e2e/tests-i18n-audit/allowlist.json',
    'with a one-line reason (reviewed human decision, same convention as',
    'scripts/audit-nav-i18n-parity.sh\'s own ALLOWLIST).',
  );
  return lines.join('\n');
}

// Splits a test's findings, prints the deferred new-key ones as a GitHub
// ::notice:: (still visible on the PR, never red), and returns only the
// ones that must fail.
function failuresAfterNewKeyCarveOut(flags: Flag[]): Flag[] {
  const { failures, deferred } = partitionFindings(flags, EN_CATALOG, BASE_EN_CATALOG) as {
    failures: Flag[];
    deferred: Flag[];
  };
  if (deferred.length > 0) {
    const detail = deferred.map((f) => `[${f.topicId}] ${f.route} -> "${f.text}"`).join('; ');
    console.log(
      `::notice file=web/locales/en.json::${deferred.length} new key(s) not yet in the de pack — ` +
        `the pack PR follows after merge (ut-docs#1857). ${detail}`,
    );
  }
  return failures;
}

const topics = routedTopics() as { id: string; route: string }[];

test('German (de) render audit: no page shows a known English catalog string', async ({ page, baseURL }) => {
  test.setTimeout(topics.length * 15_000 + 30_000);
  const allFlags: Flag[] = [];

  for (const topic of topics.filter((t) => !AUTH_TILL_TOPIC_IDS.includes(t.id))) {
    await test.step(`${topic.id} (${topic.route})`, async () => {
      const flags = await auditRoute(page, topic.id, topic.route);
      allFlags.push(...flags);
    });
  }

  const failures = failuresAfterNewKeyCarveOut(allFlags);
  expect(failures, formatReport(failures)).toEqual([]);
});

test.describe('manager-gated topics (auth till)', () => {
  test.use({ baseURL: AUTH_BASE });
  for (const id of AUTH_TILL_TOPIC_IDS) {
    const topic = topics.find((t) => t.id === id);
    test(`German (de) render audit: ${id}`, async ({ page }) => {
      test.skip(!topic, `${id} topic no longer declares routes`);
      await ensureOperator(page); // fresh Playwright context per test -> log in each time
      const flags = failuresAfterNewKeyCarveOut(await auditRoute(page, topic!.id, topic!.route));
      expect(flags, formatReport(flags)).toEqual([]);
    });
  }
});
