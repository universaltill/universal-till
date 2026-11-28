// ut-docs#7177: Playwright stops a `webServer` command with SIGKILL to its
// whole process group by default, which skips the launcher's EXIT trap and
// leaves its ~100 MB temp data dir behind. Every webServer entry sets
// `gracefulShutdown: TILL_SHUTDOWN` so the launcher gets SIGTERM first (see
// run-till-lib.sh); SIGKILL only follows after the timeout.
// scripts/ci/guard-e2e-till-cleanup.sh enforces it.
export const TILL_SHUTDOWN = { signal: 'SIGTERM', timeout: 10_000 } as const;
