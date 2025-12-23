import { test, expect } from '../support/fixtures';

test('home page renders core UI', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle('Universal Till');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
});
