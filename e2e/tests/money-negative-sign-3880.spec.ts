import { test, expect } from './fixtures';

// ut-docs#3880: a negative amount leads with its sign ("-£42.50", never
// "£-42.50"), and on an RTL page a prefix-symbol negative is a left-to-
// right isolate so the sign is drawn left of the symbol. These read glyph
// positions from the real layout engine: a string comparison alone can't
// prove the bidi algorithm draws it in that order.

// x of the first occurrence of ch inside el's text, from the real layout.
async function glyphX(page, sel: string, ch: string): Promise<number> {
  return page.locator(sel).evaluate((el: HTMLElement, c: string) => {
    const node = el.firstChild as Text;
    const i = node.data.indexOf(c);
    const r = document.createRange();
    r.setStart(node, i);
    r.setEnd(node, i + 1);
    return r.getBoundingClientRect().left;
  }, ch);
}

test.describe('Negative money sign (ut-docs#3880)', () => {
  test('utCurrency.format puts the sign before the symbol', async ({ page }) => {
    await page.goto('/catalog');
    const got = await page.evaluate(() => {
      const c = (window as any).utCurrency;
      return { sym: c.display, suffix: c.suffix, neg: c.format(-4250), pos: c.format(4250) };
    });
    // The sign leads whatever the till's currency: "-" + the positive form.
    expect(got.neg).toBe('-' + got.pos);
    if (!got.suffix) expect(got.neg.startsWith('-' + got.sym)).toBe(true);
  });

  test('on an RTL page the sign is drawn left of the symbol', async ({ page }) => {
    await page.goto('/catalog?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    // The e2e till runs GBP (a prefix symbol), the case the isolate is for.
    const formatted = await page.evaluate(() => (window as any).utCurrency.format(-4250));
    expect(formatted).toBe('⁦-£42.50⁩');
    // Go's FormatMoneyDisplay output for the same amount under fa (Persian
    // digits), plus the unwrapped string to prove the isolate is what fixes
    // the order.
    await page.evaluate((s: string) => {
      for (const [id, text] of [['m-js', s], ['m-go', '⁦-£۴۲٫۵۰⁩'], ['m-bare', '-£۴۲٫۵۰']]) {
        const p = document.createElement('p');
        p.id = id;
        p.textContent = text;
        document.body.appendChild(p);
      }
    }, formatted);
    for (const id of ['#m-js', '#m-go']) {
      expect(await glyphX(page, id, '-')).toBeLessThan(await glyphX(page, id, '£'));
    }
    // Control: without the isolate the RTL paragraph draws the sign right
    // of the symbol -- the bug this card fixed.
    expect(await glyphX(page, '#m-bare', '-')).toBeGreaterThan(await glyphX(page, '#m-bare', '£'));
  });
});
