// ut-docs#2345: builds the till binary ONCE per run, so the per-worker
// servers the `default` project boots (tests/worker-till.ts) can each
// spawn it directly instead of every worker paying for its own `go build`.
// `e2e/.bin/` is gitignored. Wired in via playwright.config.ts's
// `globalSetup`; the worker fixture falls back to building into its own
// data dir if this output is missing for any reason.
import { execFileSync } from 'child_process';
import fs from 'fs';
import path from 'path';
import { PREBUILT_BIN, REPO_ROOT } from './tests/worker-till';

export default function globalSetup(): void {
  fs.mkdirSync(path.dirname(PREBUILT_BIN), { recursive: true });
  execFileSync('go', ['build', '-o', PREBUILT_BIN, '.'], { cwd: REPO_ROOT, stdio: 'inherit' });
}
