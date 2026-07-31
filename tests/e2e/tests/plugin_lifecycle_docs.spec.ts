import { test, expect } from '../support/fixtures';
import { readFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import path from 'node:path';

// DOCS_ROOT points at the docs repo ROOT; plugin references live under
// reference/ since the 2026-07-07 docs overhaul.
const docsRoot = process.env.DOCS_ROOT || path.join(process.env.HOME || '~', 'repos/unitill/docs');

test.describe('Plugin Lifecycle and Manifest Docs', () => {
  // ut-docs is private (2026-07-31): CI only checks it out when the
  // DOCS_READ_TOKEN secret exists. Skip visibly instead of failing on reads.
  test.skip(!existsSync(docsRoot), `docs repo not present at ${docsRoot} (private ut-docs not checked out)`);
  test('lifecycle doc includes publish -> install -> telemetry -> update flow', async () => {
    const contents = await readFile(path.join(docsRoot, 'reference', 'plugin-lifecycle.md'), 'utf8');

    expect(contents).toMatch(/publish/i);
    expect(contents).toMatch(/install/i);
    expect(contents).toMatch(/telemetry|status/i);
    expect(contents).toMatch(/update|rollback|remove/i);
    expect(contents).toMatch(/offline|bundle/i);
  });

  test('manifest doc defines required schema fields', async () => {
    const contents = await readFile(path.join(docsRoot, 'reference', 'plugin-manifest.md'), 'utf8');

    expect(contents).toMatch(/\bid\b/i);
    expect(contents).toMatch(/version/i);
    expect(contents).toMatch(/capabilities/i);
    expect(contents).toMatch(/permissions/i);
    expect(contents).toMatch(/min_host_version|compatibility/i);
  });
});
