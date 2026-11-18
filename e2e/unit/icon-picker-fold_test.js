// ut-docs#6042: the picker's JS fold must equal the Go fold
// (internal/catimport FoldForSearch) on the shared fixture.
// Run: node --test e2e/unit/icon-picker-fold_test.js
'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { fold, wordMatches } = require('../../web/public/icon-picker-filter.js');

const rows = JSON.parse(fs.readFileSync(
  path.join(__dirname, '..', '..', 'internal', 'catimport', 'testdata', 'search_fold_parity.json'), 'utf8'));

test('fixture is non-trivial', () => assert.ok(rows.length >= 20));

for (const r of rows) {
  test('fold ' + JSON.stringify(r.in), () => {
    assert.strictEqual(fold(r.in), r.out);
    assert.strictEqual(fold(r.out), r.out); // idempotent
  });
}

test('wordMatches: ZWNJ is not a word boundary, both directions (ut-docs#6052)', () => {
  assert.ok(wordMatches(fold('تخم\u200cمرغ'), fold('تخممرغ')));
  assert.ok(wordMatches(fold('تخممرغ'), fold('تخم\u200cمرغ')));
  // a real substring match: a partial query without the ZWNJ, inside a longer haystack
  assert.ok(wordMatches('egg ' + fold('تخم\u200cمرغ') + ' breakfast', fold('خممر')));
});
test('wordMatches: Arabic article is stripped for words of 4+ code points', () => {
  assert.ok(wordMatches('coffee قهوه', fold('القهوة')));
});
test('wordMatches: short "ال" words are not stripped', () => {
  assert.ok(!wordMatches('لا', fold('الا')));
});
test('wordMatches: other languages do not match English', () => {
  assert.ok(!wordMatches('coffee', fold('kahve')));
});
test('wordMatches: plain substring', () => {
  assert.ok(wordMatches('coffee latte', 'latt'));
});
