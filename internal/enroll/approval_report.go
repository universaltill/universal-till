package enroll

// The approval-key report (ut-docs#5127, reference/design-5127-till-approval-
// for-moves.md §2, review M-1). /sync carries only the key id (approval_kid,
// the quarantine rule); the key itself reaches the cloud on pair/register (in
// their bodies) or here, by POST /v1/stores/devices/approval-key with the
// device bearer and a proof of possession whose store line is the store's
// external id (EMPTY only on pair/register).
//
// Best-effort and offline-first: nothing waits on it. It runs in the
// background at most once per start and per identity (device + store), and a
// failed attempt retries slowly and a bounded number of times. 404 (an older
// cloud without the endpoint), 403 (a legacy token) and 429 are ignored; 409
// (a different key is pinned) is remembered in memory for Settings > Cloud.
// Nothing here logs a key or a signature.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// approvalRetryDelays are the waits between attempts after a failure
// (network down, 5xx, a refused credential): slow, and after the last one it
// goes on every approvalRetryTail, for ever (ADR-0222 T3: a till that could
// not pin its key must still do so once it can, or it can never heal).
// Guarded by approvalReport's mutex: kickApprovalKeyReport copies it under the
// lock before its goroutine starts, so a report still running in the
// background never reads it while a test swaps it (ut-docs#7009).
var approvalRetryDelays = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour}

// approvalRetryTail is the wait after the listed ones; guarded like them.
var approvalRetryTail = 24 * time.Hour

// KeyApprovalKeyConflictAt (RFC 3339) is when the cloud last answered 409 to
// the key report (a different key is pinned): saved so the Tills page can say
// "this till can't prove who it is" after a restart. Per till
// ("till_identity." prefix); cleared when the cloud takes the key.
const KeyApprovalKeyConflictAt = "till_identity.approval_key_conflict_at"

// approvalWake lets a heal cut a long wait short: the report retries at once.
var approvalWake = make(chan struct{}, 1)

func wakeApprovalReport() {
	select {
	case approvalWake <- struct{}{}:
	default:
	}
}

// approvalKV is where a 409 is saved; set by Init.
var approvalKV atomic.Pointer[Settings]

func setApprovalReportKV(kv Settings) {
	if kv == nil {
		approvalKV.Store(nil)
		return
	}
	approvalKV.Store(&kv)
}

func saveApprovalConflict(ctx context.Context, on bool) {
	p := approvalKV.Load()
	if p == nil {
		return
	}
	kv := *p
	v := ""
	if on {
		v = time.Now().UTC().Format(time.RFC3339)
	} else if cur, _, _ := kv.Get(ctx, KeyApprovalKeyConflictAt); cur == "" {
		return
	}
	if err := kv.Set(ctx, KeyApprovalKeyConflictAt, v); err != nil {
		logging.L().Debugf("approval key: save conflict state: %v", err)
	}
}

type reportOutcome int

const (
	reportDone    reportOutcome = iota // the cloud took the key (or already had it)
	reportIgnored                      // an answer that retrying will not change
	reportRetry                        // could not reach the cloud, or it failed
	reportSkipped                      // nothing to report to yet (not paired / no key)
)

var approvalReport struct {
	sync.Mutex
	done     map[string]bool // identity keys that reached a final answer
	inflight map[string]bool
}

// approvalBG tracks every report goroutine, including those kicked with no
// WaitGroup (Recover's heal, pair, move), so a test can stop and wait for them
// before its t.TempDir is removed (ut-docs#7153). Production never cancels
// stop, so it changes nothing there. Add and the stop swap share the lock, so a
// kick racing a reset is either stopped (perhaps before its first attempt) or
// sees the new context. A reset must not overlap a kick (WaitGroup reuse) and
// must never run inside a report goroutine (it would wait on itself).
var approvalBG struct {
	sync.Mutex
	wg   sync.WaitGroup
	stop context.Context // lazily made; cancelled only by a test's reset
	end  context.CancelFunc
}

// approvalBGAdd registers one report goroutine and returns the stop context it
// must honour.
func approvalBGAdd() context.Context {
	approvalBG.Lock()
	defer approvalBG.Unlock()
	if approvalBG.stop == nil {
		approvalBG.stop, approvalBG.end = context.WithCancel(context.Background())
	}
	approvalBG.wg.Add(1)
	return approvalBG.stop
}

// approvalConflict: the cloud answered 409, a different key is pinned.
var approvalConflict atomic.Bool

// approvalIdentity resolves the device, store and bearer to report with.
func approvalIdentity(m config.MarketplaceConfig) (deviceID, storeID, token string) {
	eff := Effective(&config.Config{Marketplace: m}).Marketplace
	return eff.DeviceID, eff.StoreID, eff.MerchantToken
}

// reportApprovalKeyOnce makes one attempt.
func reportApprovalKeyOnce(ctx context.Context, m config.MarketplaceConfig) reportOutcome {
	log := logging.L()
	deviceID, storeID, token := approvalIdentity(m)
	if strings.TrimSpace(m.EndpointURL) == "" || deviceID == "" || storeID == "" || token == "" {
		return reportSkipped
	}
	rep := approvalkey.Shared().Report(deviceID, storeID)
	if rep == nil {
		return reportSkipped // no usable key: already logged
	}
	payload, err := json.Marshal(map[string]any{"store_id": storeID, "approval_key": rep})
	if err != nil {
		return reportSkipped
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/devices/approval-key"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return reportSkipped
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Debugf("approval key report: cloud not reached: %v", err)
		return reportRetry
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		log.Debugf("approval key reported (kid %s)", rep["kid"])
		approvalConflict.Store(false)
		saveApprovalConflict(ctx, false)
		return reportDone
	case resp.StatusCode == http.StatusUnauthorized:
		// A refused credential: tell the heal engine (an envelope code only;
		// a proxy's bare 401 is not evidence) and keep retrying: the report
		// works again once the till has healed.
		var env struct {
			Data struct {
				Reason  string `json:"reason"`
				Recover bool   `json:"recover"`
			} `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
			reportDeadCredential("approval_key", env.Error.Code, env.Data.Reason, env.Data.Recover)
		} else {
			log.Debugf("approval key report refused (HTTP 401 without an error code)")
		}
		return reportRetry
	case resp.StatusCode == http.StatusConflict:
		saveApprovalConflict(ctx, true)
		if approvalConflict.CompareAndSwap(false, true) {
			log.Infof("approval key: the cloud has a different key pinned for this till (kid %s); moving a till needs a support reset", rep["kid"])
		}
		return reportIgnored
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		log.Debugf("approval key report not taken (HTTP %d); ignored", resp.StatusCode)
		return reportIgnored
	default:
		log.Debugf("approval key report failed (HTTP %d)", resp.StatusCode)
		return reportRetry
	}
}

// kickApprovalKeyReportUnlessStopped is the once-per-start kick: a shop that
// switched "Send usage facts" off, on a till whose cloud identity was made
// automatically, makes no start-up POST (ut-docs#6618). Operator-driven
// callers (claim, pair, move) use kickApprovalKeyReport directly.
func kickApprovalKeyReportUnlessStopped(ctx context.Context, kv Settings, m config.MarketplaceConfig, wg *sync.WaitGroup) {
	if Stopped(ctx, kv) && AutoRegistered(ctx, kv) {
		return
	}
	kickApprovalKeyReport(ctx, m, wg)
}

// kickApprovalKeyReport starts the background report unless this identity
// (device + store) already got a final answer or one is already running for
// it. wg, when given, tracks the goroutine for shutdown. ctx must outlive the
// caller's request (pass context.WithoutCancel for a request context).
func kickApprovalKeyReport(ctx context.Context, m config.MarketplaceConfig, wg *sync.WaitGroup) {
	deviceID, storeID, token := approvalIdentity(m)
	if strings.TrimSpace(m.EndpointURL) == "" || deviceID == "" || storeID == "" || token == "" {
		return
	}
	key := deviceID + "|" + storeID
	approvalReport.Lock()
	if approvalReport.done[key] || approvalReport.inflight[key] {
		approvalReport.Unlock()
		return
	}
	if approvalReport.inflight == nil {
		approvalReport.inflight = map[string]bool{}
	}
	approvalReport.inflight[key] = true
	delays, tail := approvalRetryDelays, approvalRetryTail // the slice is never mutated in place, only replaced
	approvalReport.Unlock()
	if wg != nil {
		wg.Add(1)
	}
	stop := approvalBGAdd()
	go func() {
		defer approvalBG.wg.Done()
		defer logging.RecoverAndLog("enroll.approvalKeyReport")
		if wg != nil {
			defer wg.Done()
		}
		defer func() {
			approvalReport.Lock()
			delete(approvalReport.inflight, key)
			approvalReport.Unlock()
		}()
		// An in-flight attempt ends with stop too (cancelled context), so a
		// reset never waits out the 15 s request timeout.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(stop, cancel)()
		for attempt := 0; ; attempt++ {
			if stop.Err() != nil {
				return
			}
			out := reportApprovalKeyOnce(ctx, m)
			if out != reportRetry {
				if out != reportSkipped {
					approvalReport.Lock()
					if approvalReport.done == nil {
						approvalReport.done = map[string]bool{}
					}
					approvalReport.done[key] = true
					approvalReport.Unlock()
				}
				return
			}
			wait := tail
			if attempt < len(delays) {
				wait = delays[attempt]
			}
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done(): // the caller's, or stop (via cancel)
				t.Stop()
				return
			case <-approvalWake:
				t.Stop()
			case <-t.C:
			}
		}
	}()
}
