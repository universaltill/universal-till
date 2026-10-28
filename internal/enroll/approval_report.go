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
// (network down, 5xx): slow, and then it gives up until the next start/pair.
var approvalRetryDelays = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour}

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

// approvalConflict: the cloud answered 409, a different key is pinned.
var approvalConflict atomic.Bool

// ApprovalKeyConflict reports whether the cloud said a different approval key
// is already pinned for this till (moves then need a support reset). In
// memory only; it is for the future Settings > Cloud text.
func ApprovalKeyConflict() bool { return approvalConflict.Load() }

func resetApprovalReport() {
	approvalReport.Lock()
	approvalReport.done, approvalReport.inflight = nil, nil
	approvalReport.Unlock()
	approvalConflict.Store(false)
}

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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		log.Debugf("approval key reported (kid %s)", rep["kid"])
		return reportDone
	case resp.StatusCode == http.StatusConflict:
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
	approvalReport.Unlock()
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		defer logging.RecoverAndLog("enroll.approvalKeyReport")
		if wg != nil {
			defer wg.Done()
		}
		defer func() {
			approvalReport.Lock()
			delete(approvalReport.inflight, key)
			approvalReport.Unlock()
		}()
		for attempt := 0; ; attempt++ {
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
			if attempt >= len(approvalRetryDelays) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(approvalRetryDelays[attempt]):
			}
		}
	}()
}
