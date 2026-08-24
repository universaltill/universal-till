// Package clock is a tiny leaf wrapper over time.Now whose only reason to
// exist is to let the manual's screenshot harness (`make docs-shots`) pin
// "now" to a fixed instant, so any screen that legitimately renders the
// current time (the receipt designer's sample ticket, the back-office
// "recent problems" timestamps) captures byte-identical PNGs run-to-run
// instead of drifting with the wall clock (ut-docs#930).
//
// It is deliberately test-support only: the pin is read from an env var that
// ONLY the docs-shots Playwright webServer sets
// (e2e/playwright.docs.config.ts). Production, the e2e suite, real installs —
// none of them set it, so Now() is exactly time.Now() there. No product
// behaviour changes; this is a "pin the clock" fixture of the kind
// docs-shots.spec.ts's own design note calls for.
package clock

import (
	"os"
	"strings"
	"sync"
	"time"
)

// DocsShotsNowEnv, when set to an RFC3339 timestamp, pins Now() to that fixed
// instant. Exported so the (Go-side) callers and tests can name it without a
// second copy of the string; the value is only ever SET from the docs-shots
// webServer config, never from Go.
const DocsShotsNowEnv = "UT_DOCS_SHOTS_NOW"

var (
	pinnedOnce sync.Once
	pinnedTime time.Time
	pinnedOK   bool
)

// parsePinned resolves the pin from a raw env value: an RFC3339 timestamp
// pins Now(); empty or unparseable falls back to the live clock. Pure and
// exported-to-package so the resolution can be unit-tested without racing the
// sync.Once cache in Now().
func parsePinned(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// Now returns the current time, or the fixed instant pinned via
// UT_DOCS_SHOTS_NOW when that env var holds a valid RFC3339 timestamp. The
// env is read once, on first use, so this stays cheap on the logging hot
// path.
func Now() time.Time {
	pinnedOnce.Do(func() {
		pinnedTime, pinnedOK = parsePinned(os.Getenv(DocsShotsNowEnv))
	})
	if pinnedOK {
		return pinnedTime
	}
	return time.Now()
}
