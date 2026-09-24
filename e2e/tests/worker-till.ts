// ut-docs#2345: per-worker till servers for the `default` project.
//
// The `default` project used to drive ONE shared `unitill-pos` process
// (port 8091, booted once via playwright.config.ts's `webServer` array),
// which is why `workers` was pinned to 1: `internal/pos.Engine` is a
// server-side singleton, so two workers against the same server race each
// other's basket/settings state. Booting a separate server per worker
// (own throwaway data dir, own port) removes the shared state entirely,
// and fixtures.ts's per-file reset then isolates files within a worker
// exactly as it always did — each Playwright worker is its own Node
// process, so that module-level Set is per-worker already.
//
// This module is the process plumbing; the fixture that calls it lives in
// fixtures.ts (`workerServerURL`). It reproduces what e2e/run-till.sh does
// for the shared server (the docs-shots harness still uses that script):
// fresh data dir, the two seeds, then the BINARY run from INSIDE the data
// dir — see run-till.sh's own comment for why the CWD matters (the app's
// legacy-DB migration is CWD-relative; running from the repo root can
// silently import a real local dev database into a "throwaway" till).
import { execFileSync, spawn, type ChildProcess, type ExecFileSyncOptions } from 'child_process';
import fs from 'fs';
import os from 'os';
import path from 'path';

export const REPO_ROOT = path.resolve(__dirname, '..', '..');

// Built ONCE per run by e2e/global-setup.ts, not once per worker. The
// fallback below (build into the worker's own data dir) only exists so a
// worker still boots if global setup was somehow skipped.
export const PREBUILT_BIN = path.join(REPO_ROOT, 'e2e', '.bin', 'unitill-pos-e2e');

// A fresh, previously-unused port band — deliberately NOT 8091+N: the four
// single-server projects (auth/ai-identify/layout/diagnostics) keep their
// static 8092-8095 (and the diagnostics spec's fake ut-cloud on 8096), and
// they run in other worker slots of the SAME overall run.
export const WORKER_PORT_BASE = 9091;

// How long a fresh server may take to answer /healthz (covers the two
// `go run` seeds, which compile on a cold build cache).
const BOOT_TIMEOUT_MS = 60_000;
// SIGTERM grace before SIGKILL at teardown.
const STOP_GRACE_MS = 5_000;

export function workerPort(parallelIndex: number): number {
  return WORKER_PORT_BASE + parallelIndex;
}

export async function isHealthy(baseURL: string, timeoutMs = 1_000): Promise<boolean> {
  try {
    const res = await fetch(`${baseURL}/healthz`, { signal: AbortSignal.timeout(timeoutMs) });
    return res.status === 200;
  } catch {
    return false;
  }
}

// ut-docs#2499: the shared worker till browses in strip_overflow — the
// quick-button strip every pre-#2499 sell-screen spec taps tiles on. The
// setting's real default (category_tabs) renders category tiles on /, with
// no product tile until one is tapped, so without this every spec that
// clicks a .btn-tile on the sale screen would break at once. Set through
// the till's own endpoint, after boot (UT_AUTH=off, so no elevation), not
// baked into seed_demo: run-till.sh / docs-shots reuse that seed and the
// manual's screenshots should show the real default. Specs that want
// another mode switch it explicitly (helpers.ts's setBrowsingMode) and
// restore this one afterwards.
export const WORKER_TILL_BROWSING_MODE = 'strip_overflow';
async function applyWorkerTillDefaults(baseURL: string): Promise<void> {
  const res = await fetch(`${baseURL}/api/settings/browsing-mode`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ mode: WORKER_TILL_BROWSING_MODE }),
    signal: AbortSignal.timeout(5_000),
  });
  if (!res.ok) {
    throw new Error(`worker till ${baseURL}: could not set browsing mode ${WORKER_TILL_BROWSING_MODE}: HTTP ${res.status}`);
  }
}

export type WorkerTill = {
  url: string;
  // Resolves once the server is gone and its data dir removed. A no-op
  // for a reused (not spawned here) server, mirroring `reuseExistingServer`.
  stop: () => Promise<void>;
};

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

function waitForExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(true);
  return new Promise((resolve) => {
    const t = setTimeout(() => resolve(false), timeoutMs);
    child.once('exit', () => {
      clearTimeout(t);
      resolve(true);
    });
  });
}

export async function startWorkerTill(parallelIndex: number): Promise<WorkerTill> {
  const port = workerPort(parallelIndex);
  const url = `http://127.0.0.1:${port}`;
  const tag = `[till:${port}]`;

  if (await isHealthy(url)) {
    // Same semantics as the `webServer` entries' `reuseExistingServer:
    // !process.env.CI`: locally, a till a developer left running on this
    // worker's port is reused as-is (fast edit/test loop, single-spec
    // runs); in CI nothing should ever be listening there, so a busy port
    // is a leaked process from an earlier worker and gets reported rather
    // than silently shared.
    if (process.env.CI) {
      throw new Error(`${tag} something is already listening on ${url} in CI — a leaked till from an earlier worker?`);
    }
    await applyWorkerTillDefaults(url);
    return { url, stop: async () => {} };
  }

  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ut-e2e-worker-'));
  const env = { ...process.env, UT_DATA_DIR: dataDir, UT_AUTH: 'off', UT_LISTEN_ADDR: `127.0.0.1:${port}` };
  const goOpts: ExecFileSyncOptions = { cwd: REPO_ROOT, env, stdio: ['ignore', 'ignore', 'inherit'] };

  let child: ChildProcess | undefined;
  let onExit: (() => void) | undefined;
  const cleanup = () => fs.rmSync(dataDir, { recursive: true, force: true });

  try {
    // Same two seeds run-till.sh runs, against this worker's data dir.
    execFileSync('go', ['run', './e2e/seed_faq'], goOpts);
    execFileSync('go', ['run', './e2e/seed_demo'], goOpts);

    let bin = PREBUILT_BIN;
    if (!fs.existsSync(bin)) {
      bin = path.join(dataDir, '.ut-e2e-bin');
      execFileSync('go', ['build', '-o', bin, '.'], goOpts);
    }

    child = spawn(bin, [], { cwd: dataDir, env, stdio: ['ignore', 'ignore', 'pipe'] });
    // internal/server.listenWithFallback treats UT_LISTEN_ADDR as a
    // PREFERENCE, not a binding: a busy port silently walks to the next
    // one (base+1..+20), then port 0. If this worker's requested port is
    // busy, that fallback could land it on a SIBLING worker's own port
    // (e.g. 9091 busy -> 9092), which is exactly the cross-worker sharing
    // this design exists to remove — and, locally, a later worker's own
    // isHealthy() reuse check would then silently treat that till as
    // "already there," restoring it for real. Rather than let that binding
    // drift silently, fail the boot the moment the server itself reports
    // it: `bootFellBack` latches so the async healthz-poll loop below (a
    // separate tick from this synchronous listener) can throw from a
    // stable check rather than racing the loop for the log line.
    let bootFellBack: string | undefined;
    child.stderr!.on('data', (chunk: Buffer) => {
      const text = chunk.toString();
      if (!bootFellBack && text.includes('was busy')) bootFellBack = text.trim();
      process.stderr.write(`${tag} ${text.replace(/\n(?!$)/g, `\n${tag} `)}`);
    });
    // Belt and braces for a worker that exits without running fixture
    // teardown (Playwright's own worker restart DOES run it — see the
    // fixture in fixtures.ts — this covers an unexpected process.exit).
    // `fs.rmSync` is synchronous, so it's legal to call from an 'exit'
    // handler (unlike the async `stop()` path above/below).
    onExit = () => {
      try {
        child?.kill('SIGKILL');
      } catch {
        /* already gone */
      }
      cleanup();
    };
    process.once('exit', onExit);

    const deadline = Date.now() + BOOT_TIMEOUT_MS;
    while (!(await isHealthy(url))) {
      if (bootFellBack) {
        throw new Error(`${tag} server did not bind ${url} — it fell back to a different port: ${bootFellBack}`);
      }
      if (child.exitCode !== null || child.signalCode !== null) {
        throw new Error(`${tag} server exited during boot (code ${child.exitCode}, signal ${child.signalCode})`);
      }
      if (Date.now() > deadline) {
        throw new Error(`${tag} server did not answer /healthz within ${BOOT_TIMEOUT_MS}ms`);
      }
      await sleep(250);
    }
    await applyWorkerTillDefaults(url);

    const proc = child;
    const removeExitHook = onExit;
    return {
      url,
      stop: async () => {
        process.removeListener('exit', removeExitHook);
        if (proc.exitCode === null && proc.signalCode === null) {
          proc.kill('SIGTERM');
          if (!(await waitForExit(proc, STOP_GRACE_MS))) {
            proc.kill('SIGKILL');
            await waitForExit(proc, STOP_GRACE_MS);
          }
        }
        cleanup();
      },
    };
  } catch (err) {
    // `onExit` (registered above, right before the boot-poll loop that
    // can throw) already SIGKILLs the child and cleans up the data dir on
    // process exit — but it's only ever SET after spawn(), so a throw
    // from an earlier step (the two seeds, or the build fallback) reaches
    // here with it still undefined. Remove it if it exists, so a later,
    // unrelated process exit doesn't fire a stale reference; `cleanup()`
    // (fs.rmSync with force:true) is safe to call twice either way.
    if (onExit) process.removeListener('exit', onExit);
    if (child && child.exitCode === null && child.signalCode === null) {
      child.kill('SIGKILL');
      await waitForExit(child, STOP_GRACE_MS);
    }
    cleanup();
    throw err;
  }
}
