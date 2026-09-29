import { test, expect } from './fixtures';
import { openPhoneDrawer, watchConsole } from './helpers';

// ut-docs#3091: in-app release notes, driven on the `release-notes` project's
// till (run-till-release-notes.sh): built as the newest version that has
// notes and seeded as an existing shop just updated from an older version.
//
// The two tests run in file order against ONE server and share its state on
// purpose: the first proves the chip shows (and survives a reload) until it
// is acted on, then opens the notes from it; the second proves it is gone for
// good — "shows once". The × dismiss path is covered by the Go tests
// (TestReleaseNotesSeen_ManagerOnly), since one till can only be "just
// updated" once per run.
test.describe.configure({ mode: 'serial' });

test('after an update the chip shows until opened, and Settings → About shows the notes', async ({ page }) => {
  const done = watchConsole(page);
  await page.goto('/');
  const chip = page.getByTestId('sb-release-notes');
  await expect(chip).toBeVisible();
  await expect(chip).toContainText(/Updated to v\d+\.\d+\.\d+/);

  // Not a modal: the sale screen stays usable underneath, and the chip
  // stays put across a reload until someone acts on it.
  await page.reload();
  await expect(page.getByTestId('sb-release-notes')).toBeVisible();

  // Phone width: the pill is capped and its text ellipsised, but the × must
  // stay inside the pill and on screen — clipping the whole pill once cut
  // the × off at 360px, leaving no way to dismiss (tester, 2026-09-28).
  // On the phone sale screen the status row lives at the foot of the ☰
  // drawer (ut-docs#3256), so the chip is checked there.
  const viewport = page.viewportSize()!;
  await page.setViewportSize({ width: 360, height: 740 });
  await openPhoneDrawer(page);
  await expect(page.getByTestId('sb-release-notes')).toBeVisible();
  const pill = await page.getByTestId('sb-release-notes').boundingBox();
  const x = await page.getByTestId('sb-release-notes-dismiss').boundingBox();
  expect(pill && x).toBeTruthy();
  expect(x!.x).toBeGreaterThanOrEqual(pill!.x - 1);
  expect(x!.x + x!.width).toBeLessThanOrEqual(pill!.x + pill!.width + 1);
  expect(x!.x + x!.width).toBeLessThanOrEqual(360);
  await expect(page.getByTestId('sb-release-notes-dismiss')).toHaveAccessibleName(/\S/);
  await page.keyboard.press('Escape');
  await page.setViewportSize(viewport);

  await page.getByTestId('sb-release-notes-link').click();
  await expect(page).toHaveURL(/\/settings#settings-about$/);
  const about = page.locator('#settings-about');
  await expect(about).toBeVisible();
  const running = (await about.getByTestId('about-version').innerText()).trim();
  expect(running).toMatch(/^v\d+\.\d+\.\d+$/);

  // The running version's own note is first, with real embedded content.
  const first = about.getByTestId('release-note').first();
  await expect(first).toHaveAttribute('data-version', running);
  await expect(first.locator('h5').first()).toBeVisible();
  await expect(first.locator('li').first()).not.toBeEmpty();

  // Opening the notes from the chip counts as seen.
  await expect(page.getByTestId('sb-release-notes')).toHaveCount(0);
  done();
});

test('the chip does not come back once seen', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#sb-conn')).toBeVisible();
  await expect(page.getByTestId('sb-release-notes')).toHaveCount(0);
  await page.goto('/settings#settings-about'); // two-pane Settings shows the selected card (#1960)
  await expect(page.locator('#settings-about').getByTestId('release-note').first()).toBeVisible();
  await expect(page.getByTestId('sb-release-notes')).toHaveCount(0);
});
