package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// Redemption of a replica's own cloud credential (ADR-0116 D3,
// ut-docs#2769).
//
// When the main till vouches for a replica whose device has no credential
// yet, the cloud's devices/register answer carries a one-time redeem code
// (single use, 10 minutes, bound to store + device, stored hashed). The main
// till relays only that code (Vouch.RedeemCode); the replica itself posts it
// to POST /v1/stores/devices/redeem — unauthenticated, the code is the only
// credential — directly to the cloud over TLS, and keeps the device token
// the cloud returns exactly once, through the same adoption path as a D4
// rotation (adoptOwnCredential). Neither the code nor the token is logged,
// in any branch below.

// validRedeemCode bounds the redeem code the main till relays and the
// replica sends: today's cloud mints 64 hex characters and its /redeem
// refuses a code over 128; the printable token shape of a rotated
// credential, up to that cap, is tolerated.
func validRedeemCode(c string) bool { return len(c) <= maxRedeemCode && validRotatedToken(c) }

const maxRedeemCode = 128

// redeemPinWarned: "not redeemed because the environment pins the token" is
// logged once per process, not on every vouch.
var redeemPinWarned atomic.Bool

// errRedeemAnswerRejected: the cloud answered 200 but not for this device
// and store, or with a malformed token — anomalous, so it is warned about.
var errRedeemAnswerRejected = errors.New("redeem answer rejected")

// redeemErrCodeRe bounds a cloud error code before it is logged.
var redeemErrCodeRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// redeemError is a non-200 answer from /redeem: its status and the cloud's
// error.code only (the message is never logged).
type redeemError struct {
	status int
	code   string
}

func (e *redeemError) Error() string {
	return fmt.Sprintf("cloud answered %d %s", e.status, e.code)
}

// redeemOwnCredential exchanges v.RedeemCode for this replica's own cloud
// credential and adopts it. Best-effort: every refusal or failure is logged
// (status and error code only) and the replica keeps its current
// credential; the next vouch brings a fresh code only while the device
// still has none. deviceID is this till's own device id, already checked
// against v.DeviceID by the caller. It reports whether the failure was
// transient (network, 429, 5xx), so the caller re-vouches on its short
// backoff for a fresh code instead of waiting the 6-hour interval.
func redeemOwnCredential(ctx context.Context, m config.MarketplaceConfig, kv Settings, v Vouch, deviceID string) (retry bool) {
	if v.RedeemCode == "" {
		return false
	}
	// ut-docs#2753's rule: a vouch answer landing after POST
	// /api/sync/promote is stale — a main till does not redeem it.
	if !isReplica(ctx, kv) {
		return false
	}
	log := logging.L()
	mu.RLock()
	pinned, explicitStore, inUse := tokenExplicit, storeIDExplicit, cur.StoreID
	mu.RUnlock()
	switch {
	case pinned:
		if redeemPinWarned.CompareAndSwap(false, true) {
			log.Warnf("enrolment: the main till relayed a code for this replica's own cloud credential, but UT_MARKETPLACE_MERCHANT_TOKEN pins the token, so it was not redeemed; unset it before the shop's shared token is retired (ADR-0116 D3)")
		}
		return false
	case deviceID == "" || v.StoreID == "" || strings.TrimSpace(m.EndpointURL) == "":
		log.Infof("enrolment: not redeeming this replica's cloud credential: no store, device or marketplace endpoint yet")
		return false
	case !validRedeemCode(v.RedeemCode):
		log.Warnf("enrolment: ignored a malformed redeem code from the main till (length %d, ADR-0116 D3)", len(v.RedeemCode))
		return false
	}
	// The replica's own synced store id must agree with the store the main
	// till vouched under; otherwise the code would bind this till to
	// another shop's store.
	own, _, _ := kv.Get(ctx, keyStoreID)
	own = strings.TrimSpace(own)
	if explicitStore {
		own = strings.TrimSpace(m.StoreID)
	}
	// Not synced yet: the store an earlier redemption put in use counts.
	if own == "" {
		own = strings.TrimSpace(inUse)
	}
	if own != "" && own != v.StoreID {
		log.Warnf("enrolment: the main till vouched for this replica under store %q, but this till belongs to store %q; not redeeming (ADR-0116 D3)", v.StoreID, own)
		return false
	}
	token, err := postRedeem(ctx, m.EndpointURL, v.StoreID, deviceID, v.RedeemCode)
	if err != nil {
		var re *redeemError
		switch {
		case errors.Is(err, errRedeemAnswerRejected),
			errors.As(err, &re) && (re.status == http.StatusForbidden || re.code == "already_redeemed" || re.code == "device_revoked"):
			// A used or unknown code can mean someone else redeemed it
			// (ADR-0116 D3's residuals): the owner sees issued_by on the
			// Tills page.
			log.Warnf("enrolment: redeeming this replica's own cloud credential was refused: %v; keeping the current credential (ADR-0116 D3)", err)
			return false
		case errors.As(err, &re) && re.code == "device_exists":
			// The device already holds a credential: no new code will come.
			log.Infof("enrolment: this replica's device already holds a cloud credential: %v (ADR-0116 D3)", err)
			return false
		default:
			log.Infof("enrolment: redeeming this replica's own cloud credential failed (will retry with a fresh code soon): %v", err)
			return true
		}
	}
	mu.RLock()
	current := cur.Token
	mu.RUnlock()
	if token == current {
		return false
	}
	if adoptOwnCredential(ctx, kv, token, v.StoreID) {
		log.Infof("enrolment: this replica now uses its own cloud credential (device %s, ADR-0116 D3)", deviceID)
	}
	return false
}

// postRedeem calls POST /v1/stores/devices/redeem and returns the device
// token, accepted only when the answer names this device and store and the
// token is well-formed. No Authorization header: the code is the
// credential. Errors never carry the code or the token.
func postRedeem(ctx context.Context, endpoint, storeID, deviceID, code string) (string, error) {
	payload, err := json.Marshal(map[string]string{"store_id": storeID, "device_id": deviceID, "code": code})
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/stores/devices/redeem"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("redeem request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		var fail struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &fail)
		errCode := fail.Error.Code
		if !redeemErrCodeRe.MatchString(errCode) {
			errCode = "(no error code)"
		}
		return "", &redeemError{status: resp.StatusCode, code: errCode}
	}
	var ok struct {
		Data struct {
			StoreID     string `json:"store_id"`
			DeviceID    string `json:"device_id"`
			DeviceToken string `json:"device_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		return "", fmt.Errorf("%w: not valid JSON", errRedeemAnswerRejected)
	}
	switch {
	case ok.Data.DeviceID != deviceID:
		return "", fmt.Errorf("%w: it names another device", errRedeemAnswerRejected)
	case ok.Data.StoreID != storeID:
		return "", fmt.Errorf("%w: it names another store", errRedeemAnswerRejected)
	case !validRotatedToken(ok.Data.DeviceToken):
		return "", fmt.Errorf("%w: malformed credential (length %d)", errRedeemAnswerRejected, len(ok.Data.DeviceToken))
	}
	return ok.Data.DeviceToken, nil
}
