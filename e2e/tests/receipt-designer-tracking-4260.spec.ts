import { test, expect } from './fixtures';

// ut-docs#4260: order tracking is cloud-only, and the cloud tracking page
// does not exist yet (#4238). The receipt designer's "Order tracking QR"
// box is shown disabled with a note saying why, and the live preview
// carries no tracking block — at the 1024x600 kiosk floor, in English and
// in an RTL locale.

for (const locale of ['en', 'fa']) {
  test(`receipt designer: tracking QR box disabled with a note (${locale})`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto(`/receipt-designer?lang=${locale}`);
    const box = page.locator('input[name="show_tracking_qr"]');
    await expect(box).toBeDisabled();
    const note = page.locator('#rd-tracking-note');
    await expect(note).toBeVisible();
    await expect(box).toHaveAttribute('aria-describedby', 'rd-tracking-note');
    // The note sits under the box, inside the viewport's width.
    const nb = await note.boundingBox();
    expect(nb).not.toBeNull();
    expect(nb!.x).toBeGreaterThanOrEqual(0);
    expect(nb!.x + nb!.width).toBeLessThanOrEqual(1024);
    // No sample tracking link in the preview.
    await expect(page.locator('body')).not.toContainText('example.invalid/o/sample');
    await page.screenshot({ path: testInfo.outputPath(`designer-4260-${locale}.png`), fullPage: true });
  });
}
