package plugins

import "github.com/tetratelabs/wazero"

// sharedCompileCacheForTest is set only by ShareCompileCacheForTest (a test
// binary's TestMain); production never touches it.
var sharedCompileCacheForTest wazero.CompilationCache

// ShareCompileCacheForTest makes every WasmRuntime built afterwards in this
// process share one in-memory wazero compilation cache instead of its own (or
// the on-disk one), so a test binary compiles each distinct guest once
// (ut-docs#7091). Call it from TestMain before m.Run, never concurrently with
// building a runtime; production never calls it.
func ShareCompileCacheForTest() (restore func()) {
	prev := sharedCompileCacheForTest
	sharedCompileCacheForTest = wazero.NewCompilationCache()
	return func() { sharedCompileCacheForTest = prev }
}
