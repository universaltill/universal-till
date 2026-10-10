import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#3518: on /users a super-admin viewer's row actions (PIN, active
// toggle, Promote, role + Change role) are `.users-inline` forms that never
// wrap, so at 1024x600 (and 800 in tr) the widest form pushed past the
// `#users-list` card and was clipped by the table's overflow-x:auto; a long
// username made it worse by taking the actions column's width.
// Default project (UT_AUTH=off): the viewer sees Promote, i.e. the
// super-admin view.

const RUN = Date.now().toString(36).toUpperCase();
// Long and unbreakable on purpose: before the fix such a name took the
// actions column's width (table auto layout sizes by min-content). Every
// row is checked, so other specs' users on a shared worker DB count too.
const USERNAME = `fitcheckunbreakableusername3518${RUN}`;
const SHORT_USERNAME = `adm3518${RUN}`;

// Once per worker DB: POST /api/users answers a refusal (duplicate,
// role) with 200 too, so the X-UT-Response header is the real signal.
async function createUser(page: Page, username: string, displayName: string): Promise<void> {
  await page.goto('/users');
  if (await page.locator('#users-list tr', { hasText: username }).count()) return;
  const resp = await page.request.post('/api/users', {
    form: { username, display_name: displayName, role: 'cashier' },
  });
  expect(resp.headers()['x-ut-response'], `create user ${username}`).toBe('ok');
}

const LOCALES = ['en', 'tr', 'fa'];
const VIEWPORTS = [
  { width: 1024, height: 600 },
  { width: 800, height: 600 },
  { width: 1101, height: 600 },
  // first width above the wrap breakpoint (<1280px): forms are nowrap again
  { width: 1280, height: 600 },
];

test.describe('Users row actions stay inside the card (ut-docs#3518)', () => {
  test.beforeEach(async ({ page }) => {
    await createUser(page, USERNAME, 'Fitcheckwithaverylongdisplayname3518');
    await createUser(page, SHORT_USERNAME, 'Administrator');
  });

  for (const loc of LOCALES) {
    for (const vp of VIEWPORTS) {
      test(`${loc} at ${vp.width}x${vp.height}`, async ({ page }) => {
        await page.setViewportSize(vp);
        await page.goto(`/users?lang=${loc}`);
        const table = page.locator('#users-list .table');
        await expect(table).toBeVisible();
        await expect(page.locator('#users-list tr', { hasText: USERNAME })).toBeVisible();

        const result = await page.evaluate(() => {
          const t = document.querySelector('#users-list .table') as HTMLElement;
          const tb = t.getBoundingClientRect();
          const offenders: string[] = [];
          document
            .querySelectorAll<HTMLElement>('#users-list .users-actions :is(button, input, select)')
            .forEach((el) => {
              if (el instanceof HTMLInputElement && el.type === 'hidden') return;
              const r = el.getBoundingClientRect();
              if (r.width === 0 || r.height === 0) return;
              if (r.left < tb.left - 1 || r.right > tb.right + 1) {
                const label = (el.textContent || (el as HTMLInputElement).name || el.tagName).trim();
                offenders.push(`${el.tagName.toLowerCase()} "${label}" [${Math.round(r.left)}..${Math.round(r.right)}] vs table [${Math.round(tb.left)}..${Math.round(tb.right)}]`);
              }
            });
          return { offenders, scrollWidth: t.scrollWidth, clientWidth: t.clientWidth };
        });

        expect(result.offenders, `controls outside the table box:\n${result.offenders.join('\n')}`).toEqual([]);
        expect(
          result.scrollWidth,
          `table has hidden sideways overflow (scrollWidth ${result.scrollWidth} > clientWidth ${result.clientWidth})`,
        ).toBeLessThanOrEqual(result.clientWidth + 1);
      });
    }
  }

  // The long-name fix must not split ordinary words: "Administrator" stays
  // on one line at 1024 (an earlier 5rem floor broke it as "Administra|tor").
  test('a short display name is not split mid-word at 1024x600 (en)', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/users?lang=en');
    const cell = page.locator('#users-list tr', { hasText: SHORT_USERNAME }).locator('td').first();
    const lines = await cell.evaluate((td) => {
      const text = [...td.childNodes].find((n) => n.nodeType === Node.TEXT_NODE && n.textContent!.includes('Administrator'))!;
      const r = document.createRange();
      const start = text.textContent!.indexOf('Administrator');
      r.setStart(text, start);
      r.setEnd(text, start + 'Administrator'.length);
      return new Set([...r.getClientRects()].map((b) => Math.round(b.top))).size;
    });
    expect(lines, '"Administrator" renders on one line').toBe(1);
  });

  // Guard against ut-docs#898's revert: the role select and its "Change
  // role" button stay on one line at desktop width, even next to a long
  // username (the wrap stops below 1280px).
  test('role select and Change role share a line at 1280x800 (en)', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/users?lang=en');
    const row = page.locator('#users-list tr', { hasText: USERNAME });
    const select = row.locator('select.users-role-pick');
    const button = select.locator('xpath=ancestor::form[1]').locator('button[type=submit]');
    const s = await select.boundingBox();
    const b = await button.boundingBox();
    expect(s, 'role select box').not.toBeNull();
    expect(b, 'Change role button box').not.toBeNull();
    expect(Math.abs(s!.y - b!.y), 'select and button tops differ').toBeLessThan(10);
  });
});
