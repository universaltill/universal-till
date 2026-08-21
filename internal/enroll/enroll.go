// Package enroll implements anonymous store enrolment (ADR-0013, layer 1).
// Registration is LAZY: multi-store and paid cloud services are the paid
// tier, so a till that never uses the marketplace never registers a store.
// A fresh till only mints a stable device_id at boot; the store identity is
// created on the first real marketplace interaction — a plugin download/
// install (EnsureRegistered) or the operator's Settings → "Register now"
// (RegisterNow) — via POST /v1/stores/register, and the returned store/
// merchant identity + device token persist in settings. Explicit
// configuration (UT_MARKETPLACE_CLIENT_ID / _STORE_ID / _DEVICE_ID) always
// wins; enrolment only fills the gaps. Nothing here ever blocks startup or
// checkout (offline-first).
//
// The marketplace's Ed25519 signing key (needed to verify plugin bundles) is
// fetched alongside enrolment when UT_MARKETPLACE_PUBLIC_KEY is not set, and
// persisted on first sight — a later key change never silently replaces it.
package enroll

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// Settings is the key/value persistence enrolment needs; *settings.Store
// satisfies it.
type Settings interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

// Settings keys holding the enrolled identity.
const (
	keyDeviceID   = "marketplace.device_id"
	keyStoreID    = "marketplace.store_id"
	keyMerchantID = "marketplace.merchant_id"
	keyToken      = "marketplace.token"
	keyPublicKey  = "marketplace.public_key"
	keyEnrolledAt = "marketplace.enrolled_at"
	// keyDeviceRegistered holds the device_id last registered as a device under
	// the store. When it differs from the current device_id (a replica that
	// joined a shop and minted its own device id), the till registers itself as
	// a new device under the shared store — the store's fleet (ADR-0013).
	keyDeviceRegistered = "marketplace.device_registered"
)

// StoreCountrySettingsKey is the settings.key a shop's chosen country lives
// under (ADR-0026 setup wizard). Duplicated from
// internal/pages/common.KeyCountry ("store.country") rather than imported —
// same convention internal/data/reset_archive_repo.go already uses for this
// exact key, to avoid a new cross-package dependency. Exported (rather than
// a private literal) so TestEnrollStoreCountrySettingsKeyMatchesCommon (in
// internal/pages/common) can assert the two never drift apart instead of
// each just trusting the other's copy — see that test and
// reset_archive_repo.go's own StoreCountrySettingsKey for why this matters:
// a silent drift here means the region hint is never sent, with green CI.
const StoreCountrySettingsKey = "store.country"

type identity struct {
	DeviceID   string
	StoreID    string
	MerchantID string
	Token      string
	PublicKey  string
}

var (
	mu  sync.RWMutex
	cur identity
	// storeIDExplicit records whether UT_MARKETPLACE_STORE_ID was set in the
	// environment. cfg.Marketplace.StoreID falls back to the store NAME when
	// unset, so emptiness alone can't tell "operator chose this" from default.
	storeIDExplicit bool
	// explicitConfigured: the operator set a merchant identity via env, so the
	// till counts as registered without auto-enrolment.
	explicitConfigured bool
	// displayStoreID is what UI surfaces show as this till's store identity.
	displayStoreID string

	// attemptMu serializes registration attempts (background loop + the
	// Settings "Register now" button) so the marketplace never sees two
	// concurrent registers from one till.
	attemptMu sync.Mutex

	// Overridable in tests.
	httpClient  = &http.Client{Timeout: 15 * time.Second}
	retryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
)

// Status describes the till's registration state for UI surfaces (status-bar
// chip, Settings card).
type Status struct {
	Registered bool
	StoreID    string
	DeviceID   string
}

// CurrentStatus returns the live registration state. Explicitly configured
// tills (env identity) count as registered.
func CurrentStatus() Status {
	mu.RLock()
	defer mu.RUnlock()
	return Status{
		Registered: explicitConfigured || (cur.StoreID != "" && cur.Token != ""),
		StoreID:    displayStoreID,
		DeviceID:   cur.DeviceID,
	}
}

// RegisterNow performs one immediate, synchronous registration (and signing
// key fetch) attempt — the Settings "Register now" button. The background
// retry loop keeps running regardless; attempts are serialized.
func RegisterNow(ctx context.Context, cfg *config.Config, kv Settings) (Status, error) {
	eff := Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" {
		return CurrentStatus(), fmt.Errorf("marketplace endpoint is not configured")
	}
	attemptMu.Lock()
	defer attemptMu.Unlock()
	var firstErr error
	if s := CurrentStatus(); !s.Registered {
		if err := register(ctx, m, cfg.StoreName, kv); err != nil {
			firstErr = err
		}
	}
	if m.PublicKey == "" {
		mu.RLock()
		haveKey := cur.PublicKey != ""
		mu.RUnlock()
		if !haveKey {
			if err := fetchSigningKey(ctx, m.EndpointURL, kv); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return CurrentStatus(), firstErr
}

// Init loads (or creates) this till's marketplace identity, fills the empty
// cfg.Marketplace fields from it, and — when the till is not yet enrolled and
// not explicitly configured — starts a background registration loop. Call once
// at startup, after LoadRuntimeConfig and before the server starts. wg is
// marked Done once that background loop (if started) has fully exited, so
// callers can wait out shutdown instead of returning while it still runs.
func Init(ctx context.Context, cfg *config.Config, kv Settings, wg *sync.WaitGroup) {
	log := logging.L()
	storeIDExplicit = os.Getenv("UT_MARKETPLACE_STORE_ID") != ""
	// At this point cfg.Marketplace.ClientID can only have come from the
	// environment; a non-empty value means the operator configured a merchant
	// identity themselves, so auto-enrolment must not mint another one.
	clientIDExplicit := cfg.Marketplace.ClientID != ""

	get := func(key string) string {
		v, _, err := kv.Get(ctx, key)
		if err != nil {
			log.Warnf("enrolment: read setting %s: %v", key, err)
		}
		return v
	}
	id := identity{
		DeviceID:   get(keyDeviceID),
		StoreID:    get(keyStoreID),
		MerchantID: get(keyMerchantID),
		Token:      get(keyToken),
		PublicKey:  get(keyPublicKey),
	}
	if id.DeviceID == "" {
		id.DeviceID = cfg.Marketplace.DeviceID
		if id.DeviceID == "" {
			id.DeviceID = "till-" + uuid.NewString()
		}
		if err := kv.Set(ctx, keyDeviceID, id.DeviceID); err != nil {
			log.Warnf("enrolment: persist device_id: %v", err)
		}
	}
	mu.Lock()
	cur = id
	explicitConfigured = clientIDExplicit
	if clientIDExplicit {
		displayStoreID = cfg.Marketplace.StoreID
	} else {
		displayStoreID = id.StoreID
	}
	mu.Unlock()

	// Startup fill: explicit (env) values win, the persisted identity fills
	// the gaps. Safe to mutate cfg here — the server hasn't started yet.
	m := &cfg.Marketplace
	if m.DeviceID == "" {
		m.DeviceID = id.DeviceID
	}
	if m.ClientID == "" {
		m.ClientID = id.MerchantID
	}
	if id.StoreID != "" && !storeIDExplicit {
		m.StoreID = id.StoreID
	}
	if m.PublicKey == "" {
		m.PublicKey = id.PublicKey
	}
	if m.MerchantToken == "" {
		m.MerchantToken = id.Token
	}

	if m.EndpointURL == "" {
		return
	}
	// Store registration is deliberately NOT started here: it is lazy
	// (EnsureRegistered / RegisterNow) — a till that never uses the
	// marketplace never creates a store. The background loop only fetches
	// the signing key (required to verify any plugin bundle) and — for a
	// replica that joined a shop and inherited its store identity —
	// registers this device under the shared store (one store, many
	// devices).
	needKey := m.PublicKey == ""
	needDevice := !clientIDExplicit && m.StoreID != "" && m.MerchantToken != "" && get(keyDeviceRegistered) != id.DeviceID
	deviceName := get("sync.till_name")
	if !needKey && !needDevice {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		run(ctx, *m, deviceName, kv, needKey, needDevice)
	}()
}

// EnsureRegistered returns the effective config, first enrolling the till
// when it isn't registered yet: store registration is lazy, and a plugin
// download/install is the first interaction that actually needs a store
// identity (entitlements hang off it). Failure is non-fatal — the effective
// config is returned regardless and the caller's marketplace call surfaces
// its own error; the next attempt retries.
func EnsureRegistered(ctx context.Context, cfg *config.Config, kv Settings) config.Config {
	if s := CurrentStatus(); !s.Registered {
		if _, err := RegisterNow(ctx, cfg, kv); err != nil {
			logging.L().Infof("enrolment: lazy registration failed (marketplace call will surface it): %v", err)
		}
	}
	return Effective(cfg)
}

// Effective returns a copy of cfg with the live enrolled identity applied
// (explicit configuration still wins). Handlers use it per request so a till
// that enrols after boot installs plugins without a restart; reading through
// a copy keeps the shared config race-free.
func Effective(cfg *config.Config) config.Config {
	out := *cfg
	m := &out.Marketplace
	mu.RLock()
	defer mu.RUnlock()
	if m.DeviceID == "" {
		m.DeviceID = cur.DeviceID
	}
	if m.ClientID == "" {
		m.ClientID = cur.MerchantID
	}
	if cur.StoreID != "" && !storeIDExplicit {
		m.StoreID = cur.StoreID
	}
	if m.PublicKey == "" {
		m.PublicKey = cur.PublicKey
	}
	if m.MerchantToken == "" {
		m.MerchantToken = cur.Token
	}
	return out
}

// run retries the signing-key fetch and/or the device-under-store
// registration until both succeed or the till shuts down. Best-effort by
// design: the till is fully usable offline the whole time.
func run(ctx context.Context, m config.MarketplaceConfig, deviceName string, kv Settings, needKey, needDevice bool) {
	log := logging.L()
	for attempt := 0; ; attempt++ {
		attemptMu.Lock()
		// A RegisterNow call may have fetched the key between waits.
		mu.RLock()
		if cur.PublicKey != "" {
			needKey = false
		}
		mu.RUnlock()
		if needKey {
			if err := fetchSigningKey(ctx, m.EndpointURL, kv); err != nil {
				log.Infof("enrolment: fetch marketplace signing key failed (will retry): %v", err)
			} else {
				needKey = false
			}
		}
		// Register this device under the shop's store. The store token —
		// shared by every till in the shop — authenticates the call.
		if needDevice {
			m.StoreID, m.MerchantToken = currentStoreAuth(m)
			if err := registerDevice(ctx, m, deviceName, kv); err != nil {
				log.Infof("enrolment: register device under store failed (will retry): %v", err)
			} else {
				needDevice = false
			}
		}
		attemptMu.Unlock()
		if !needKey && !needDevice {
			return
		}
		delay := retryDelays[len(retryDelays)-1]
		if attempt < len(retryDelays) {
			delay = retryDelays[attempt]
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// regionForCountry derives the region auto-sent at store registration from
// the shop's chosen country (store.country, ADR-0026 setup wizard) — no new
// setup step for the shop owner. It returns "de" iff country is, after
// trimming whitespace and uppercasing, exactly the ISO-3166 alpha-2 code
// "DE"; any other value (including an empty/unset country, a 3-letter code,
// or garbage/unicode input) returns "" with no panic. This is a jurisdiction
// match, not a language match — ADR-0049 cares about the shop's legal
// jurisdiction, so "de-AT"/"de-CH"-style language confusion must not apply
// here (there is no such ambiguity with a country code, but the exactness is
// deliberate: e.g. "DEU" must NOT match).
func regionForCountry(country string) string {
	if strings.ToUpper(strings.TrimSpace(country)) == "DE" {
		return "de"
	}
	return ""
}

// register performs the anonymous enrolment call and persists the returned
// identity (ADR-0013 layer 1; marketplace handler: /api/v1/stores/register).
// The shop's chosen country (StoreCountrySettingsKey, read from kv) is
// mapped to a region (regionForCountry); when it maps to one, that region
// rides along in the payload so a fresh German till lands in the right
// merchant region with no extra setup step. A failed/absent settings read is
// treated as "no signal" — best-effort, same tolerant pattern used elsewhere
// in this file — and never fails registration.
func register(ctx context.Context, m config.MarketplaceConfig, storeName string, kv Settings) error {
	mu.RLock()
	deviceID := cur.DeviceID
	mu.RUnlock()

	var region string
	if country, _, err := kv.Get(ctx, StoreCountrySettingsKey); err == nil {
		region = regionForCountry(country)
	}

	fields := map[string]string{
		"store_name": storeName,
		"device_id":  deviceID,
		"version":    buildinfo.Version,
	}
	if region != "" {
		fields["region"] = region
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("register returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Data struct {
			StoreID    string `json:"store_id"`
			MerchantID string `json:"merchant_id"`
			Token      string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode register response: %w", err)
	}
	d := envelope.Data
	if d.StoreID == "" || d.Token == "" {
		return fmt.Errorf("register response missing store_id or token")
	}
	if d.MerchantID == "" {
		d.MerchantID = d.StoreID
	}

	log := logging.L()
	for key, value := range map[string]string{
		keyStoreID:          d.StoreID,
		keyMerchantID:       d.MerchantID,
		keyToken:            d.Token,
		keyEnrolledAt:       time.Now().UTC().Format(time.RFC3339),
		keyDeviceRegistered: deviceID, // /stores/register recorded this till as device #1
	} {
		if err := kv.Set(ctx, key, value); err != nil {
			log.Warnf("enrolment: persist %s: %v", key, err)
		}
	}
	mu.Lock()
	cur.StoreID = d.StoreID
	cur.MerchantID = d.MerchantID
	cur.Token = d.Token
	displayStoreID = d.StoreID
	mu.Unlock()
	log.Infof("enrolment: till registered with marketplace as %s (device %s)", d.StoreID, deviceID)
	return nil
}

// ClaimInfo is a freshly minted store claim code plus where to redeem it.
type ClaimInfo struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	ClaimURL  string `json:"claim_url"`
}

// claimCodeCache remembers the last code minted for a store. Every mint
// REPLACES the code server-side, so a second button click would silently
// invalidate the code the operator is reading off the screen — re-showing
// the fresh one instead is what saves them. Duration-based on the till's own
// clock (not the server's expires_at) so marketplace clock skew can't extend
// it past the server-side 15-minute TTL.
var claimCodeCache struct {
	sync.Mutex
	storeID string
	info    ClaimInfo
	until   time.Time
}

// claimCodeReuseWindow is how long a fetched code is re-shown instead of
// re-minted: the server TTL minus a safety margin for typing time.
const claimCodeReuseWindow = 13 * time.Minute

// ClaimCode asks the marketplace for a short-lived claim code (ADR-0013
// layer 2): the operator shows it to the owner, who redeems it at the
// marketplace's /ui/claim after signing in with their Universal Till ID.
// Needs a registered store; the store token authenticates the call.
// Repeated calls within the reuse window return the SAME code.
func ClaimCode(ctx context.Context, cfg *config.Config) (ClaimInfo, error) {
	eff := Effective(cfg)
	m := eff.Marketplace
	storeID, token := currentStoreAuth(m)
	if m.EndpointURL == "" || storeID == "" || token == "" {
		return ClaimInfo{}, fmt.Errorf("till is not registered with the marketplace yet")
	}
	claimCodeCache.Lock()
	if claimCodeCache.storeID == storeID && time.Now().Before(claimCodeCache.until) {
		info := claimCodeCache.info
		claimCodeCache.Unlock()
		return info, nil
	}
	claimCodeCache.Unlock()
	payload, err := json.Marshal(map[string]string{"store_id": storeID})
	if err != nil {
		return ClaimInfo{}, err
	}
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/claim-code"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return ClaimInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return ClaimInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ClaimInfo{}, fmt.Errorf("claim-code returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Data struct {
			Code      string `json:"code"`
			ExpiresAt string `json:"expires_at"`
			ClaimPath string `json:"claim_path"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return ClaimInfo{}, fmt.Errorf("decode claim-code response: %w", err)
	}
	if envelope.Data.Code == "" {
		return ClaimInfo{}, fmt.Errorf("claim-code response missing code")
	}
	webBase := strings.TrimSuffix(strings.TrimRight(m.EndpointURL, "/"), "/api")
	info := ClaimInfo{
		Code:      envelope.Data.Code,
		ExpiresAt: envelope.Data.ExpiresAt,
		ClaimURL:  webBase + envelope.Data.ClaimPath,
	}
	claimCodeCache.Lock()
	claimCodeCache.storeID = storeID
	claimCodeCache.info = info
	claimCodeCache.until = time.Now().Add(claimCodeReuseWindow)
	claimCodeCache.Unlock()
	return info, nil
}

// Device is one till registered under this store (the fleet).
type Device struct {
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// Fleet fetches every till device registered under this till's store from the
// marketplace (one store, many devices). Needs a registered store; the shared
// store token authenticates it. Network call — callers should treat failure as
// "fleet unavailable", never block the kiosk on it.
func Fleet(ctx context.Context, cfg *config.Config) ([]Device, error) {
	eff := Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return nil, fmt.Errorf("till is not registered")
	}
	endpoint := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/devices?store_id=" + url.QueryEscape(m.StoreID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fleet returned %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Devices []Device `json:"devices"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode fleet: %w", err)
	}
	return envelope.Data.Devices, nil
}

// currentStoreAuth returns the live store id + token the device-registration
// call should use (the store identity register() may have just persisted wins
// over the startup copy).
func currentStoreAuth(m config.MarketplaceConfig) (string, string) {
	mu.RLock()
	defer mu.RUnlock()
	storeID := m.StoreID
	if cur.StoreID != "" && !storeIDExplicit {
		storeID = cur.StoreID
	}
	token := m.MerchantToken
	if token == "" {
		token = cur.Token
	}
	return storeID, token
}

// registerDevice adds this till as a device under its (already-registered)
// store — the second, third, … till of a shop that shares one store identity
// and its entitlements (marketplace: /v1/stores/devices/register). The shared
// store token authenticates it. Best-effort, retried by the background loop.
func registerDevice(ctx context.Context, m config.MarketplaceConfig, deviceName string, kv Settings) error {
	mu.RLock()
	deviceID := cur.DeviceID
	mu.RUnlock()
	storeID, token := m.StoreID, m.MerchantToken
	if storeID == "" || token == "" {
		return fmt.Errorf("no store identity yet")
	}
	if deviceName == "" {
		deviceName = "Till"
	}
	payload, err := json.Marshal(map[string]string{
		"store_id": storeID, "device_id": deviceID, "device_name": deviceName, "version": buildinfo.Version,
	})
	if err != nil {
		return err
	}
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/devices/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("device register returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := kv.Set(ctx, keyDeviceRegistered, deviceID); err != nil {
		logging.L().Warnf("enrolment: persist device_registered: %v", err)
	}
	logging.L().Infof("enrolment: till registered as device %s under store %s", deviceID, storeID)
	return nil
}

// fetchSigningKey retrieves the marketplace's Ed25519 release-signing public
// key so a fresh till can verify plugin bundles without manual configuration.
// The endpoint lives on the marketplace UI server, a sibling of the /api base
// (same derivation the entitlements fetch uses).
func fetchSigningKey(ctx context.Context, endpointURL string, kv Settings) error {
	base := strings.TrimSuffix(strings.TrimRight(endpointURL, "/"), "/api")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/ui/api/signing-key", nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("signing-key returned %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Algorithm    string `json:"algorithm"`
			PublicKeyHex string `json:"public_key_hex"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode signing-key response: %w", err)
	}
	if envelope.Data.Algorithm != "ed25519" {
		return fmt.Errorf("unsupported signing algorithm %q", envelope.Data.Algorithm)
	}
	raw, err := hex.DecodeString(envelope.Data.PublicKeyHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("signing-key response is not a valid ed25519 public key")
	}

	if err := kv.Set(ctx, keyPublicKey, envelope.Data.PublicKeyHex); err != nil {
		logging.L().Warnf("enrolment: persist public_key: %v", err)
	}
	mu.Lock()
	cur.PublicKey = envelope.Data.PublicKeyHex
	mu.Unlock()
	logging.L().Infof("enrolment: marketplace signing key pinned (%s…)", envelope.Data.PublicKeyHex[:12])
	return nil
}
