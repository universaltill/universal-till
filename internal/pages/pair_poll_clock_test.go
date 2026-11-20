package pages

import (
	"sync"
	"time"
)

// Most pairing tests poll the approval endpoint back to back, which no real
// phone does (2 s cadence, ut-docs#6538 F1). Under test the poll limiters'
// clock moves 3 s on every reading, so only the tests that simulate a clock
// themselves (reader_identity_test.go) see the real limits.
var testPollClock = func() func() time.Time {
	var mu sync.Mutex
	t := time.Now()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(3 * time.Second)
		return t
	}
}()

func init() { pairPollNow = testPollClock }
