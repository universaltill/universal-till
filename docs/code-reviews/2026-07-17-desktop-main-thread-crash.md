# Code review — v0.2.11 mac app launch crash (Cocoa main-thread)

**Date:** 2026-07-17
**Branch:** `fix/desktop-main-thread`
**Field report (Farshid):** upgraded to v0.2.11 → app "not opening anymore".

## Diagnosis

- The auto-update itself worked: `/Applications/Universal Till.app` was a
  valid, correctly signed v0.2.11 bundle.
- Each launch attempt spawned the `unitill-pos` server child and then the
  `unitill-desktop` window shell **aborted** — six orphaned server processes,
  no window. Running the shell in a terminal showed the abort:
  `NSInternalInconsistencyException: 'API misuse: setting the main menu on a
  non-main thread'` in `RunWebView`, with goroutine 1 on **m=5**.
- Root cause: the native shell (since v0.2.8) never called
  `runtime.LockOSThread()`. `main()` blocks in the wait-for-server dial loop
  before `showWindow`, giving the Go scheduler a window to migrate the main
  goroutine off the initial OS thread; Cocoa requires UI on the process's
  first thread and aborts. **Latent timing race, not a v0.2.11 regression**:
  v0.2.11's diff doesn't touch the shell. With Farshid's real data the server
  start is slow enough that the dial loop actually spins and the migration
  hits reliably; the quick fresh-data launches used to verify earlier builds
  got lucky.

## Fix

`cmd/unitill-desktop/desktop.go`: `func init() { runtime.LockOSThread() }` —
pins the main goroutine to the initial thread during package init, before
any other goroutine exists. Applies to all platforms (GTK/webview_go has the
same first-thread requirement).

## Verification (the failure mode is a launch, so tested by launching)

- Reproduced the abort with the **released** v0.2.11 binary on the affected
  machine (crash log captured).
- Built the fixed .app: shell stays alive, window opens, server answers;
  relaunch cycle repeated OK. Installed to `/Applications` over the broken
  copy (shop data untouched) and verified running.
- v0.2.12 released with the fix; the released .dmg download was mounted and
  launch-tested the same way before telling Farshid to trust auto-update.

## Lesson

A "release verified" that only exercises the server/pages is not enough for
the desktop shell — every release's .dmg must get one real `open` test
(orphaned `unitill-pos` processes with no `unitill-desktop` is the tell).
Added to the release checklist in RELEASING.md.
