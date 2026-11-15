import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3881: at the phone tier every labelled card cell shows its column
// header as a `::before` label in a fixed 40% column. The cell itself wraps
// with `overflow-wrap: anywhere` (data such as a long SKU must never spill
// the card), and the label inherited it, so a long one-word German header
// broke between any two letters ("GESAMTBETR" / "AG"). A label now breaks
// only at word boundaries or the page language's hyphenation points, and its
// column grows to fit a piece too long for 40%.
test.describe('phone 360px: card labels never break mid-word (ut-docs#3881)', () => {
  test.use({ hasTouch: true, isMobile: true });

  // Injects a card-mode table whose headers are the given labels and returns,
  // per label, the rendered ::before box next to a reference: the same text in
  // the same font, language and hyphenation, laid out WITHOUT break-anywhere —
  // at the label's own width (how many lines it may take) and at min-content
  // (the narrowest it can be without splitting a word).
  async function measure(page: Page, lang: string, dir: string, labels: string[]) {
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/orders');
    await page.waitForSelector('main');
    return page.evaluate(
      ({ lang, dir, labels }) => {
        const main = document.querySelector('main')!;
        const wrap = document.createElement('div');
        wrap.id = 'wrap-3881';
        wrap.lang = lang;
        wrap.dir = dir;
        const head = ['Name', ...labels].map((l) => `<th>${l}</th>`).join('');
        const cells = labels.map(() => '<td>12,50 €</td>').join('');
        wrap.innerHTML = `<table id="t3881"><thead><tr>${head}</tr></thead><tbody>` +
          `<tr id="r1"><td>Kaffee</td>${cells}</tr></tbody></table>`;
        main.appendChild(wrap);
        (window as any).utTableCards(wrap);
        const row = document.querySelector('#t3881 tr#r1')!;
        const cardWidth = (row as HTMLElement).getBoundingClientRect().width;
        return labels.map((label, i) => {
          const td = row.children[i + 1] as HTMLElement;
          const cs = getComputedStyle(td, '::before');
          const width = parseFloat(cs.width);
          const probe = (inlineSize: string) => {
            const el = document.createElement('span');
            el.textContent = label;
            for (const p of ['font-family', 'font-size', 'font-weight', 'letter-spacing',
              'text-transform', 'line-height', 'hyphens']) {
              el.style.setProperty(p, cs.getPropertyValue(p));
            }
            el.style.display = 'inline-block';
            el.style.position = 'absolute';
            el.style.overflowWrap = 'normal';
            el.style.wordBreak = 'normal';
            el.style.inlineSize = inlineSize;
            td.appendChild(el);
            const r = el.getBoundingClientRect();
            el.remove();
            return r;
          };
          return {
            label,
            dataLabel: td.dataset.label,
            width,
            height: parseFloat(cs.height),
            lineHeight: parseFloat(cs.lineHeight),
            refHeight: probe(`${width}px`).height,
            minContent: probe('min-content').width,
            cardWidth,
          };
        });
      },
      { lang, dir, labels },
    );
  }

  // The label takes exactly the lines it would with word/hyphen breaks only,
  // and its column is never narrower than its longest unbreakable piece.
  function expectNoMidWordBreak(m: Awaited<ReturnType<typeof measure>>[number]) {
    const ctx = JSON.stringify(m);
    expect(Math.abs(m.height - m.refHeight), ctx).toBeLessThan(1);
    expect(m.width + 0.5, ctx).toBeGreaterThanOrEqual(m.minContent);
    expect(m.width, ctx).toBeLessThanOrEqual(m.cardWidth);
  }

  test('German: a long one-word label hyphenates or stays whole; a multi-word one wraps at spaces', async ({ page }) => {
    const assertClean = watchConsole(page);
    const [single, multi, short] = await measure(page, 'de', 'ltr', [
      'Mehrwertsteuerbetrag',
      'Zahlungsart der Bestellung heute',
      'Preis',
    ]);

    expect(single.dataLabel).toBe('Mehrwertsteuerbetrag');
    expectNoMidWordBreak(single);
    expectNoMidWordBreak(multi);
    expectNoMidWordBreak(short);

    // A label with spaces keeps the 40% column and wraps between words.
    expect(multi.height, JSON.stringify(multi)).toBeGreaterThan(multi.lineHeight * 1.5);
    expect(multi.width).toBeLessThan(multi.cardWidth * 0.45);

    // A short label is untouched: the usual 40% column, one line.
    expect(short.height).toBeLessThan(short.lineHeight * 1.5);
    expect(short.width).toBeLessThan(short.cardWidth * 0.45);

    assertClean();
  });

  test('Persian (RTL): a long one-word label stays whole', async ({ page }) => {
    const assertClean = watchConsole(page);
    // "مجموع‌پرداخت‌شده" is one word: its parts join with ZWNJ, not spaces.
    const [single] = await measure(page, 'fa', 'rtl', ['مجموع‌پرداخت‌شده‌نهایی‌مشتری']);
    expectNoMidWordBreak(single);
    // No Persian hyphenation: the column grows and the word sits on one line.
    expect(single.height, JSON.stringify(single)).toBeLessThan(single.lineHeight * 1.5);
    assertClean();
  });
});
