package cloudsync

// ADR-0117 §3 (ut-docs#2827): the conditional check-in that makes the
// 2-minute loop cheap. Before the full /v1/stores/sync POST the till asks
//
//	GET /v1/stores/checkin?store_id=… with If-None-Match: "<store_id>:<link_version>"
//
// (same base URL and device bearer as the POST). The cloud bumps a
// per-store link_version with every change a till must pick up, so:
//
//   - 200 (a newer version, or the till doesn't know one yet) → POST now;
//     the version is only taken as processed once that POST succeeds.
//   - 304 → skip the POST unless the till's own state (the device/health
//     body the POST would send) changed since the last successful POST,
//     or checkinFloor passed since it. A 304 is an authenticated "nothing
//     changed", so it also confirms a cached entitlement (ADR-0060 §3 as
//     amended by ADR-0117 §3).
//   - 401 (revoked credential) fails the tick exactly like the POST's 401
//     — never masked by a fallback POST. 429/503 and transport errors fail
//     it too, so the scheduler's backoff and Retry-After apply unchanged.
//   - any other answer (404/405 from a cloud that predates the endpoint, a
//     5xx, an unreadable 200) → POST as before, and don't ask again for
//     checkinRetryOld, so an old cloud isn't asked twice every tick.
//
// A newer link_version from the cloud link (hello/nudge, Hooks.LinkVersion)
// forces the POST without asking: sending it as If-None-Match would get a
// 304 against the very change the nudge announced.
//
// The state is in memory: a restarted till simply starts with a 200 and a
// full POST. Everything here runs on Start's goroutine, never on the sale
// path (ADR-0003).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
)

const (
	// checkinFloor: a 304 still runs the full POST this long after the
	// last successful one, so health, uptime and directive results keep
	// flowing (ADR-0117 §3's 10-minute floor).
	checkinFloor = 10 * time.Minute
	// checkinRetryOld: after a GET the cloud didn't answer usefully, POST
	// every tick and ask again only this much later.
	checkinRetryOld = time.Hour
	// checkinMaxBody bounds the 200 body read: it is one small JSON object.
	checkinMaxBody = 64 << 10
)

// checkinNow is the check-in's clock; tests pin it.
var checkinNow = time.Now

// checkinState is what the check-in remembers between ticks. key is the
// endpoint + store it belongs to: a re-enrolment to another store or cloud
// starts over.
type checkinState struct {
	key         string
	known       bool  // version is a link_version this till processed
	version     int64 // … by a successful POST
	lastPostAt  time.Time
	lastPostSum [sha256.Size]byte
	oldUntil    time.Time // no GET before this (old cloud / unusable answer)
}

var (
	checkinMu sync.Mutex
	checkin   checkinState // guarded by checkinMu
)

// checkinPlan is one tick's decision, applied by done after the POST.
type checkinPlan struct {
	post    bool
	sum     [sha256.Size]byte
	version int64 // the version the POST will have processed (valid if hasVer)
	hasVer  bool
}

// planCheckin decides whether this tick needs the full POST. stateSum is
// the hash of the till-state part of the POST body (stateHash). A non-nil
// error is the tick's failure (401, 429/503, transport).
// hashFailed (the body didn't marshal) means the state can't be compared,
// so a 304 still POSTs.
func planCheckin(ctx context.Context, cfg *config.Config, settings *data.SettingsRepo, stateSum [sha256.Size]byte, hashFailed bool, linkVersion func() int64) (checkinPlan, error) {
	if settings != nil {
		enroll.RetryUnsavedCredential(ctx, settings)
	}
	m := enroll.Effective(cfg).Marketplace
	key := strings.TrimRight(m.EndpointURL, "/") + "|" + m.StoreID
	now := checkinNow()
	plan := checkinPlan{post: true, sum: stateSum}

	checkinMu.Lock()
	if checkin.key != key {
		checkin = checkinState{key: key}
	}
	known, version, oldUntil := checkin.known, checkin.version, checkin.oldUntil
	lastPostAt, lastSum := checkin.lastPostAt, checkin.lastPostSum
	checkinMu.Unlock()

	if linkVersion != nil {
		if hint := linkVersion(); hint > 0 && (!known || hint > version) {
			plan.version, plan.hasVer = hint, true
			return plan, nil // the link announced a change: POST, don't ask
		}
	}
	if now.Before(oldUntil) {
		return plan, nil
	}

	status, newVersion, err := getCheckin(ctx, m.EndpointURL, m.StoreID, m.MerchantToken, known, version)
	switch {
	case err != nil:
		return plan, err
	case status == http.StatusNotModified:
		confirmEntitlement(ctx, settings, now)
		if hashFailed || stateSum != lastSum || lastPostAt.IsZero() || now.Sub(lastPostAt) >= checkinFloor {
			return plan, nil
		}
		plan.post = false
		return plan, nil
	case status == http.StatusOK:
		plan.version, plan.hasVer = newVersion, true
		return plan, nil
	default:
		checkinMu.Lock()
		if checkin.key == key {
			checkin.oldUntil = now.Add(checkinRetryOld)
		}
		checkinMu.Unlock()
		logging.L().Infof("cloudsync: check-in GET answered %d; posting every tick, asking again in %s", status, checkinRetryOld)
		return plan, nil
	}
}

// done records a successful POST (called only after pushSync succeeded).
func (p checkinPlan) done(cfg *config.Config) {
	m := enroll.Effective(cfg).Marketplace
	key := strings.TrimRight(m.EndpointURL, "/") + "|" + m.StoreID
	checkinMu.Lock()
	defer checkinMu.Unlock()
	if checkin.key != key {
		return
	}
	checkin.lastPostAt = checkinNow()
	checkin.lastPostSum = p.sum
	if p.hasVer {
		checkin.known, checkin.version = true, p.version
	}
}

// getCheckin runs the conditional GET. It returns the status for
// 200/304/unusable answers, and an error for the answers that must fail the
// tick as the POST's would: 401, 429, 503 (statusError, so the scheduler
// sees Retry-After) and transport errors. A 200 without a readable
// link_version comes back as status 0 (treated like an old cloud).
func getCheckin(ctx context.Context, endpoint, storeID, token string, known bool, version int64) (status int, linkVersion int64, err error) {
	const path = "/v1/stores/checkin"
	u := strings.TrimRight(endpoint, "/") + path + "?store_id=" + url.QueryEscape(storeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if known {
		req.Header.Set("If-None-Match", `"`+storeID+":"+strconv.FormatInt(version, 10)+`"`)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Data struct {
				LinkVersion *int64 `json:"link_version"`
			} `json:"data"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, checkinMaxBody)).Decode(&body) != nil || body.Data.LinkVersion == nil {
			drainBody(resp)
			return 0, 0, nil
		}
		drainBody(resp)
		return http.StatusOK, *body.Data.LinkVersion, nil
	case http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		drainBody(resp)
		return resp.StatusCode, 0, &statusError{
			Path:       path,
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	default:
		drainBody(resp)
		return resp.StatusCode, 0, nil
	}
}

// confirmEntitlement is a 304's ADR-0060 §3 confirmation: the cached
// entitlement's last_confirmed_at moves to now, exactly as a successful
// POST's block would move it. A till that never cached a block gets
// nothing invented. Best-effort, like cacheEntitlement.
func confirmEntitlement(ctx context.Context, settings *data.SettingsRepo, now time.Time) {
	v, ok, err := settings.Get(ctx, entitlement.KeyLastConfirmedAt)
	if err != nil || !ok || strings.TrimSpace(v) == "" {
		return
	}
	if err := settings.Set(ctx, entitlement.KeyLastConfirmedAt, now.UTC().Format(time.RFC3339)); err != nil {
		logging.L().Warnf("cloudsync: entitlement confirmation not recorded (will retry next tick): %v", err)
	}
}

// stateHash hashes the till-state part of the sync body (ADR-0117 §3's
// "till state hash"): the canonical JSON of the device record(s) the POST
// would send, minus health.uptime_min — it moves every minute and would
// defeat every 304; the 10-minute floor carries it instead. encoding/json
// sorts map keys, so equal state hashes equal.
func stateHash(devices []map[string]any) ([sha256.Size]byte, error) {
	cp := make([]map[string]any, len(devices))
	for i, d := range devices {
		c := make(map[string]any, len(d))
		for k, v := range d {
			c[k] = v
		}
		if h, ok := d["health"].(map[string]any); ok {
			hc := make(map[string]any, len(h))
			for k, v := range h {
				if k != "uptime_min" {
					hc[k] = v
				}
			}
			c["health"] = hc
		}
		cp[i] = c
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		return [sha256.Size]byte{}, errors.New("cloudsync: state hash: " + err.Error())
	}
	return sha256.Sum256(raw), nil
}
