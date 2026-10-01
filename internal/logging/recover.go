package logging

import "runtime/debug"

// RecoverAndLog recovers a panic in the calling goroutine and logs it at
// ERROR, named and with its stack. Use it as the FIRST statement of every
// goroutine (ut-docs#3304, enforced by TestNoUnrecoveredGoroutines):
//
//	go func() {
//		defer logging.RecoverAndLog("pkg.what")
//		...
//	}()
//
// It must be the deferred function itself — recover() only stops a panic
// when called directly by a deferred function. The line goes through L(),
// so it is redacted and lands in till.log and the Problems ring rather than
// raw on stderr. The panic is not re-raised: the goroutine just ends, so a
// background task never takes the till down mid-sale (the same choice as
// pluginUpdateCheckTick's recover).
func RecoverAndLog(name string) {
	if r := recover(); r != nil {
		L().Errorf("[%s] recovered from goroutine panic: %v\n%s", name, r, debug.Stack())
	}
}
