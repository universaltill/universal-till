import { test, expect } from '../support/fixtures';
import { readFile } from 'node:fs/promises';
import path from 'node:path';

// DOCS_ROOT points at the docs repo ROOT (post-2026-07-07 overhaul layout:
// README.md + architecture.md at the root, references under reference/).
const docsRoot = process.env.DOCS_ROOT || path.join(process.env.HOME || '~', 'repos/unitill/docs');

test.describe('Docs Hub Consistency', () => {
  test('docs hub README exists and links to POS, marketplace, plugins', async () => {
    const contents = await readFile(path.join(docsRoot, 'README.md'), 'utf8');

    expect(contents).toContain('Universal Till');
    expect(contents).toMatch(/POS/i);
    expect(contents).toMatch(/Marketplace/i);
    expect(contents).toMatch(/Plugin/i);
  });

  test('architecture doc captures platform promises', async () => {
    const contents = await readFile(path.join(docsRoot, 'architecture.md'), 'utf8');

    expect(contents).toMatch(/offline-first/i);
    expect(contents).toMatch(/\bGo\b/);
    expect(contents).toMatch(/plugin/i);
    expect(contents).toMatch(/language/i);
  });
});
