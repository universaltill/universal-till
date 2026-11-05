// Unit tests for new-keys.js (ut-docs#2805) — plain `node --test`, no
// Playwright, no till. Run by .github/workflows/locale-render-audit.yml
// before the render audit itself:
//
//   node --test e2e/tests-i18n-audit/lib/new-keys_test.js
//
// Named *_test.js (not *.test.js / *.spec.js) on purpose: Playwright's
// default testMatch would otherwise pick it up as a render-audit spec under
// playwright.locale-audit.config.ts's testDir.
const test = require('node:test');
const assert = require('node:assert/strict');
const { newKeyOnlyValues, lagKeyOnlyValues, partitionFindings } = require('./new-keys');

const flag = (text) => ({ topicId: 'tills', route: '/tills', text });

const BASE = { 'tills.title': 'Tills', 'common.save': 'Save' };
const HEAD = {
  ...BASE,
  'tills.refresh_hint': 'Link, version and queue refresh by themselves every 10 seconds.',
  // A NEW key whose value collides with an EXISTING key's value: the text
  // on screen can't tell us which key rendered it, so it must stay strict.
  'tills.save_again': 'Save',
};

test('PR mode: text of a key new in this PR is deferred, not failed', () => {
  const { failures, deferred } = partitionFindings(
    [flag('Link, version and queue refresh by themselves every 10 seconds.')],
    HEAD,
    BASE,
  );
  assert.deepEqual(failures, []);
  assert.equal(deferred.length, 1);
});

test('PR mode: same text whose key already exists on base still fails', () => {
  const { failures, deferred } = partitionFindings([flag('Tills')], HEAD, BASE);
  assert.equal(failures.length, 1);
  assert.deepEqual(deferred, []);
});

test('PR mode: a new key sharing its value with a base key still fails', () => {
  const { failures, deferred } = partitionFindings([flag('Save')], HEAD, BASE);
  assert.equal(failures.length, 1);
  assert.deepEqual(deferred, []);
  assert.equal(newKeyOnlyValues(HEAD, BASE).has('Save'), false);
});

test('no base catalog (push to main / non-PR run): everything fails (strict)', () => {
  const flags = [flag('Link, version and queue refresh by themselves every 10 seconds.'), flag('Tills')];
  for (const base of [null, undefined]) {
    const { failures, deferred } = partitionFindings(flags, HEAD, base);
    assert.equal(failures.length, 2);
    assert.deepEqual(deferred, []);
  }
});

test('values are compared trimmed, like the rendered lines', () => {
  const head = { ...BASE, 'x.new': '  Padded new string  ' };
  const { failures, deferred } = partitionFindings([flag('Padded new string')], head, BASE);
  assert.deepEqual(failures, []);
  assert.equal(deferred.length, 1);
});

// ---- pack-lag carve-out (ut-docs#5583) ------------------------------------
// A PRE-EXISTING key (same English value on base) the de pack merely lags on.
const LAG_BASE = { 'tills.title': 'Tills', 'reports.days.last_24h': 'Last 24 hours', 'a.one': 'Shared', 'b.two': 'Shared' };
const LAG_HEAD = { ...LAG_BASE };
const PACK = { 'tills.title': 'Kassen', 'a.one': 'Geteilt' };

test('lag: pre-existing key missing from the pack is deferred, with its key', () => {
  const r = partitionFindings([flag('Last 24 hours')], LAG_HEAD, LAG_BASE, PACK);
  assert.deepEqual(r.failures, []);
  assert.equal(r.deferredLag.length, 1);
  assert.deepEqual(r.deferredLag[0].keys, ['reports.days.last_24h']);
  assert.equal(r.deferred.length, 1);
  assert.deepEqual(lagKeyOnlyValues(LAG_HEAD, LAG_BASE, PACK).get('Last 24 hours'), ['reports.days.last_24h']);
});

test('lag: blank / whitespace / non-string pack value counts as lagging; real value does not', () => {
  for (const v of ['', '   ', null, 5]) {
    const r = partitionFindings([flag('Last 24 hours')], LAG_HEAD, LAG_BASE, { ...PACK, 'reports.days.last_24h': v });
    assert.deepEqual(r.failures, [], JSON.stringify(v));
    assert.equal(r.deferredLag.length, 1);
  }
  const translated = partitionFindings([flag('Last 24 hours')], LAG_HEAD, LAG_BASE, { ...PACK, 'reports.days.last_24h': 'Letzte 24 Stunden' });
  assert.equal(translated.failures.length, 1);
  // A cognate (pack value == English) is "translated": stays strict.
  const cognate = partitionFindings([flag('Last 24 hours')], LAG_HEAD, LAG_BASE, { ...PACK, 'reports.days.last_24h': 'Last 24 hours' });
  assert.equal(cognate.failures.length, 1);
});

test('lag: English value changed in this PR (head != base) is not deferred', () => {
  const head = { ...LAG_BASE, 'reports.days.last_24h': 'Past 24 hours' };
  const r = partitionFindings([flag('Past 24 hours')], head, LAG_BASE, PACK);
  assert.equal(r.failures.length, 1);
  assert.deepEqual(r.deferredLag, []);
});

test('lag: text shared with a pack-translated key fails; shared by two lagging keys is deferred', () => {
  // "Shared": a.one is translated, b.two lags -> ambiguous -> fail.
  const amb = partitionFindings([flag('Shared')], LAG_HEAD, LAG_BASE, PACK);
  assert.equal(amb.failures.length, 1);
  // Neither translated -> deferred, both keys named.
  const r = partitionFindings([flag('Shared')], LAG_HEAD, LAG_BASE, { 'tills.title': 'Kassen' });
  assert.deepEqual(r.failures, []);
  assert.deepEqual(r.deferredLag[0].keys.sort(), ['a.one', 'b.two']);
});

test('lag: hardcoded literal (no key) still fails; mixed list partitions', () => {
  const r = partitionFindings(
    [flag('Brand new hardcoded English'), flag('Last 24 hours'), flag('Tills')],
    LAG_HEAD,
    LAG_BASE,
    PACK,
  );
  assert.deepEqual(r.failures.map((f) => f.text), ['Brand new hardcoded English', 'Tills']);
  assert.deepEqual(r.deferredLag.map((f) => f.text), ['Last 24 hours']);
});

test('lag: no base catalog => nothing deferred even with a pack; no pack => new-key rule only', () => {
  const flags = [flag('Last 24 hours')];
  for (const base of [null, undefined]) {
    const r = partitionFindings(flags, LAG_HEAD, base, PACK);
    assert.equal(r.failures.length, 1);
    assert.deepEqual(r.deferred, []);
  }
  for (const pack of [null, undefined]) {
    const r = partitionFindings(flags, LAG_HEAD, LAG_BASE, pack);
    assert.equal(r.failures.length, 1);
  }
  // new-key rule still works without a pack
  const head = { ...LAG_BASE, 'x.new': 'Only new' };
  const r = partitionFindings([flag('Only new')], head, LAG_BASE, undefined);
  assert.equal(r.deferredNew.length, 1);
  assert.deepEqual(r.failures, []);
});

test('lag: a key present only on base (removed in head) cannot defer its text', () => {
  const head = { 'tills.title': 'Tills' };
  const base = { 'tills.title': 'Tills', 'gone.key': 'Gone text', 'x.keep': 'Gone text' };
  const r = partitionFindings([flag('Gone text')], { ...head, 'x.keep': 'Gone text' }, base, {});
  // x.keep lags, but base-only 'gone.key' also owns the text and is not a lagging head key.
  assert.equal(r.failures.length, 1);
});
