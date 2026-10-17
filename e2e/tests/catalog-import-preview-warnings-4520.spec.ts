import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4520: the Preview an operator studies before committing must
// already flag a row whose tax rate, net quantity, barcode or photo will be
// dropped — the same warned (⚠ row-warn) treatment the post-commit summary
// gives it, not a plain "ok". Preview only: nothing is written, so there
// is no catalog cleanup to do. Driven in Turkish to prove the warning text
// is the translated commit-path text, and at the kiosk floor and phone
// width so the longer warned status doesn't break the table.
for (const vp of [
  { name: 'kiosk', width: 1024, height: 600 },
  { name: 'phone', width: 360, height: 800 },
]) {
  test(`preview warns a parse-time issue before commit (${vp.name})`, async ({ page }, testInfo) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await page.goto('/import?lang=tr');
    await expect(page.locator('input[type=file]')).toBeVisible();

    const csv =
      'Name,SKU,Price,Tax rate,Net quantity,Net quantity unit\n' +
      'Preview Oddball 4520,PW4520A,1.00,abc,,\n' +
      'Preview Flour 4520,PW4520B,1.20,19,1500,kg\n' +
      'Preview Plain 4520,PW4520C,2.00,19,,\n';
    await page.setInputFiles('input[type=file]', {
      name: 'import-4520.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/import')),
      page.getByRole('button', { name: 'Önizleme', exact: true }).click(),
    ]);

    const result = page.locator('#import-result');
    await expect(result).toContainText('Preview Oddball 4520');

    const taxRow = page.locator('tr.row-warn', { hasText: 'Preview Oddball 4520' });
    await expect(taxRow).toBeVisible();
    const nqRow = page.locator('tr.row-warn', { hasText: 'Preview Flour 4520' });
    if (vp.width < 600) {
      // Phone card layout: the status cell sits behind each card's "More"
      // toggle (the shared table pattern), so open it before reading it.
      for (const row of [taxRow, nqRow]) {
        await row.getByRole('button', { name: 'Daha fazla' }).click();
      }
    }
    await expect(taxRow.locator('.row-warn-icon')).toBeVisible();
    await expect(taxRow).toContainText('yüzde olarak okunamadı'); // tax_unparseable, translated

    await expect(nqRow).toBeVisible();
    await expect(nqRow).toContainText('1500 kg');

    const plainRow = page.locator('tr', { hasText: 'Preview Plain 4520' });
    await expect(plainRow).toBeVisible();
    await expect(plainRow).not.toHaveClass(/row-warn/);
    const [warnedBg, cleanBg] = await Promise.all([
      taxRow.locator('td').first().evaluate((el) => getComputedStyle(el).backgroundColor),
      plainRow.locator('td').first().evaluate((el) => getComputedStyle(el).backgroundColor),
    ]);
    expect(warnedBg).not.toBe(cleanBg);

    // The warned status wraps inside the page rather than pushing it
    // sideways.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);

    await page.screenshot({ path: testInfo.outputPath(`preview-4520-${vp.name}.png`), fullPage: true });
    assertClean();
  });
}
