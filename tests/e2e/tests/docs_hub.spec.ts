import { test, expect } from '../support/fixtures';
import { readFile } from 'node:fs/promises';
import path from 'node:path';

test.describe('Docs Hub Consistency', () => {
  test('docs hub README exists and links to POS, marketplace, plugins', async () => {
    const docsRoot = process.env.DOCS_ROOT || '~/repos/unitill/docs/docs';
    const readmePath = path.join(docsRoot, 'README.md');

    const contents = await readFile(readmePath, 'utf8');

    expect(contents).toContain('Universal Till');
    expect(contents).toMatch(/POS/i);
    expect(contents).toMatch(/Marketplace/i);
    expect(contents).toMatch(/Plugin/i);
  });

  test('docs overview captures platform promises', async () => {
    const docsRoot = process.env.DOCS_ROOT || '~/repos/unitill/docs/docs';
    const overviewPath = path.join(docsRoot, 'overview.md');

    const contents = await readFile(overviewPath, 'utf8');

    expect(contents).toMatch(/offline-first/i);
    expect(contents).toMatch(/Go/i);
    expect(contents).toMatch(/plugin/i);
    expect(contents).toMatch(/multi-language/i);
  });

  test('legacy specs are labeled as legacy', async () => {
    const docsRoot = process.env.DOCS_ROOT || '~/repos/unitill/docs/docs';
    const legacyIndexPath = path.join(docsRoot, 'specs', 'README.md');

    const contents = await readFile(legacyIndexPath, 'utf8');

    expect(contents).toMatch(/legacy/i);
    expect(contents).toMatch(/input/i);
  });
});
