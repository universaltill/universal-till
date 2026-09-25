package logging

import (
	"io"
	"sync/atomic"
)

// captureSink receives every formatted log line, whatever the level, while
// a test has one installed (CaptureForTest).
type captureSink struct{ w io.Writer }

var capture atomic.Pointer[captureSink]

// CaptureForTest tees every log line -- Debug included, regardless of the
// configured level -- to w until the returned restore func runs. Test-only:
// it lets a test assert that a secret (a PIN, a PIN hash) never reaches the
// log at any level (ADR-0115 §1). w must be safe for concurrent use. There
// is no production caller.
func CaptureForTest(w io.Writer) (restore func()) {
	prev := capture.Swap(&captureSink{w: w})
	return func() { capture.Store(prev) }
}

// teeCapture forwards one formatted line to the installed sink, if any.
func teeCapture(level Level, msg string) {
	if c := capture.Load(); c != nil {
		_, _ = io.WriteString(c.w, "["+level.String()+"] "+msg+"\n")
	}
}
