package enroll

// Self-heal recover (ADR-0222, design §4.3): a till whose cloud credential is
// dead proves its pinned approval key and gets a NEW credential for the SAME
// store and device, with no human. Two calls, neither authenticated with the
// dead token: a challenge (a single-use server nonce), then the signed
// recover. This file never registers, pairs, reconnects or starts a new shop
// (TestRecoverSourceNeverRegisters pins that), and the only write it ever
// makes is adoptOwnCredential after the cloud's 200: every other answer
// leaves marketplace.* and till_identity.* byte for byte as they were. No
// token, key, nonce or signature is logged.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// Why CanRecover said no.
const (
	CanRecoverNoIdentity = "no_identity" // no token or store id held: nothing to heal
	CanRecoverPinned     = "env_pinned"  // the environment pins the identity
	CanRecoverStopped    = "stopped"     // the ADR-0200 stop switch is on
	CanRecoverDemo       = "demo"        // demo / never-register till
	CanRecoverNoProof    = "no_key"      // no usable approval key: cannot prove who it is
)

// RecoverOutcome is one recover round's result. An answer other than 200 is an
// outcome, not an error: the caller (internal/credheal) decides what it means.
type RecoverOutcome struct {
	Healed bool
	// Code is the cloud's error.code, or one of this package's own:
	// feature_absent (404, an older cloud), bad_answer (an answer the till
	// refuses), cannot_recover (CanRecover said no), transport (no answer).
	// "" on success.
	Code string
	// Reason is data.reason of a device_revoked.
	Reason string
	// Until is usable_from / retry_after / Retry-After when the cloud gave one.
	Until time.Time
	// Detail is the display text of replaced_by / moved_to, clamped.
	Detail string
	// Transient: a network failure, 5xx or 429; try again later.
	Transient bool
}

// CanRecover reports whether a recover may be tried now, and when not why (one
// of the CanRecover* constants). Local reads only.
func CanRecover(ctx context.Context, cfg *config.Config, kv Settings) (bool, string) {
	mu.RLock()
	held := cur.Token != "" && cur.StoreID != "" && cur.DeviceID != ""
	pinned := tokenExplicit || explicitConfigured || deviceIDExplicit || storeIDExplicit
	mu.RUnlock()
	switch {
	case !held && !pinned:
		return false, CanRecoverNoIdentity
	case pinned:
		return false, CanRecoverPinned
	case Stopped(ctx, kv):
		return false, CanRecoverStopped
	case cfg == nil || cfg.Demo || cfg.NeverRegister:
		return false, CanRecoverDemo
	case !approvalkey.Shared().Has():
		return false, CanRecoverNoProof
	}
	return true, ""
}

const (
	recoverBodyMax  = 4 << 10
	lostAnswerFloor = 30 * time.Second
)

// recoverAnswer is the envelope of every recover answer.
type recoverAnswer struct {
	Data struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		ExpiresAt   string `json:"expires_at"`
		StoreID     string `json:"store_id"`
		DeviceID    string `json:"device_id"`
		DeviceToken string `json:"device_token"`
		Reason      string `json:"reason"`
		UsableFrom  string `json:"usable_from"`
		RetryAfter  string `json:"retry_after"`
		ReplacedBy  string `json:"replaced_by"`
		MovedTo     string `json:"moved_to"`
	} `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// recoverPost sends one JSON body without credentials and returns the status,
// the Retry-After header and the decoded (bounded) envelope.
func recoverPost(ctx context.Context, url string, body any) (int, replyHeaders, recoverAnswer, error) {
	var ans recoverAnswer
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, replyHeaders{}, ans, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, replyHeaders{}, ans, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, replyHeaders{}, ans, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, recoverBodyMax))
	_ = json.Unmarshal(raw, &ans)
	return resp.StatusCode, replyHeaders{RetryAfter: resp.Header.Get("Retry-After"), Date: resp.Header.Get("Date")}, ans, nil
}

// replyHeaders are the two answer headers recover reads.
type replyHeaders struct{ RetryAfter, Date string }

// maxWait caps every wait the cloud can ask for (review M5).
const maxWait = 73 * time.Hour

// waitUntil turns a date the cloud named (usable_from, retry_after) into a time
// on THIS till's clock: the date minus the answer's own Date header, so a till
// whose clock is far off waits the right span; capped at 73 hours. Without a
// usable Date header the date is taken as is, with the same cap. A date already
// past is "now". Zero when the cloud named none.
func waitUntil(named time.Time, h replyHeaders) time.Time {
	if named.IsZero() {
		return time.Time{}
	}
	now := time.Now()
	wait := named.Sub(now)
	if cloudNow, err := http.ParseTime(h.Date); err == nil {
		wait = named.Sub(cloudNow)
	}
	switch {
	case wait < 0:
		wait = 0
	case wait > maxWait:
		wait = maxWait
	}
	return now.Add(wait)
}

func parseWhen(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// refusalOutcome turns a non-200 answer into an outcome.
func refusalOutcome(status int, h replyHeaders, ans recoverAnswer) RecoverOutcome {
	out := RecoverOutcome{Code: ans.Error.Code}
	switch {
	case status == http.StatusNotFound:
		return RecoverOutcome{Code: "feature_absent"}
	case status == http.StatusTooManyRequests:
		out.Code, out.Transient = "rate_limited", true
		if secs, err := strconv.Atoi(strings.TrimSpace(h.RetryAfter)); err == nil && secs > 0 && secs < 86400 {
			out.Until = time.Now().Add(time.Duration(secs) * time.Second)
		}
		return out
	case status >= 500, out.Code == "store_merging", status == http.StatusForbidden:
		if out.Code == "" {
			out.Code = "unavailable"
		}
		out.Transient = true
		return out
	}
	if out.Code == "" {
		out.Code = "refused"
	}
	d := ans.Data
	out.Reason = clampText(d.Reason, 40)
	switch out.Code {
	case "key_quarantined":
		out.Until = waitUntil(parseWhen(d.UsableFrom), h)
	case "recover_limit":
		out.Until = waitUntil(parseWhen(d.RetryAfter), h)
	case "device_revoked":
		out.Detail = clampText(strings.TrimSpace(d.ReplacedBy), 80) // moved_to is a store id, never shown
	}
	return out
}

// Recover runs one recover round (challenge, sign, recover). On a 200 that
// names this till's own store and device with a well-formed token, it adopts
// the token (one settings write) and reports Healed. A first recover_refused is
// retried once with a fresh challenge (an expired or raced challenge); a second
// is final. It shares the registration slot, so it never interleaves with
// Reconnect or Pair.
func Recover(ctx context.Context, cfg *config.Config, kv Settings) (RecoverOutcome, error) {
	if ok, why := CanRecover(ctx, cfg, kv); !ok {
		return RecoverOutcome{Code: "cannot_recover", Detail: why}, nil
	}
	if !acquireAttempt(ctx) {
		return RecoverOutcome{}, ErrAttemptBusy
	}
	defer releaseAttempt()
	m := Effective(cfg).Marketplace
	if strings.TrimSpace(m.EndpointURL) == "" {
		return RecoverOutcome{}, ErrNotConfigured
	}
	storeID, deviceID := m.StoreID, m.DeviceID
	base := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/devices/recover"
	var out RecoverOutcome
	for attempt := 0; attempt < 2; attempt++ {
		var done bool
		out, done = recoverOnce(ctx, cfg, kv, m, base, storeID, deviceID)
		if done || out.Code != "recover_refused" {
			return out, nil
		}
	}
	return out, nil
}

func recoverOnce(ctx context.Context, cfg *config.Config, kv Settings, m config.MarketplaceConfig, base, storeID, deviceID string) (RecoverOutcome, bool) {
	log := logging.L()
	status, hd, ch, err := recoverPost(ctx, base+"/challenge", map[string]string{"store_id": storeID, "device_id": deviceID})
	if err != nil {
		log.Debugf("recover: cloud not reached: %v", err)
		return RecoverOutcome{Code: "transport", Transient: true}, true
	}
	if status != http.StatusOK {
		return refusalOutcome(status, hd, ch), true
	}
	expires := parseWhen(ch.Data.ExpiresAt)
	if ch.Data.ChallengeID == "" || ch.Data.Nonce == "" || expires.IsZero() {
		return RecoverOutcome{Code: "bad_answer"}, true
	}
	expiresAt := expires.UTC().Format("2006-01-02T15:04:05Z")
	kid, pub, sig, err := approvalkey.Shared().SignRecover(storeID, deviceID, ch.Data.ChallengeID, ch.Data.Nonce, expiresAt)
	if err != nil {
		return RecoverOutcome{Code: "cannot_recover", Detail: CanRecoverNoProof}, true
	}
	status, hd, ans, err := recoverPost(ctx, base, map[string]any{
		"store_id": storeID, "device_id": deviceID, "challenge_id": ch.Data.ChallengeID,
		"approval_key": map[string]string{"kid": kid, "public_key": pub},
		"sig":          sig, "version": buildinfo.Version, "platform": runtime.GOOS,
	})
	if err != nil {
		// The answer may have been lost AFTER the cloud rotated the token and
		// spent one of the day's recovers: wait at least 30 s before trying
		// again (review M8).
		log.Debugf("recover: cloud not reached: %v", err)
		return RecoverOutcome{Code: "transport", Transient: true, Until: time.Now().Add(lostAnswerFloor)}, true
	}
	if status >= 500 {
		// A proxy's 502/504 can hide a commit that already rotated the token:
		// same 30 s floor as a lost answer (review 2026-11-27, finding 13).
		out := refusalOutcome(status, hd, ans)
		out.Transient = true
		if floor := time.Now().Add(lostAnswerFloor); out.Until.Before(floor) {
			out.Until = floor
		}
		return out, true
	}
	if status != http.StatusOK {
		return refusalOutcome(status, hd, ans), false
	}
	d := ans.Data
	if d.StoreID != storeID || d.DeviceID != deviceID || !validRotatedToken(d.DeviceToken) {
		log.Warnf("recover: ignored a cloud answer that does not match this till (store or device differs, or a malformed credential)")
		return RecoverOutcome{Code: "bad_answer"}, true
	}
	adoptOwnCredential(ctx, kv, d.DeviceToken, "")
	wakeApprovalReport()
	log.Infof("recover: this till got a new cloud credential for its own shop (device %s)", deviceID)
	kickApprovalKeyReport(context.WithoutCancel(ctx), config.MarketplaceConfig{EndpointURL: m.EndpointURL, DeviceID: deviceID, StoreID: storeID}, nil)
	return RecoverOutcome{Healed: true}, true
}
