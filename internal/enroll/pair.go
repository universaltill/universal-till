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

// ErrPairIdentityPinned: UT_MARKETPLACE_* pins this till's cloud identity, so
// a paired one would only burn the single-use code (Effective keeps the
// pinned one). Exported so the setup wizard can show its own message.
var ErrPairIdentityPinned = errors.New("this till's cloud identity is set in the environment (UT_MARKETPLACE_*); remove it before pairing")

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
		return CurrentStatus(), ErrNotConfigured
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
		return CurrentStatus(), ErrPairIdentityPinned
	}
	// Serialized with registration (RegisterNow and the background loop),
	// so nothing registers the old device id mid-pair.
	if !acquireAttempt(ctx) {
		// acquireAttempt only fails via ctx.Done, so ctx.Err() is non-nil.
		return CurrentStatus(), fmt.Errorf("%w: %w", ErrAttemptBusy, ctx.Err())
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

	// 1+2. Clear exactly this till's cloud identity and take a fresh device
	// id (ADR-0116 D5).
	newID := "till-" + uuid.NewString()
	resetIdentityForPair(ctx, kv, replica, newID)

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
	adoptPaired(ctx, kv, replica, newID, storeID, merchantID, token)
	log.Infof("enrolment: this till is now paired with store %s as device %s (ADR-0116 D5)", storeID, newID)
	return CurrentStatus(), nil
}

// resetIdentityForPair clears exactly this till's cloud identity (ADR-0116
// D5) and gives it the fresh device id newID. The credential being
// replaced carries nothing worth keeping: this flow runs on a till that is
// unpaired, revoked or retired, so a refused code leaves it cleanly
// unregistered rather than half-old. newID is the fresh device id. Caller
// holds the attempt slot.
func resetIdentityForPair(ctx context.Context, kv Settings, replica bool, newID string) {
	log := logging.L()
	wipe := []string{keyToken, keyEnrolledAt, keyDeviceRegistered, keyDeviceTillID}
	if !replica {
		wipe = append(wipe, keyStoreID, keyMerchantID)
	}
	for _, key := range wipe {
		if err := kv.Set(ctx, key, ""); err != nil {
			log.Warnf("enrolment: pair: clear %s: %v", key, err)
		}
	}
	// A fresh device id: re-pairing is always as a new device.
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
}

// adoptPaired adopts and persists the credential a pairing answered with,
// the same way for Settings pairing and the wizard's final post.
func adoptPaired(ctx context.Context, kv Settings, replica bool, newID, storeID, merchantID, token string) {
	log := logging.L()
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
}

// postPair calls POST /v1/stores/pair and returns the store, merchant and
// device token the cloud answers with, accepted only when all three are
// present and the token is well-formed. No Authorization header: the code
// is the credential. Errors never carry the code or the token.
func postPair(ctx context.Context, endpoint, code, deviceID, deviceName, version string) (storeID, merchantID, token string, err error) {
	a, err := postPairFull(ctx, endpoint, code, deviceID, deviceName, version, false)
	return a.storeID, a.merchantID, a.token, err
}

// pairAnswer is a successful /pair answer: the credential plus, from a
// cloud that knows ADR-0166, the store profile block (nil otherwise).
type pairAnswer struct {
	storeID, merchantID, token string
	store                      *PairProfile
}

// PairProfile is the cloud's data.store block (ADR-0166 §3). Empty strings
// and a nil CurrencyDecimals mean "the cloud has none": the wizard then
// asks locally.
type PairProfile struct {
	Name             string `json:"name"`
	Country          string `json:"country"`
	Currency         string `json:"currency"`
	CurrencyDecimals *int   `json:"currency_decimals"`
	CatalogMaster    string `json:"catalog_master"`
}

// StoreHasTillError: a first_till pairing for a shop that already has a
// till that has talked to the cloud (409 store_has_till). The code stays
// usable. Name and LastUsedAt (RFC 3339) describe that till.
type StoreHasTillError struct {
	Name       string
	LastUsedAt string
}

func (e *StoreHasTillError) Error() string { return "pair: the shop already has a till" }

// postPairFull is postPair with the optional first_till flag and the whole
// answer.
func postPairFull(ctx context.Context, endpoint, code, deviceID, deviceName, version string, firstTill bool) (pairAnswer, error) {
	none := pairAnswer{}
	body := map[string]any{"code": code, "device_id": deviceID, "device_name": deviceName, "version": version}
	if firstTill {
		body["first_till"] = true
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return none, err
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/stores/pair"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return none, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return none, fmt.Errorf("pair request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		var fail struct {
			Data struct {
				ExistingTill struct {
					Name       string `json:"name"`
					LastUsedAt string `json:"last_used_at"`
				} `json:"existing_till"`
			} `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &fail)
		// Typed like Register now / claim (ut-docs#4058) so the Settings
		// handler can branch on Status/Code. Only the sanitised code is
		// kept: the raw body (which could echo the pairing code or a
		// token) never enters the error.
		errCode := fail.Error.Code
		msg := errCode
		if !redeemErrCodeRe.MatchString(errCode) {
			errCode, msg = "", "(no error code)"
		}
		if firstTill && resp.StatusCode == http.StatusConflict && errCode == "store_has_till" {
			et := fail.Data.ExistingTill
			return none, &StoreHasTillError{Name: clampText(et.Name, 80), LastUsedAt: clampText(et.LastUsedAt, 40)}
		}
		return none, &RegisterHTTPError{Op: "pair", Status: resp.StatusCode, Code: errCode, Body: msg}
	}
	var ok struct {
		Data struct {
			StoreID     string       `json:"store_id"`
			MerchantID  string       `json:"merchant_id"`
			DeviceToken string       `json:"device_token"`
			Store       *PairProfile `json:"store"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		return none, fmt.Errorf("%w: not valid JSON", errPairAnswerRejected)
	}
	d := ok.Data
	switch {
	case strings.TrimSpace(d.StoreID) == "":
		return none, fmt.Errorf("%w: no store", errPairAnswerRejected)
	case strings.TrimSpace(d.MerchantID) == "":
		return none, fmt.Errorf("%w: no merchant", errPairAnswerRejected)
	case !validRotatedToken(d.DeviceToken):
		return none, fmt.Errorf("%w: malformed credential (length %d)", errPairAnswerRejected, len(d.DeviceToken))
	}
	return pairAnswer{storeID: d.StoreID, merchantID: d.MerchantID, token: d.DeviceToken, store: d.Store}, nil
}

// clampText bounds a cloud-supplied string that is later shown to the
// operator.
func clampText(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// FirstTillPairing is a pairing made by the setup wizard (ADR-0166 §2, §4):
// held in memory by the caller and persisted only by Commit, so no check-in
// or sync can run on a wizard that never finished. The token is a secret:
// never log or render it. Store is nil for a cloud that sent no profile.
type FirstTillPairing struct {
	DeviceID, Token, StoreID, MerchantID string
	Store                                *PairProfile
}

// PairFirstTill posts the owner's code with first_till:true under a fresh
// device id and returns the answer. It writes no setting and adopts nothing.
// The refusals match Pair's: no endpoint (ErrNotConfigured) and an
// environment-pinned identity (ErrPairIdentityPinned) before the code is
// sent, so neither burns it. tillName is the name the cloud shows for the
// till.
func PairFirstTill(ctx context.Context, cfg *config.Config, code, tillName string) (*FirstTillPairing, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxRedeemCode {
		return nil, fmt.Errorf("enter the pairing code from the shop's cloud account")
	}
	m := Effective(cfg).Marketplace
	if strings.TrimSpace(m.EndpointURL) == "" {
		return nil, ErrNotConfigured
	}
	mu.RLock()
	pinned := tokenExplicit || explicitConfigured || deviceIDExplicit || storeIDExplicit
	mu.RUnlock()
	if pinned {
		return nil, ErrPairIdentityPinned
	}
	newID := "till-" + uuid.NewString()
	a, err := postPairFull(ctx, m.EndpointURL, code, newID, tillName, buildinfo.Version, true)
	if err != nil {
		logging.L().Warnf("enrolment: pair-first setup failed: %v (ADR-0166)", err)
		return nil, err
	}
	return &FirstTillPairing{DeviceID: newID, Token: a.token, StoreID: a.storeID, MerchantID: a.merchantID, Store: a.store}, nil
}

// Commit makes the held pairing this till's cloud identity, exactly as
// Settings pairing does (ADR-0166 §4 step 6). Call it only after the
// wizard's seed is written, so the first pull comes after it.
func (p *FirstTillPairing) Commit(ctx context.Context, cfg *config.Config, kv Settings) (Status, error) {
	if !acquireAttempt(ctx) {
		return CurrentStatus(), fmt.Errorf("%w: %w", ErrAttemptBusy, ctx.Err())
	}
	defer releaseAttempt()
	resetIdentityForPair(ctx, kv, false, p.DeviceID)
	adoptPaired(ctx, kv, false, p.DeviceID, p.StoreID, p.MerchantID, p.Token)
	logging.L().Infof("enrolment: this till is now paired with store %s as device %s (ADR-0166)", p.StoreID, p.DeviceID)
	return CurrentStatus(), nil
}
