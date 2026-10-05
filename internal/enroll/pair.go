package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// Pairing a till with a shop (ADR-0116 D5/D6, ut-docs#3523).
//
// The owner's my. "Add or re-pair a till" mints a short, single-use pairing
// code bound to the store. On the till, Settings → Cloud → "Pair with a
// shop" (Pair) clears exactly this till's cloud identity — the credential
// and device keys of db.TillCloudIdentityPrefixes (its own wipe list below;
// the unpaid check-in markers stay, and Pair's own check-in re-records
// them), plus store_id / merchant_id on a main
// or standalone till; never the pinned signing key (ADR-0006) or the
// store-level preferences — mints a fresh device id and posts the code to
// POST /v1/stores/pair — unauthenticated, the code is the only credential —
// directly to the cloud over TLS. The cloud answers with store_id,
// merchant_id and this device's own credential, adopted through the same
// path as a D4 rotation (adoptOwnCredential). A replica, whose store
// identity comes from its main till's admin bundle, refuses an answer for
// any other store. Neither the code nor the token is logged, in any branch
// below.

// ErrPairStoreMismatch: on a replica, the pairing code belongs to a store
// other than its main till's. Exported so the Settings handler can show
// its own message instead of the generic failure.
var ErrPairStoreMismatch = errors.New("the pairing code belongs to a different shop than this till's main till")

// ErrPairReplicaNoStore: a replica that has not synced its store from its
// main till yet cannot check the answer's store, so it does not pair (and
// does not burn the code). Exported like ErrPairStoreMismatch, so the
// Settings handler shows a translated message for it.
var ErrPairReplicaNoStore = errors.New("this till has not received its shop from its main till yet; sync with the main till before pairing")

// errPairAnswerRejected: the cloud answered 200 but without a store, a
// merchant or a well-formed token.
var errPairAnswerRejected = errors.New("pair answer rejected")

// Pair pairs this till with the shop the owner's pairing code belongs to
// and returns the resulting status. Synchronous and bounded by ctx; the
// till keeps selling offline whatever the outcome.
func Pair(ctx context.Context, cfg *config.Config, kv Settings, code string) (Status, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxRedeemCode {
		return CurrentStatus(), fmt.Errorf("enter the pairing code from the shop's cloud account")
	}
	m := Effective(cfg).Marketplace
	if strings.TrimSpace(m.EndpointURL) == "" {
		return CurrentStatus(), fmt.Errorf("marketplace endpoint is not configured")
	}
	replica := isReplica(ctx, kv)
	mu.RLock()
	// UT_MARKETPLACE_STORE_ID pins the store on a main or standalone till:
	// Effective keeps the pinned store over the paired one, so adopting the
	// paired token would split the identity. On a replica the pinned store
	// is instead the store the answer must match (as in redeem), which keeps
	// the identity whole, so it is not a refusal there.
	pinned := tokenExplicit || explicitConfigured || deviceIDExplicit || (storeIDExplicit && !replica)
	inUse := cur.StoreID
	explicitStore := storeIDExplicit
	mu.RUnlock()
	if pinned {
		// The environment's identity would win over the paired one
		// (Effective), so pairing would only burn the single-use code.
		return CurrentStatus(), fmt.Errorf("this till's cloud identity is set in the environment (UT_MARKETPLACE_*); remove it before pairing")
	}
	// Serialized with registration (RegisterNow and the background loop),
	// so nothing registers the old device id mid-pair.
	if !acquireAttempt(ctx) {
		return CurrentStatus(), fmt.Errorf("pairing did not start before the caller's deadline (slot held by another attempt): %w", ctx.Err())
	}
	defer releaseAttempt()

	log := logging.L()
	// The store this till actually belongs to, read before any write. On a
	// replica it is kept current by the admin sync from its main till and
	// is never cleared here.
	own, _, _ := kv.Get(ctx, keyStoreID)
	own = strings.TrimSpace(own)
	if explicitStore {
		own = strings.TrimSpace(m.StoreID)
	}
	if own == "" {
		own = strings.TrimSpace(inUse)
	}
	if replica && own == "" {
		// No answer could match a store this replica does not know yet, so
		// refuse before clearing anything or burning the single-use code.
		log.Warnf("enrolment: this replica has not received its store from its main till yet; not pairing (ADR-0116 D5)")
		return CurrentStatus(), ErrPairReplicaNoStore
	}

	// 1. Clear exactly this till's cloud identity (ADR-0116 D5). The
	// credential being replaced carries nothing worth keeping: this flow
	// runs on a till that is unpaired, revoked or retired, so a refused
	// code below leaves it cleanly unregistered rather than half-old.
	wipe := []string{keyToken, keyEnrolledAt, keyDeviceRegistered, keyDeviceTillID}
	if !replica {
		wipe = append(wipe, keyStoreID, keyMerchantID)
	}
	for _, key := range wipe {
		if err := kv.Set(ctx, key, ""); err != nil {
			log.Warnf("enrolment: pair: clear %s: %v", key, err)
		}
	}
	// 2. A fresh device id: re-pairing is always as a new device.
	newID := "till-" + uuid.NewString()
	if err := kv.Set(ctx, keyDeviceID, newID); err != nil {
		log.Warnf("enrolment: pair: persist device_id: %v", err)
	}
	if replica {
		// Marks the new id as minted for this till, so the next boot's
		// copied-identity repair (repairCopiedIdentity) keeps it.
		if tid, _, _ := kv.Get(ctx, keySyncTillID); strings.TrimSpace(tid) != "" {
			if err := kv.Set(ctx, keyDeviceTillID, strings.TrimSpace(tid)); err != nil {
				log.Warnf("enrolment: pair: persist %s: %v", keyDeviceTillID, err)
			}
		}
	}
	mu.Lock()
	cur.DeviceID = newID
	cur.Token = ""
	vouchedDevice = ""
	identityReplaced = true
	if !replica {
		cur.StoreID = ""
		cur.MerchantID = ""
		displayStoreID = ""
	}
	mu.Unlock()
	unsavedToken.Store(false)

	// 3. Pair.
	storeID, merchantID, token, err := postPair(ctx, m.EndpointURL, code, newID, DeviceName(ctx, kv), buildinfo.Version)
	if err != nil {
		log.Warnf("enrolment: pairing this till with a shop failed: %v (ADR-0116 D5)", err)
		return CurrentStatus(), err
	}
	if replica && storeID != own {
		log.Warnf("enrolment: the pairing code belongs to store %q, but this replica's main till is in store %q; not adopting it (ADR-0116 D5)", storeID, own)
		return CurrentStatus(), ErrPairStoreMismatch
	}
	adoptOwnCredential(ctx, kv, token, storeID)
	persist := [][2]string{
		{keyEnrolledAt, time.Now().UTC().Format(time.RFC3339)},
		{keyDeviceRegistered, newID}, // /pair recorded this device under the store
	}
	if !replica {
		persist = append(persist, [2]string{keyStoreID, storeID}, [2]string{keyMerchantID, merchantID})
	}
	for _, kvp := range persist {
		if err := kv.Set(ctx, kvp[0], kvp[1]); err != nil {
			log.Warnf("enrolment: pair: persist %s: %v", kvp[0], err)
		}
	}
	if !replica {
		mu.Lock()
		cur.StoreID = storeID
		cur.MerchantID = merchantID
		displayStoreID = storeID
		mu.Unlock()
	}
	log.Infof("enrolment: this till is now paired with store %s as device %s (ADR-0116 D5)", storeID, newID)
	return CurrentStatus(), nil
}

// postPair calls POST /v1/stores/pair and returns the store, merchant and
// device token the cloud answers with, accepted only when all three are
// present and the token is well-formed. No Authorization header: the code
// is the credential. Errors never carry the code or the token.
func postPair(ctx context.Context, endpoint, code, deviceID, deviceName, version string) (storeID, merchantID, token string, err error) {
	payload, err := json.Marshal(map[string]string{"code": code, "device_id": deviceID, "device_name": deviceName, "version": version})
	if err != nil {
		return "", "", "", err
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/stores/pair"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("pair request: %w", err)
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
		return "", "", "", &redeemError{status: resp.StatusCode, code: errCode}
	}
	var ok struct {
		Data struct {
			StoreID    string `json:"store_id"`
			MerchantID string `json:"merchant_id"`
			Token      string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		return "", "", "", fmt.Errorf("%w: not valid JSON", errPairAnswerRejected)
	}
	d := ok.Data
	switch {
	case strings.TrimSpace(d.StoreID) == "":
		return "", "", "", fmt.Errorf("%w: no store", errPairAnswerRejected)
	case strings.TrimSpace(d.MerchantID) == "":
		return "", "", "", fmt.Errorf("%w: no merchant", errPairAnswerRejected)
	case !validRotatedToken(d.Token):
		return "", "", "", fmt.Errorf("%w: malformed credential (length %d)", errPairAnswerRejected, len(d.Token))
	}
	return d.StoreID, d.MerchantID, d.Token, nil
}
