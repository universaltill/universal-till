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
const { newKeyOnlyValues, partitionFindings } = require('./new-keys');

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
