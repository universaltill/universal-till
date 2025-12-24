import { test, expect } from '../support/fixtures';
import { readFile } from 'node:fs/promises';
import path from 'node:path';

test.describe('Plugin Lifecycle and Manifest Docs', () => {
  test('lifecycle doc includes publish -> install -> telemetry -> update flow', async () => {
    const docsRoot = process.env.DOCS_ROOT || '~/repos/unitill/docs/docs';
    const lifecyclePath = path.join(docsRoot, 'plugins', 'lifecycle.md');

    const contents = await readFile(lifecyclePath, 'utf8');

    expect(contents).toMatch(/publish/i);
    expect(contents).toMatch(/install/i);
    expect(contents).toMatch(/telemetry|status/i);
    expect(contents).toMatch(/update|rollback|remove/i);
    expect(contents).toMatch(/offline|bundle/i);
  });

  test('manifest doc defines required schema fields', async () => {
    const docsRoot = process.env.DOCS_ROOT || '~/repos/unitill/docs/docs';
    const manifestPath = path.join(docsRoot, 'plugins', 'manifest.md');

    const contents = await readFile(manifestPath, 'utf8');

    expect(contents).toMatch(/id/i);
    expect(contents).toMatch(/version/i);
    expect(contents).toMatch(/capabilities/i);
    expect(contents).toMatch(/permissions/i);
    expect(contents).toMatch(/min_host_version|compatibility/i);
  });
});
