import { test, expect } from './fixtures';

// ut-docs#1282: web/ui/pages/inventory.html's stock-cost -> stock-cost-minor
// sync hardcoded Math.round(pounds * 100) instead of delegating to
// window.utCurrency.toMinor() -- wrong by 100x on any 0-decimal currency
// (IRR/IRT/IQD/AFN/JPY), same defect family as ut-docs#1272/#1274/#1400 on
// shifts/tips/promotions/settings.
//
// The e2e till here only ever runs GBP (2 decimals), where the buggy
// `* 100` and a correct decimals-aware conversion compute the SAME number --
// so a plain "type 12.34, expect 1234" assertion would pass against the
// unfixed code too and prove nothing. Instead this monkey-patches
// window.utCurrency.toMinor to a value that could only come from actually
// calling it (never coincide with a hardcoded *100), which fails against
// the pre-fix code and passes only once the conversion is genuinely
// delegated -- verified via TDD revert/restore against the pre-fix
// `Math.round(pounds * 100)` before this file was committed.
test.describe('inventory: stock-cost currency-decimals conversion', () => {
  test('the cost conversion is delegated to window.utCurrency.toMinor(), not a hardcoded *100', async ({ page }) => {
    await page.goto('/inventory');
    await expect(page.locator('#stock-form')).toBeVisible();

    // A marker return value no arithmetic on "12.34" could ever produce by
    // accident (1234, 12.34, 1233.9999... are all plausible near-misses;
    // 424242 is not), so reaching it proves toMinor() was actually called.
    await page.evaluate(() => {
      (window as any).utCurrency.toMinor = () => 424242;
    });
    await page.locator('#stock-cost').fill('12.34');
    await page.locator('#stock-form').evaluate((form: HTMLFormElement) =>
      form.dispatchEvent(new Event('submit', { cancelable: true })),
    );
    await expect(page.locator('#stock-cost-minor')).toHaveValue('424242');
  });

  test('a blank cost stays blank (never "0") even though toMinor("") is numerically 0 -- cost_price is optional server-side', async ({ page }) => {
    await page.goto('/inventory');
    await expect(page.locator('#stock-form')).toBeVisible();

    // Real window.utCurrency.toMinor('') returns 0 (Number('') is 0, not
    // NaN) -- calling it unconditionally on a blank field would silently
    // turn "leave cost blank" into a posted "0". The fix must check the
    // raw text for blank BEFORE calling toMinor(), not rely on toMinor()
    // itself to preserve blankness. Left as the REAL toMinor (unmocked)
    // specifically to exercise that real 0-for-blank behavior, not a
    // mocked stand-in.
    await page.locator('#stock-cost').fill('');
    await page.locator('#stock-form').evaluate((form: HTMLFormElement) =>
      form.dispatchEvent(new Event('submit', { cancelable: true })),
    );
    await expect(page.locator('#stock-cost-minor')).toHaveValue('');
  });
});
