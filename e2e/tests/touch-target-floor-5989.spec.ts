import { test, expect } from './fixtures';
import * as fs from 'fs';
import * as path from 'path';
import { TILL_GET_ROUTES } from './helpers';

// ut-docs#5989: global touch-target floor for the till.
//
// Rule: every visible interactive element is at least 44x44 CSS px in BOTH
// dimensions (border box; it fails below 43.5, the 0.5px tolerance of
// settings-checkbox-touch-row-5738). New UI must meet it; the reviewed
// exception is data-touch-ok="<reason>" on the element (or an ancestor).
// An empty reason does not exempt. Inline links inside prose are exempt
// (WCAG 2.5.8).
//
// scripts/ci/touch-target-baseline.txt lists the misses that predate this
// spec. It is pre-existing-only and SHRINK-ONLY: a violation not listed
// fails, and so does a listed entry that no longer violates (delete the
// line). Regenerate locally, never in CI:
//   cd e2e && TOUCH_BASELINE_WRITE=1 npx playwright test touch-target-floor-5989 --project=default --workers=1
//
// Out of scope: dialogs/overlays/OSK/tour states, auth-session chips (need a
// PIN session), cloud surfaces (my./admin./partner.), WebKit, RTL.
// The file name must not contain "phone": playwright.webkit.config.ts pulls
// every /phone/ spec into the WebKit lane.

const VIEWPORTS = {
  phone: { width: 440, height: 956 },
  till: { width: 1024, height: 600 },
} as const;
type VP = keyof typeof VIEWPORTS;

const ROUTES = [...TILL_GET_ROUTES, '/self-order'];
// Routes whose scan root is the whole body (shell chrome: nav rail, status
// bar). Elsewhere only <main> is scanned, so a chrome miss is reported once.
const CHROME_ROUTES = new Set(['/', '/admin']);
// Read-only pages whose <main> has no interactive element by design (the
// reports list is a plain table, the order board an empty status card). They prove they rendered with their <h1>
// instead of a measured-element count.
const NO_CONTROLS_ROUTES = new Set(['/my-reports', '/orders', '/report-issue']);

const BASELINE = path.resolve(__dirname, '../../scripts/ci/touch-target-baseline.txt');
const WRITE = process.env.TOUCH_BASELINE_WRITE === '1';
if (WRITE && process.env.CI) {
  throw new Error('TOUCH_BASELINE_WRITE is a local regeneration tool; refusing under CI');
}

type Violation = { sig: string; w: number; h: number };
type ScanResult = { measured: number; violations: Violation[] };

// Runs in the page (serialised by page.evaluate), so it must be
// self-contained.
function scan(opts: { chrome: boolean }): ScanResult {
  const root: Element = opts.chrome ? document.body : (document.querySelector('main') || document.body);
  const NATIVE = 'a[href], button, summary, select, textarea, input:not([type=hidden]), [role=button], [role=tab], [role=menuitem], [role=switch], [role=checkbox], [role=radio], [role=link]';
  const NO_CLICK_TAGS = /^(form|input|select|textarea)$/;
  const isCandidate = (el: Element): boolean => {
    if (el.matches(NATIVE)) return true;
    if (/^(label|option|area)$/i.test(el.tagName)) return false;
    if (el.hasAttribute('onclick') || el.hasAttribute('@click') || el.hasAttribute('x-on:click')) return true;
    if (NO_CLICK_TAGS.test(el.tagName.toLowerCase())) return false;
    for (const a of ['hx-get', 'hx-post', 'hx-put', 'hx-patch', 'hx-delete']) {
      if (el.hasAttribute(a)) {
        const t = el.getAttribute('hx-trigger');
        if (t === null || t.includes('click')) return true;
      }
    }
    return false;
  };
  const visible = (r: DOMRect) => r.width > 0 && r.height > 0;
  const isVisible = (el: Element): boolean => {
    if (el.closest('[hidden], [aria-hidden="true"], dialog:not([open]), #osk[hidden], template')) return false;
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
    if (el.getClientRects().length === 0) return false;
    return true;
  };
  // getAttribute, not .id: a <form> with an <input name=id> answers .id with that input.
  const stableId = (e: Element) => { const id = e.getAttribute('id') || ''; return id && !/\d{2,}|[0-9a-f]{8}/i.test(id) ? id : ''; };
  const own = (e: Element): string => {
    const tag = e.tagName.toLowerCase();
    const id = stableId(e);
    if (id) return `${tag}#${id}`;
    const classes = Array.from(e.classList)
      .filter((c) => !/^(is-|has-|htmx-|x-)|^(active|open|selected|current|hidden|disabled|show|on)$/.test(c))
      .sort().join('.');
    let out = tag + (classes ? '.' + classes : '');
    const type = e.getAttribute('type');
    if (type && /^(input|button)$/.test(tag)) out += `[type=${type}]`;
    const role = e.getAttribute('role');
    if (role) out += `[role=${role}]`;
    const name = e.getAttribute('name');
    if (name) out += `[name=${name.replace(/\d+/g, '#')}]`;
    const tid = e.getAttribute('data-testid');
    if (tid) out += `[data-testid=${tid}]`;
    return out;
  };
  const signature = (e: Element): string => {
    const o = own(e);
    if (stableId(e)) return o;
    for (let p = e.parentElement; p; p = p.parentElement) {
      const id = stableId(p);
      if (id) return `#${id} > ${o}`;
      const t = p.getAttribute('data-testid');
      if (t) return `[data-testid=${t}] > ${o}`;
    }
    return o;
  };
  const isCheckLike = (el: Element) =>
    (el.tagName === 'INPUT' && /^(checkbox|radio)$/.test((el as HTMLInputElement).type)) ||
    /^(switch|checkbox|radio)$/.test(el.getAttribute('role') || '');
  const proseLinkExempt = (a: Element): boolean => {
    if (a.tagName !== 'A' || !a.hasAttribute('href')) return false;
    if (getComputedStyle(a).display !== 'inline') return false;
    let b: Element | null = a.parentElement;
    while (b && getComputedStyle(b).display.startsWith('inline')) b = b.parentElement;
    if (!b) return false;
    const w = document.createTreeWalker(b, NodeFilter.SHOW_TEXT);
    let n: Node | null;
    while ((n = w.nextNode())) {
      if (!/\p{L}/u.test(n.textContent || '')) continue;
      if (n.parentElement && n.parentElement.closest('a, button')) continue;
      return true;
    }
    return false;
  };

  let measured = 0;
  const best = new Map<string, Violation>();
  for (const el of Array.from(root.querySelectorAll('*'))) {
    // [tabindex] alone is not a candidate; it counts only with a click attribute (isCandidate).
    if (!isCandidate(el)) continue;
    if (!isVisible(el)) continue;
    const ok = el.closest('[data-touch-ok]');
    let reasonMissing = false;
    if (ok) {
      if ((ok.getAttribute('data-touch-ok') || '').trim() !== '') continue;
      reasonMissing = true;
    }
    let r = el.getBoundingClientRect();
    let sigSuffix = '';
    if (isCheckLike(el)) {
      const elId = el.getAttribute('id');
      const lbl = el.closest('label') || (elId ? document.querySelector(`label[for="${CSS.escape(elId)}"]`) : null);
      if (lbl && isVisible(lbl)) {
        const lr = lbl.getBoundingClientRect();
        if (visible(lr)) {
          const parts = [lr];
          if (r.width >= 1 && r.height >= 1) parts.push(r);
          const left = Math.min(...parts.map((q) => q.left));
          const top = Math.min(...parts.map((q) => q.top));
          const right = Math.max(...parts.map((q) => q.right));
          const bottom = Math.max(...parts.map((q) => q.bottom));
          r = new DOMRect(left, top, right - left, bottom - top);
          sigSuffix = ' (label)';
        }
      }
    }
    if (!visible(r)) continue;
    measured++;
    if (proseLinkExempt(el)) continue;
    if (r.width >= 43.5 && r.height >= 43.5) continue;
    const sig = signature(el) + sigSuffix + (reasonMissing ? ' (data-touch-ok needs a reason)' : '');
    const v = { sig, w: Math.round(r.width), h: Math.round(r.height) };
    const prev = best.get(sig);
    if (!prev || v.w * v.h < prev.w * prev.h || (v.w * v.h === prev.w * prev.h && v.w < prev.w)) best.set(sig, v);
  }
  const violations = Array.from(best.values()).sort((a, b) => (a.sig < b.sig ? -1 : a.sig > b.sig ? 1 : 0));
  return { measured, violations };
}

type Entry = { route: string; vp: string; sig: string; size: string };

function readBaseline(): { lines: string[]; entries: (Entry & { line: string; n: number })[] } {
  const lines = fs.existsSync(BASELINE) ? fs.readFileSync(BASELINE, 'utf8').split('\n') : [];
  const entries: (Entry & { line: string; n: number })[] = [];
  lines.forEach((line, i) => {
    if (line.startsWith('#') || line.trim() === '') return;
    const f = line.split('\t');
    entries.push({ route: f[0], vp: f[1], sig: f[2], size: f[3], line, n: i + 1 });
  });
  return { lines, entries };
}

test.describe('scanner sanity (setContent)', () => {
  test.use({ viewport: VIEWPORTS.phone, hasTouch: true, isMobile: true });

  test('reports only the 30x30 button', async ({ page }) => {
    await page.setContent(`<!doctype html><body style="margin:0">
      <button class="tiny" style="width:30px;height:30px;display:block">a</button>
      <button style="width:44px;height:44px;display:block">b</button>
      <p>Read the <a href="#x">terms</a> first.</p>
      <label style="display:block;height:48px"><input type="checkbox" name="agree" style="opacity:0;width:1px;height:1px"> Agree</label>
      <button style="width:20px;height:20px;display:block" data-touch-ok="fixture: reviewed">c</button>
      <button style="width:20px;height:20px;display:none">d</button>
    </body>`);
    const r = await page.evaluate(scan, { chrome: true });
    expect(r.violations).toEqual([{ sig: 'button.tiny', w: 30, h: 30 }]);
    expect(r.measured).toBeGreaterThanOrEqual(4);
  });

  test('an empty data-touch-ok does not exempt', async ({ page }) => {
    await page.setContent(`<body style="margin:0"><button class="tiny" data-touch-ok="" style="width:30px;height:30px">a</button></body>`);
    const r = await page.evaluate(scan, { chrome: true });
    expect(r.violations).toEqual([{ sig: 'button.tiny (data-touch-ok needs a reason)', w: 30, h: 30 }]);
  });

  test('repeated signatures collapse to the smallest', async ({ page }) => {
    await page.setContent(`<body style="margin:0">
      <button class="tiny" style="width:32px;height:40px;display:block">a</button>
      <button class="tiny" style="width:30px;height:30px;display:block">b</button></body>`);
    const r = await page.evaluate(scan, { chrome: true });
    expect(r.violations).toEqual([{ sig: 'button.tiny', w: 30, h: 30 }]);
  });
});

test.describe('baseline file', () => {
  test('is well formed, sorted and deduplicated', () => {
    const { entries } = readBaseline();
    const routes = new Set(ROUTES);
    const seen = new Set<string>();
    let prev = '';
    for (const e of entries) {
      const f = e.line.split('\t');
      const bad = `malformed or stale touch-target baseline line ${e.n}: ${e.line} — expected <route>\t<phone|till>\t<signature>\t<W>x<H>, route in TILL_GET_ROUTES or /self-order, sorted; delete or fix it`;
      expect(f.length === 4 && (e.vp === 'phone' || e.vp === 'till') && routes.has(e.route) && /^\d+x\d+$/.test(e.size) && e.sig !== '', bad).toBe(true);
      const key = `${e.route}\t${e.vp}\t${e.sig}`;
      expect(seen.has(key), bad + ' (duplicate)').toBe(false);
      seen.add(key);
      expect(key >= prev, bad + ' (out of order)').toBe(true);
      prev = key;
    }
  });
});

const collected: string[] = [];
const expected = ROUTES.length * Object.keys(VIEWPORTS).length;

test.describe('route sweep', () => {
  test.describe.configure({ mode: WRITE ? 'serial' : 'parallel' });

  // Seeds so list pages have a row to lay out (an empty list proves
  // nothing). Unique values: worker tills are shared with other specs.
  test.beforeAll(async ({ request }) => {
    const day = 86_400_000;
    const iso = (ms: number) => new Date(ms).toISOString().slice(0, 10);
    const now = Date.now();
    const seeds: [string, Record<string, string>, string][] = [
      ['/api/promotions', { code: `TT5989${now.toString(36).toUpperCase()}`, type: 'percent', value_percent: '10', description: 'Touch target row', starts_at: iso(now - 30 * day), ends_at: iso(now + 365 * day), customer_id: '' }, '/promotions'],
      ['/api/kitchen-stations', { name: `Touch station 5989 ${now}`, destination_type: 'printer', printer_address: '192.0.2.10:9100' }, '/kitchen-stations'],
    ];
    for (const [url, form, back] of seeds) {
      const r = await request.post(url, { form, maxRedirects: 0 });
      expect(r.status(), url).toBe(303);
      expect(r.headers().location, url).toBe(back);
    }
    // State other specs leave behind on the shared worker till decides
    // whether these controls exist at all, so create it here: a free table
    // (/tables floor plan), categories with items that overflow the sell
    // screen's tab strip into its "More" button, an inactive category for
    // /designer, and one sale (/orders lists it with its status buttons).
    // Without this the baseline flaps between a clean till and a used one.
    const t = await request.post('/api/tables', { form: { label: `T5989 ${now.toString(36)}`, area_zone: '', shape: 'rect', seat_count: '4' }, maxRedirects: 0 });
    expect(t.status(), '/api/tables').toBe(303);
    expect(t.headers().location, '/api/tables').toBe('/tables');
    // The sell screen lists only categories that hold an item, so each of the
    // six long-named ones gets one; a seventh stays empty and inactive (its
    // /designer row shows an "Activate" button).
    const catId = async (name: string) => {
      const html = await (await request.get('/catalog')).text();
      const m = html.match(new RegExp(`<option value="([^"]+)"[^>]*>${name}`));
      expect(m, `category ${name} in the /catalog picker`).not.toBeNull();
      return m![1];
    };
    for (let i = 0; i < 7; i++) {
      const name = `Touch category number ${i} ${now.toString(36)}`;
      const c = await request.post('/api/designer/categories', { form: { name, color: '' } });
      expect(c.ok(), `/api/designer/categories ${c.status()}`).toBe(true);
      const id = await catId(name);
      if (i < 6) {
        const it = await request.post('/api/catalog/item', { form: { name: `Touch item ${i} ${now.toString(36)}`, price: '150', categoryId: id } });
        expect(it.ok(), `/api/catalog/item ${it.status()}`).toBe(true);
      } else {
        const off = await request.post(`/api/designer/categories/${id}/active`, { form: { active: '0' } });
        expect(off.ok(), `deactivate ${off.status()}`).toBe(true);
      }
    }
    // A modifier group with an option, attached to a category: /modifiers
    // lists its chips, remove button and category checkboxes.
    const ownerName = `Touch owner ${now.toString(36)}`;
    const cat0 = await catId(`Touch category number 0 ${now.toString(36)}`);
    const owner = await request.post('/api/catalog/item', { form: { name: ownerName, price: '150', categoryId: cat0 } });
    expect(owner.ok(), `/api/catalog/item ${owner.status()}`).toBe(true);
    const catalog = await (await request.get('/catalog')).text();
    const ownerTag = catalog.match(new RegExp(`<[^>]*data-name="${ownerName}"[^>]*>`));
    const ownerId = ownerTag?.[0].match(/data-id="([^"]+)"/)?.[1];
    expect(ownerId, `owner item ${ownerName} on /catalog`).toBeTruthy();
    const groupName = `Touch group ${now.toString(36)}`;
    const g = await request.post('/api/catalog/modifier-group', { form: { itemId: ownerId!, name: groupName, minSelect: '0', maxSelect: '1' } });
    expect(g.ok(), `/api/catalog/modifier-group ${g.status()}`).toBe(true);
    const panel = await (await request.get(`/api/catalog/modifier-groups-panel?item_id=${ownerId}`)).text();
    const groupIdx = panel.indexOf(`>${groupName}<`);
    expect(groupIdx, 'new group on the owner panel').toBeGreaterThan(-1);
    const gids = [...panel.slice(0, groupIdx).matchAll(/data-group-id="([^"]*)"/g)];
    const groupId = gids[gids.length - 1][1];
    const o = await request.post('/api/catalog/modifier-option', { form: { groupId, itemId: ownerId!, name: `Touch option ${now.toString(36)}` } });
    expect(o.ok(), `/api/catalog/modifier-option ${o.status()}`).toBe(true);
    const at = await request.post('/api/catalog/modifier-group/attach-category', { form: { groupId, categoryId: cat0 } });
    expect(at.ok(), `attach-category ${at.status()}`).toBe(true);
    await request.post('/api/pos/reset');
    await request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
    const sale = await request.post('/api/pos/tender', { headers: { 'Content-Type': 'application/json' }, data: { payments: [{ method: 'cash', amount: 100000 }] } });
    expect(sale.ok(), '/api/pos/tender').toBe(true);
    await request.post('/api/pos/reset');
  });

  for (const vp of Object.keys(VIEWPORTS) as VP[]) {
    test.describe(vp, () => {
      test.use({ viewport: VIEWPORTS[vp], hasTouch: true, isMobile: true });
      for (const route of ROUTES) {
        test(`${vp} ${route} touch targets >= 44px`, async ({ page }) => {
          const resp = await page.goto(route);
          expect(resp?.status(), `${route} status`).toBe(200);
          expect(new URL(page.url()).pathname, `${route} redirected`).toBe(route);
          await page.waitForLoadState('load');
          await page.waitForTimeout(300);
          const r = await page.evaluate(scan, { chrome: CHROME_ROUTES.has(route) });
          if (NO_CONTROLS_ROUTES.has(route)) {
            await expect(page.locator('main h1'), `${route} @ ${vp}: page did not render`).toBeVisible();
          } else {
            expect(r.measured, `${route} @ ${vp}: scanner measured no interactive element (blank or wrong page?)`).toBeGreaterThan(0);
          }

          if (WRITE) {
            for (const v of r.violations) collected.push(`${route}\t${vp}\t${v.sig}\t${v.w}x${v.h}`);
            collected.push(`#done\t${route}\t${vp}`);
            return;
          }

          const base = readBaseline().entries.filter((e) => e.route === route && e.vp === vp);
          const baseSigs = new Set(base.map((e) => e.sig));
          const nowSigs = new Set(r.violations.map((v) => v.sig));
          const problems: string[] = [];
          for (const v of r.violations) {
            if (baseSigs.has(v.sig)) continue;
            problems.push(`touch target under 44px: ${route} @ ${vp} — ${v.sig} is ${v.w}x${v.h} CSS px. Fix the CSS (min 44x44; e2e/README.md "Touch targets"), or mark a reviewed exception data-touch-ok="<reason>" on the element. New UI never goes in the baseline (pre-existing only: ${route}\t${vp}\t${v.sig}\t${v.w}x${v.h}).`);
          }
          for (const e of base) {
            if (nowSigs.has(e.sig)) continue;
            problems.push(`stale touch-target baseline entry (shrink-only): ${route} @ ${vp} ${e.sig} now meets 44px or is gone — delete this line from scripts/ci/touch-target-baseline.txt: ${e.line}`);
          }
          expect(problems, problems.join('\n')).toEqual([]);
        });
      }
    });
  }

  test.afterAll(() => {
    if (!WRITE) return;
    const done = collected.filter((l) => l.startsWith('#done')).length;
    if (done !== expected) throw new Error(`incomplete run (${done}/${expected}), baseline NOT written`);
    const data = Array.from(new Set(collected.filter((l) => !l.startsWith('#done')))).sort((a, b) => {
      const ka = a.split('\t').slice(0, 3).join('\t');
      const kb = b.split('\t').slice(0, 3).join('\t');
      return ka < kb ? -1 : ka > kb ? 1 : 0;
    });
    const head = readBaseline().lines.filter((l) => l.startsWith('#'));
    fs.writeFileSync(BASELINE, [...head, ...data].join('\n') + '\n');
  });
});
