package pages

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Tester additions (ut-docs#5922): a promote that lands on a LATER accepted
// sub-batch, and a promote on a 413 followed by a failed retry.

func TestSyncPush_PromoteOnSecondAcceptedSubBatch_5922(t *testing.T) {
	r := newPushRig(t)
	noKicks(t)
	r.seed(t, 6)
	r.fake.limit.Store(int64(r.oneSale) * 5 / 2) // fits 2 sales
	var n atomic.Int32
	r.fake.before = func() {
		if n.Add(1) == 4 { // 6→413, 3→413, 2→200 (written), 2→promote
			promoteNow(t, r.replicaDp)
		}
	}
	logs := captureLogs(t)
	r.tick()
	assertKeysAbsent(t, r.replicaDp, "sync.push_cursor", "sync.last_push_at", "sync.last_contact_at", salesPushBatchKey)
	if got := len(r.fake.requests()); got != 4 {
		t.Errorf("requests = %d, want 4 (no POST after the refused write)", got)
	}
	assertSkipLogged(t, logs, 1)
}

type failNth struct {
	n    atomic.Int32
	fail int32
	rt   http.RoundTripper
}

func (f *failNth) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.n.Add(1) >= f.fail {
		return nil, errors.New("tester: network down")
	}
	return f.rt.RoundTrip(r)
}

func TestSyncPush_PromoteOn413ThenRetryFails_5922(t *testing.T) {
	r := newPushRig(t)
	noKicks(t)
	r.seed(t, 2)
	r.fake.limit.Store(int64(r.oneSale) * 3 / 2)
	r.fake.before = promoteOnce(t, r.replicaDp)
	r.client = &http.Client{Transport: &failNth{fail: 2, rt: http.DefaultTransport}}
	logs := captureLogs(t)
	r.tick()
	assertKeysAbsent(t, r.replicaDp, salesPushBatchKey, "sync.push_cursor")
	if out := logs.String(); !strings.Contains(out, promotedMidPushLine) {
		t.Errorf("no promoted-mid-push line; log:\n%s", out)
	}
}
