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
//  2. Vouch (replicaLoop): the replica asks its main till over the
//     authenticated LAN sync channel (POST /api/sync/cloud-device, sync
//     bearer) to register the replica's own device id + version under the
//     store. The main till does that with the store token only IT holds.
//
// Security (the reason for this shape): NO cloud credential ever crosses the
// LAN. The vouch answer type (Vouch) has no token field, and the replica
// only ever persists the device registration marker from it, so even a buggy
// or hostile main till cannot plant a credential. Today's cloud has one
// token per store and no per-device token; giving a replica its own cloud
// credential needs the cloud to issue per-device tokens (follow-up card) —
// until then a fresh replica has none and the main till speaks for it.
//
// A replica that already holds a copy of the store token (pre-fix fleets)
// keeps it: removing a copy revokes nothing (it is the same credential the
// main till still uses), and it would cut that replica's own heartbeat,
// directives and uploads off the cloud. The real remedy is rotation to
// per-device tokens, which the cloud follow-up enables. With a pre-fix main
// till (no vouch endpoint) such a replica registers its own device id with
// that token, so the cloud still sees a distinct device.
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
// device is now registered under. It deliberately has NO credential field —
// that is what keeps a token off the LAN (file comment); the JSON wire shape
// of POST /api/sync/cloud-device's data.
type Vouch struct {
	StoreID  string `json:"store_id"`
	DeviceID string `json:"device_id"`
	// Entitlement is the main till's own cached cloud entitlement
	// (ut-docs#2792), set by the handler when it has one. Not a credential:
	// it only drives paid cloud surfaces, never a sale (ADR-0060 §5), and a
	// main till already decides every other admin setting a replica runs on.
	Entitlement *entitlement.Cached `json:"entitlement,omitempty"`
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
	mu.RLock()
	deviceID := cur.DeviceID
	mu.RUnlock()
	if v.DeviceID != deviceID {
		return fmt.Errorf("enrol: main till vouched for device %q, this till is %q", v.DeviceID, deviceID)
	}
	if err := kv.Set(ctx, keyDeviceRegistered, deviceID); err != nil {
		return fmt.Errorf("enrol: persist %s: %w", keyDeviceRegistered, err)
	}
	if at, _, _ := kv.Get(ctx, keyEnrolledAt); at == "" {
		if err := kv.Set(ctx, keyEnrolledAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
			logging.L().Warnf("enrolment: persist enrolled_at: %v", err)
		}
	}
	_, token := currentStoreAuth(m)
	applyRelayedEntitlement(ctx, kv, v.Entitlement, token != "")
	return nil
}

// applyRelayedEntitlement stores the main till's entitlement cache on a
// replica with no cloud token of its own (ut-docs#2792). A replica that
// holds a store token (a pre-#2730 copy, or one from the environment) runs
// its own cloud sync, which is the fresher source — the relay would fight
// it, so it is skipped. Best-effort: an invalid block keeps the replica's
// cache, as does a relayed confirmation older than the one already held (two
// vouch answers landing out of order, or a re-pointed replica); a failed
// write is logged and retried on the next vouch. last_confirmed_at is written
// last so a partial write never looks freshly confirmed.
func applyRelayedEntitlement(ctx context.Context, kv Settings, c *entitlement.Cached, ownToken bool) {
	if c == nil || ownToken {
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
		if aerr := applyVouch(ctx, m, kv, v); aerr != nil {
			log.Infof("enrolment: record main till's vouch failed (will retry): %v", aerr)
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
			rerr = registerDevice(ctx, m, deviceName(ctx, kv), kv)
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

func deviceName(ctx context.Context, kv Settings) string {
	v, _, _ := kv.Get(ctx, keySyncTillName)
	return v
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
		return Vouch{}, fmt.Errorf("main till answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
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
	if err := registerDeviceID(ctx, m, req.DeviceID, req.DeviceName, req.Version, req.TillID); err != nil {
		return Vouch{}, err
	}
	logging.L().Infof("enrolment: registered replica till %s as device %s under store %s", req.TillID, req.DeviceID, storeID)
	return Vouch{StoreID: storeID, DeviceID: req.DeviceID}, nil
}
