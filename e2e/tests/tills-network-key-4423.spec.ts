import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4423 (ADR-0114 §7): the main till's Tills page shows its LAN
// TLS network key and a manager "Change key" that starts a rotation now.
// After the press the roster re-renders as "change in progress" and the
// button is gone (a second press would only keep the same next key).

for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 800 }]) {
  test(`tills: network key line fits at ${vp.width}x${vp.height}`, async ({ page }) => {
    await page.setViewportSize(vp);
    await page.goto('/tills');
    const block = page.getByTestId('tills-network-key');
    await expect(block).toHaveCount(1);
    const box = await block.boundingBox();
    expect(box, 'network key block not laid out').not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(vp.width + 0.5);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, 'page scrolls sideways').toBeLessThanOrEqual(0);
  });
}

test('tills: Change key starts a rotation and the line reads in progress', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/tills');
  const block = page.getByTestId('tills-network-key');
  const btn = block.getByRole('button', { name: 'Change key' });
  // The e2e till is shared across specs: a run on a reused till may find
  // the rotation already started by an earlier run.
  if ((await block.getAttribute('data-rotation')) === 'idle') {
    const box = await btn.boundingBox();
    expect(box!.height, 'touch target').toBeGreaterThanOrEqual(44);
    page.once('dialog', (d) => {
      expect(d.message()).toContain('network security key');
      void d.accept();
    });
    await btn.click();
  }
  await expect(page.getByTestId('tills-network-key')).toHaveAttribute('data-rotation', 'pending');
  await expect(page.getByTestId('tills-network-key').getByRole('button')).toHaveCount(0);
  await expect(page.getByTestId('tills-network-key')).toContainText('Key change in progress');
  assertClean();
});
