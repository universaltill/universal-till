package plugins

import "github.com/tetratelabs/wazero"

// SharedCompileCacheForTest, when non-nil, makes every WasmRuntime built
// afterwards in this process share this one in-memory wazero compilation
// cache instead of its own (or the on-disk one), so a test binary compiles
// each distinct guest once (ut-docs#7091). Only a test binary's TestMain sets
// it, before m.Run, and clears it after; production never touches it
// (TestSharedCompileCacheForTest_OnlySetFromTests pins that). It is a var, not
// a setter func, because a func called only from _test.go files reads as
// unreachable to scripts/ci/guard-deadcode-baseline.sh.
var SharedCompileCacheForTest wazero.CompilationCache
