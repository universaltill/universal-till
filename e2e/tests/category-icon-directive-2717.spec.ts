import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { execFileSync } from 'child_process';
import { watchConsole } from './helpers';
import { REPO_ROOT } from './worker-till';

// ut-docs#2717: the pilot's report. my. changed a category's icon, the till
// logged `save_category applied`, and the sale screen kept drawing the old
// picture — the library tile the till's own editor had stored as an
// image_path (#2500) hid the new icon. Pinned here in a real browser
// against a real, still-running till (never restarted):
//   (1) a category showing a library tile stored the old way (a path);
//   (2) a save_category {icon} lands in its database out of band — the
//       repository write the cloudsync hook performs (e2e/category_picture;
//       the e2e till has no cloud and a 2-minute sync tick);
//   (3) the open sale screen's next load draws the NEW icon, and the
//       artwork actually loads (not the fallback tag, not a broken image).
//
// HONESTY NOTE: Chromium; the pilot tablet's WebView is the hardware
// lane's to confirm.

const CAT = 'cat_drink'; // demo catalogue "Drinks"
const BEER = '/public/assets/category-icons/beer.svg';
const EGG = '/public/assets/category-icons/breakfast.svg'; // lucide:egg-fried

function categoryPicture(mode: 'directive' | 'legacy-path', value: string) {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  execFileSync('go', ['run', './e2e/category_picture', mode, CAT, value], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'ignore', 'inherit'],
  });
}

async function tabImage(page: Page) {
  const img = page.locator(`#cat-tab-${CAT} img.tab-cat-img`);
  await expect(img).toHaveCount(1);
  return img;
}

test.describe('category icon from my. replaces a library tile on the sale screen (ut-docs#2717)', () => {
  test.afterEach(() => {
    // Hand the worker till back with no picture on Drinks.
    if (process.env.UT_E2E_WORKER_DATA_DIR) categoryPicture('legacy-path', '');
  });

  test('a save_category icon shows on the next load, without a restart', async ({ page }) => {
    const assertClean = watchConsole(page);
    // Checked here, not at describe level: the variable is set by the
    // worker fixture, which runs after the file is collected.
    test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir); a reused server has none');
    await page.setViewportSize({ width: 1024, height: 600 });

    categoryPicture('legacy-path', BEER);
    await page.goto('/');
    await expect(await tabImage(page)).toHaveAttribute('src', new RegExp(BEER.replace(/[.]/g, '\\.')));

    categoryPicture('directive', 'lucide:egg-fried');
    await page.reload();
    const img = await tabImage(page);
    await expect(img).toHaveAttribute('src', new RegExp(EGG.replace(/[.]/g, '\\.')));
    await expect
      .poll(() => img.evaluate((el) => (el as HTMLImageElement).complete && (el as HTMLImageElement).naturalWidth > 0))
      .toBe(true);

    // An icon only my.'s picker offered before #2717 (lucide:leaf) draws
    // its own artwork, not the fallback tag.
    categoryPicture('directive', 'lucide:leaf');
    await page.reload();
    await expect(await tabImage(page)).toHaveAttribute('src', /category-icons\/leaf\.svg/);
    assertClean();
  });
});

// ut-docs#3585: the tile case above (an icon directive replacing a legacy
// library tile in image_path) is NOT the only shape a save_category
// directive can hit now that a category's photo and icon are independent
// columns — a category can hold a REAL uploaded photo and an icon at the
// same time, image wins. Pinned here, same real-browser/real-till pattern
// as the tile case: a save_category {icon} landing under an uploaded photo
// must leave the sale screen showing the photo, not fall back to the icon
// underneath it.
test.describe('category photo survives an icon directive on the sale screen (ut-docs#3585)', () => {
  test.afterEach(() => {
    // Hand the worker till back with no picture on Drinks.
    if (process.env.UT_E2E_WORKER_DATA_DIR) categoryPicture('legacy-path', '');
  });

  test('a save_category icon directive under an uploaded photo keeps the photo showing', async ({ page }) => {
    const assertClean = watchConsole(page);
    test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir); a reused server has none');
    await page.setViewportSize({ width: 1024, height: 600 });

    // A real uploaded photo (not a library tile) with no icon yet.
    categoryPicture('photo', '');
    await page.goto('/');
    const photoSrc = new RegExp(`/public/assets/categories/${CAT}/thumb\\.png`);
    const img = await tabImage(page);
    await expect(img).toHaveAttribute('src', photoSrc);
    await expect
      .poll(() => img.evaluate((el) => (el as HTMLImageElement).complete && (el as HTMLImageElement).naturalWidth > 0))
      .toBe(true);

    // my. sets an icon under that photo (save_category {icon}) — before
    // ut-docs#3585 this silently wiped image_path and the tile would show
    // the icon instead; now both columns are kept and the photo still wins
    // (iconid.Resolve).
    categoryPicture('directive', 'lucide:egg-fried');
    await page.reload();
    await expect(await tabImage(page)).toHaveAttribute('src', photoSrc);
    assertClean();
  });
});
