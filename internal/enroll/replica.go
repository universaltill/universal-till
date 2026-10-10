// Per-till cloud identity for replicas (ut-docs#2730).
//
// Every till of a shop is its OWN device in the cloud, under the shop's one
// store. The admin sync used to copy the main till's marketplace identity
// (device id, registration marker, store token) onto every replica, so every
// till heartbeat as the main till's device — my.universaltill.com showed one
// row fed by several machines, with stale versions for the rest. Those keys
// are now per-till (data.PerTillSettingPrefixes: never dumped by the main
// till, never applied by a replica) and this file gives a replica its own
// device identity:
//
//  1. Repair at boot (repairCopiedIdentity): a replica whose device id was not
//     minted for its own sync.till_id (marker marketplace.device_till_id)
//     mints a fresh one and drops the copied registration markers. Every
//     pre-fix replica carries the main till's id (the admin sync overwrote
//     it on every pull), so the marker — not a comparison with the main
//     till's id, which a replica cannot know offline — is what detects it.
//     Local only; never needs the network.
//
//  2. Vouch (replicaLoop): the replica asks its main till over the
//     authenticated LAN sync channel (POST /api/sync/cloud-device, sync
//     bearer) to register the replica's own device id + version under the
//     store. The main till does that with the store token only IT holds.
//
//  3. Redeem (redeem.go, ADR-0116 D3, ut-docs#2769): when the replica's
//     device has no cloud credential yet, the cloud's devices/register
//     answer to the main till carries a one-time redeem code; the main till
//     relays only that code, and the replica exchanges it for its OWN device
//     credential directly with the cloud over TLS
//     (POST /v1/stores/devices/redeem), keeping it in marketplace.token.
//
// Security (the reason for this shape): NO cloud credential ever crosses the
// LAN. The vouch answer type (Vouch) has no token field — at most a redeem
// code, which is single-use, valid for 10 minutes, bound to (store, device)
// and not a token — and the replica persists only the device registration
// marker from it, plus the credential the CLOUD returns for the code, so
// even a buggy or hostile main till cannot plant a credential. Residuals
// (ADR-0116 D3): until ADR-0114's pinned TLS/wss, LAN sync is plain HTTP, so
// a LAN eavesdropper can race the replica to redeem (the replica gets 409
// already_redeemed and keeps what it has); and the main till sees the code,
// so a hostile main till could redeem it itself and get the replica's first
// credential. Either shows on the Tills page as a credential issued_by the
// vouching till. A replica whose code was not redeemed (old cloud, env-pinned
// token, refused redeem) has no credential of its own and the main till
// speaks for it.
//
// A replica that already holds a copy of the store token (pre-fix fleets)
// keeps it: removing a copy revokes nothing (it is the same credential the
// main till still uses), and it would cut that replica's own heartbeat,
// directives and uploads off the cloud. The remedy is its own credential —
// by redemption above, or by D4 rotation (rotate.go) — which replaces the
// copy in marketplace.token. With a pre-fix main till (no vouch endpoint)
// such a replica registers its own device id with that token, so the cloud
// still sees a distinct device.
//
// None of this ever blocks selling: every step is best-effort and retried.
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
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
)

const (
	// keyDeviceTillID records the sync.till_id the current device id was
	// minted for on a replica. A mismatch means the identity was copied from
	// another till (join snapshot or a pre-#2730 admin sync).
	keyDeviceTillID = "marketplace.device_till_id"

	keySyncPrimaryURL = "sync.primary_url"
	keySyncBearer     = "sync.bearer"
	keySyncTillID     = "sync.till_id"
	keySyncTillName   = "sync.till_name"

	// keyTillName is shop-wide: on a replica it holds the main till's name.
	keyTillName = "till.name"
)

var (
	// ErrNotRegistered: the main till has no store identity to vouch with.
	ErrNotRegistered = errors.New("enrol: main till is not registered with the cloud")
	// ErrBadDeviceRequest: the replica's request is malformed or names a
	// device id it may not have (the main till's own).
	ErrBadDeviceRequest = errors.New("enrol: invalid replica device request")
	// errPrimaryUnsupported: the main till predates the vouch endpoint.
	errPrimaryUnsupported = errors.New("enrol: main till has no cloud-device endpoint")

	// vouchInterval is how often a replica re-asks after a success (or after
	// finding a pre-fix main till), so the cloud's record of its version and
	// last contact stays current even when the replica has no token of its
	// own to heartbeat with. Overridable in tests.
	vouchInterval = 6 * time.Hour
	// replicaRetryDelays is the vouch loop's backoff (same shape as
	// retryDelays, separate so tests can shorten it without racing the store
	// loop). Overridable in tests.
	replicaRetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

	deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// Vouch is the main till's answer to a replica: which store the replica's
// device is now registered under, plus at most a one-time redeem code. It
// deliberately has NO credential field — that is what keeps a token off the
// LAN (file comment); the JSON wire shape of POST /api/sync/cloud-device's
// data.
type Vouch struct {
	StoreID  string `json:"store_id"`
	DeviceID string `json:"device_id"`
	// Entitlement is the main till's own cached cloud entitlement
	// (ut-docs#2792), set by the handler when it has one. Not a credential:
	// it only drives paid cloud surfaces, never a sale (ADR-0060 §5), and a
	// main till already decides every other admin setting a replica runs on.
	Entitlement *entitlement.Cached `json:"entitlement,omitempty"`
	// RedeemCode is the cloud's one-time code for the replica's device
	// (ADR-0116 D3), relayed verbatim when devices/register returned one.
	// Not a credential: the replica exchanges it for its own token directly
	// with the cloud (redeemOwnCredential); it is single-use, expires in
	// 10 minutes and is bound to (store, device). Empty (omitted) otherwise.
	RedeemCode string `json:"redeem_code,omitempty"`
}

// ReplicaRequest is what the main till knows about the asking replica. TillID
// and DeviceName come from the main till's own tills table (the authenticated
// caller), never from the request body; DeviceID and Version come from the
// replica.
type ReplicaRequest struct {
	TillID     string
	DeviceID   string
	DeviceName string
	Version    string
}

// VouchSource is the replica's call to its main till. Swappable in tests.
type VouchSource interface {
	RequestVouch(ctx context.Context, deviceID, version string) (Vouch, error)
}

var newVouchSource = func(kv Settings) VouchSource { return primarySource{kv: kv} }

func isReplica(ctx context.Context, kv Settings) bool {
	v, _, _ := kv.Get(ctx, keySyncPrimaryURL)
	return strings.TrimSpace(v) != ""
}

// repairCopiedIdentity gives a replica its own device id when the one it
// holds was minted for another till. Keeps any token (file comment). Returns
// whether it repaired.
func repairCopiedIdentity(ctx context.Context, kv Settings, get func(string) string) bool {
	if strings.TrimSpace(get(keySyncPrimaryURL)) == "" {
		return false
	}
	tillID := strings.TrimSpace(get(keySyncTillID))
	if tillID == "" || get(keyDeviceTillID) == tillID {
		return false
	}
	old := get(keyDeviceID)
	copied := old != "" && get(keyDeviceRegistered) != ""
	fresh := "till-" + uuid.NewString()
	// Marker LAST: a failure part-way leaves it unset, so the next boot
	// simply repairs again.
	for _, kvp := range [][2]string{
		{keyDeviceID, fresh},
		{keyDeviceRegistered, ""},
		{keyEnrolledAt, ""},
		{keyDeviceTillID, tillID},
	} {
		if err := kv.Set(ctx, kvp[0], kvp[1]); err != nil {
			logging.L().Warnf("enrolment: repair replica identity: persist %s: %v (will retry next boot)", kvp[0], err)
			return false
		}
	}
	if copied {
		// INFO, not WARN (ut-docs#2798): a successful self-repair, not a
		// problem the owner must act on (a failed repair above stays WARN).
		logging.L().Infof("enrolment: this replica carried another till's cloud device identity (%s); minted its own (%s) and will register it through the main till (ut-docs#2730)", old, fresh)
	} else {
		logging.L().Infof("enrolment: replica minted its own cloud device id %s", fresh)
	}
	return true
}

// applyVouch records that the main till registered THIS till's device. Only
// the registration marker is persisted — never anything credential-like, and
// never the store id (store-level, it arrives with the admin sync).
func applyVouch(ctx context.Context, m config.MarketplaceConfig, kv Settings, v Vouch) error {
	_, err := applyVouchRedeem(ctx, m, kv, v)
	return err
}

// applyVouchRedeem is applyVouch that also reports whether redeeming the
// relayed code failed transiently (redeemOwnCredential), so replicaAttempt
// can ask again soon for a fresh code.
func applyVouchRedeem(ctx context.Context, m config.MarketplaceConfig, kv Settings, v Vouch) (retryRedeem bool, err error) {
	mu.RLock()
	deviceID := cur.DeviceID
	mu.RUnlock()
	if v.DeviceID != deviceID {
		return false, fmt.Errorf("enrol: main till vouched for device %q, this till is %q", v.DeviceID, deviceID)
	}
	if err := kv.Set(ctx, keyDeviceRegistered, deviceID); err != nil {
		return false, fmt.Errorf("enrol: persist %s: %w", keyDeviceRegistered, err)
	}
	// ut-docs#2753: only while still a replica — a vouch answer that lands
	// after POST /api/sync/promote (which clears sync.primary_url, then
	// ForgetReplicaVouch) must not bring the via-main state back.
	if isReplica(ctx, kv) {
		mu.Lock()
		vouchedDevice = deviceID
		mu.Unlock()
	}
	if at, _, _ := kv.Get(ctx, keyEnrolledAt); at == "" {
		if err := kv.Set(ctx, keyEnrolledAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
			logging.L().Warnf("enrolment: persist enrolled_at: %v", err)
		}
	}
	// ADR-0116 D3: a one-time redeem code from the main till lets this
	// replica fetch its OWN credential straight from the cloud. Outside mu
	// (network call), and best-effort: the vouch above is already recorded
	// whatever the cloud says.
	retryUnsaved(ctx, kv)
	retryRedeem = redeemOwnCredential(ctx, m, kv, v, deviceID)
	// Applied whether or not this replica now holds its own token
	// (ADR-0148, ut-docs#3615): its own check-in is gated on this very
	// cache, so a token-holding replica that skipped the relay would never
	// sync. Its own check-in, once running, stays the fresher source by
	// the newer-confirmation-wins rule in applyRelayedEntitlement.
	applyRelayedEntitlement(ctx, kv, v.Entitlement)
	return retryRedeem, nil
}

// applyRelayedEntitlement stores the main till's entitlement cache on a
// replica (ut-docs#2792), including one that holds its own store token:
// ADR-0148 gates that replica's own check-in on this cache, so the relay is
// what first lets it sync (ut-docs#3615). Its own check-in, once running, is
// not fought: a relayed confirmation older than the one already held is
// dropped. Best-effort: an invalid block keeps the replica's cache, as does
// a relayed confirmation older than the one already held (two vouch answers
// landing out of order, a re-pointed replica, or the replica's own fresher
// check-in); a failed
// write is logged and retried on the next vouch. last_confirmed_at is written
// last so a partial write never looks freshly confirmed.
func applyRelayedEntitlement(ctx context.Context, kv Settings, c *entitlement.Cached) {
	if c == nil {
		return
	}
	vals, err := c.RelayValues()
	if err != nil {
		logging.L().Warnf("enrolment: ignoring the main till's entitlement (cache kept): %v", err)
		return
	}
	if held, _, _ := kv.Get(ctx, entitlement.KeyLastConfirmedAt); held != "" {
		heldAt, herr := time.Parse(time.RFC3339, strings.TrimSpace(held))
		relayedAt, _ := time.Parse(time.RFC3339, vals[entitlement.KeyLastConfirmedAt])
		if herr == nil && heldAt.After(relayedAt) {
			return
		}
	}
	for _, k := range []string{entitlement.KeyPlan, entitlement.KeySubscriptionStatus, entitlement.KeyExpiresAt, entitlement.KeyLastConfirmedAt} {
		if err := kv.Set(ctx, k, vals[k]); err != nil {
			logging.L().Warnf("enrolment: persist relayed %s: %v (will retry on the next vouch)", k, err)
			return
		}
	}
}

// replicaLoop keeps this replica's device registered in the cloud through
// its main till, until ctx ends.
func replicaLoop(ctx context.Context, m config.MarketplaceConfig, kv Settings) {
	src := newVouchSource(kv)
	for attempt := 0; ; attempt++ {
		wait, ok := replicaAttempt(ctx, m, kv, src, attempt)
		if ok {
			attempt = -1 // next failure starts the backoff afresh
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// replicaAttempt runs one vouch attempt; it returns how long to wait before
// the next one and whether this one succeeded.
func replicaAttempt(ctx context.Context, m config.MarketplaceConfig, kv Settings, src VouchSource, attempt int) (time.Duration, bool) {
	log := logging.L()
	backoff := replicaRetryDelays[len(replicaRetryDelays)-1]
	if attempt < len(replicaRetryDelays) {
		backoff = replicaRetryDelays[attempt]
	}
	mu.RLock()
	deviceID := cur.DeviceID
	mu.RUnlock()
	v, err := src.RequestVouch(ctx, deviceID, buildinfo.Version)
	switch {
	case err == nil:
		retryRedeem, aerr := applyVouchRedeem(ctx, m, kv, v)
		if aerr != nil {
			log.Infof("enrolment: record main till's vouch failed (will retry): %v", aerr)
			return backoff, false
		}
		if retryRedeem {
			// The relayed code expires in 10 minutes; the next vouch
			// brings a fresh one (ADR-0116 D3).
			return backoff, false
		}
		return vouchInterval, true
	case errors.Is(err, errPrimaryUnsupported):
		// Pre-#2730 main till: register this till's own device id with the
		// (legacy) token it may already hold, so the cloud still sees a
		// distinct device. No token → nothing to do until the main till
		// upgrades.
		if !acquireAttempt(ctx) {
			return 0, false
		}
		m.StoreID, m.MerchantToken = currentStoreAuth(m)
		var rerr error
		if m.StoreID != "" && m.MerchantToken != "" {
			rerr = registerDevice(ctx, m, DeviceName(ctx, kv), kv)
		}
		releaseAttempt()
		if rerr != nil {
			log.Infof("enrolment: register replica device under store failed (will retry): %v", rerr)
			return backoff, false
		}
		return vouchInterval, true
	default:
		log.Infof("enrolment: main till could not register this replica in the cloud (will retry): %v", err)
		return backoff, false
	}
}

// DeviceName is the name this till registers with the cloud for itself
// (ut-docs#3019). The owner types it into till.name, but till.name is
// shop-wide (data.ShopWideSettingPrefixes): on a replica it holds the main
// till's name. So:
//   - replica: sync.till_name only, never till.name;
//   - main/standalone: till.name, else sync.till_name.
//
// Promoting a replica (data.SettingsRepo.ClearReplicaIdentity, ut-docs#3025)
// copies sync.till_name into till.name before clearing it, so the newly
// promoted till keeps reporting its own name here, not the old main till's.
// When sync.till_name is blank or missing, there is nothing to carry over,
// so till.name is set blank instead: the till falls back to its translated
// default name (shown by tillNameOrDefault) and this function reports ""
// rather than keep the old main till's name (ut-docs#3030).
func DeviceName(ctx context.Context, kv Settings) string {
	get := func(key string) string {
		v, _, _ := kv.Get(ctx, key)
		return strings.TrimSpace(v)
	}
	if isReplica(ctx, kv) {
		return get(keySyncTillName)
	}
	if name := get(keyTillName); name != "" {
		return name
	}
	return get(keySyncTillName)
}

// registerOnReplica is RegisterNow's replica branch: a replica never creates
// its own anonymous store (it would split the shop in the cloud); it asks the
// main till to vouch for it, once, synchronously.
func registerOnReplica(ctx context.Context, m config.MarketplaceConfig, kv Settings) error {
	mu.RLock()
	deviceID := cur.DeviceID
	mu.RUnlock()
	v, err := newVouchSource(kv).RequestVouch(ctx, deviceID, buildinfo.Version)
	if err != nil {
		return fmt.Errorf("replica: register through the main till: %w", err)
	}
	return applyVouch(ctx, m, kv, v)
}

// primarySource asks this replica's main till, reading sync.primary_url and
// sync.bearer at call time.
type primarySource struct{ kv Settings }

func (p primarySource) RequestVouch(ctx context.Context, deviceID, version string) (Vouch, error) {
	get := func(k string) string { v, _, _ := p.kv.Get(ctx, k); return strings.TrimSpace(v) }
	primary, bearer := get(keySyncPrimaryURL), get(keySyncBearer)
	if primary == "" || bearer == "" {
		return Vouch{}, fmt.Errorf("not a paired replica")
	}
	body, err := json.Marshal(map[string]string{"device_id": deviceID, "version": version})
	if err != nil {
		return Vouch{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(primary, "/")+"/api/sync/cloud-device", bytes.NewReader(body))
	if err != nil {
		return Vouch{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := httpClient.Do(req)
	if err != nil {
		return Vouch{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnauthorized:
		// A pre-#2730 main till's session middleware answers 401 for this
		// unknown path before any handler runs, so 401 cannot be told apart
		// from "no such endpoint". Treating a genuine 401 (revoked bearer)
		// the same grants nothing: the fallback only registers THIS till's
		// own device id with a token it already holds.
		return Vouch{}, errPrimaryUnsupported
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		body := strings.TrimSpace(string(msg))
		// The main till relays the cloud refusing its shop as 403
		// {"error":"service_unavailable"} (ut-docs#3990); carry it typed so
		// IsServiceRefused holds on this side too. Its envelope's error is a
		// bare code string, unlike the cloud's {code,message} object.
		var env struct {
			Error string `json:"error"`
		}
		if resp.StatusCode == http.StatusForbidden && json.Unmarshal([]byte(body), &env) == nil && env.Error == ServiceRefusedCode {
			return Vouch{}, &RegisterHTTPError{Op: "main till", Status: http.StatusForbidden, Code: ServiceRefusedCode, Body: body}
		}
		return Vouch{}, fmt.Errorf("main till answered %d: %s", resp.StatusCode, body)
	}
	var env struct {
		Data Vouch `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&env); err != nil {
		return Vouch{}, fmt.Errorf("decode main till's answer: %w", err)
	}
	return env.Data, nil
}

// VouchForReplica is the main till's side of POST /api/sync/cloud-device:
// with its own store token it registers the replica's device under the store
// and says so. The caller has already authenticated the replica (sync
// bearer) and supplies TillID/DeviceName from its own records. The store
// token is used here and never returned.
func VouchForReplica(ctx context.Context, cfg *config.Config, req ReplicaRequest) (Vouch, error) {
	eff := Effective(cfg)
	m := eff.Marketplace
	storeID, token := currentStoreAuth(m)
	if m.EndpointURL == "" || storeID == "" || token == "" {
		return Vouch{}, ErrNotRegistered
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.TillID = strings.TrimSpace(req.TillID)
	if req.TillID == "" || !deviceIDPattern.MatchString(req.DeviceID) || req.DeviceID == m.DeviceID {
		return Vouch{}, ErrBadDeviceRequest
	}
	if len(req.Version) > 64 {
		req.Version = req.Version[:64]
	}
	m.StoreID, m.MerchantToken = storeID, token
	code, err := registerDeviceID(ctx, m, req.DeviceID, req.DeviceName, req.Version, req.TillID)
	if err != nil {
		return Vouch{}, err
	}
	logging.L().Infof("enrolment: registered replica till %s as device %s under store %s", req.TillID, req.DeviceID, storeID)
	v := Vouch{StoreID: storeID, DeviceID: req.DeviceID}
	// ADR-0116 D3: relay the cloud's one-time redeem code — never a token —
	// so the replica can redeem its own credential directly with the cloud.
	// A malformed code is dropped (the replica's redeem would only fail),
	// and never logged.
	switch {
	case code == "":
	case validRedeemCode(code):
		v.RedeemCode = code
	default:
		logging.L().Warnf("enrolment: the cloud's redeem code for replica device %s is malformed (length %d); not relayed (ADR-0116 D3)", req.DeviceID, len(code))
	}
	return v, nil
}
