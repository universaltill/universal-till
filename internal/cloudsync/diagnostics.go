package cloudsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// ErrNotRegistered is the exported face of errNotRegistered for callers
// outside this package (the Settings activation handler maps it to its
// own localized message).
var ErrNotRegistered = errNotRegistered

// CloudError is a non-2xx answer from a /v1/stores/diagnostics/* endpoint,
// decoded from ut-cloud's {error:{code,message}} envelope so a caller can
// branch on the documented codes (invalid_code, code_unavailable,
// unauthorized, session_not_active, …) instead of string-matching a
// message that is free to change.
type CloudError struct {
	Status  int
	Code    string
	Message string
}

func (e *CloudError) Error() string {
	return fmt.Sprintf("cloud answered %d %s: %s", e.Status, e.Code, e.Message)
}

// cloudErrorMaxBytes bounds how much of an error body is read — an error
// envelope is a few hundred bytes; never buffer an unbounded body from a
// misbehaving endpoint (same stance as pullStatusMaxBytes).
const cloudErrorMaxBytes = 64 << 10

// decodeCloudError turns a non-2xx response into a *CloudError, tolerating
// a body that isn't the envelope (a proxy's HTML 502, say) by leaving Code
// empty.
func decodeCloudError(resp *http.Response) *CloudError {
	ce := &CloudError{Status: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, cloudErrorMaxBytes))
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil {
		ce.Code, ce.Message = env.Error.Code, env.Error.Message
	}
	return ce
}

// postJSON is post() with the error envelope decoded: a non-2xx answer
// comes back as *CloudError; a transport failure as the plain error.
func postJSON(ctx context.Context, cfg *config.Config, path string, payload any) ([]byte, error) {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(m.EndpointURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeCloudError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, cloudErrorMaxBytes))
}

// ActivateDiagnostics redeems a Universal Till-issued activation code
// (ADR-0092 §1): POST /v1/stores/diagnostics/activate {store_id, code,
// device_id}, authenticated with the store's existing bearer token — the
// same credential every other call in this package sends; no new till-side
// secret. Returns the cloud's session id. The caller persists it via
// diagnostics.Activate; this function only talks to the cloud.
func ActivateDiagnostics(ctx context.Context, cfg *config.Config, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("activation code is required")
	}
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return "", errNotRegistered
	}
	body, err := postJSON(ctx, cfg, "/v1/stores/diagnostics/activate", map[string]string{
		"store_id":  m.StoreID,
		"code":      code,
		"device_id": enroll.CurrentStatus().DeviceID,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Data struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("cloudsync: decode activate response: %w", err)
	}
	if strings.TrimSpace(resp.Data.SessionID) == "" {
		return "", fmt.Errorf("cloudsync: activate response missing session_id")
	}
	return strings.TrimSpace(resp.Data.SessionID), nil
}

// uploadPendingDiagnostics is the ADR-0092 §4 upload, riding this tick
// exactly like uploadPendingIssueReports: flush the ring to disk, then
// POST each pending batch FIFO to /v1/stores/diagnostics/batch. A 200
// deletes the local copy (only then). Three failure shapes, all read from
// ut-cloud's own documented codes (internal/httpapi/handlers/diagnostics.go):
//   - 409 session_not_active / 404 session_not_found: the SESSION is dead
//     server-side (revoked, rejected, or the session row itself is gone —
//     a GDPR crypto-shred, a merchant delete, an env reset). Both are
//     TERMINAL for the whole session, not just this batch: the rest of the
//     queue is drained in one step and the local flag flips off, the same
//     disposition as a revoke. Retrying either would just repeat the same
//     answer forever.
//   - 400 invalid_request / invalid_event: this ONE BATCH is malformed and
//     can never become valid by resending it unchanged — terminal for the
//     batch only (discarded), not the session; the loop continues to the
//     next batch rather than stopping, so one bad batch cannot head-of-line
//     block every batch queued after it (review finding, ut-docs#2169: the
//     original code treated every non-409 the same as a network failure and
//     `return`ed, so a single permanently-rejected batch silently blocked
//     the entire upload queue until eviction, up to `maxPendingBatches`
//     batches later).
//   - Anything else (network error, 5xx): transient — leave the batch for
//     the next tick and stop this tick's drain so FIFO order is preserved.
//
// Never called from a request path: checkout is never behind this.
func uploadPendingDiagnostics(ctx context.Context, cfg *config.Config, db *sql.DB) {
	kv := data.NewSettingsRepo(db)
	if err := diagnostics.Flush(ctx, kv); err != nil {
		logging.L().Warnf("cloudsync: diagnostics flush failed (events dropped, gap marker queued): %v", err)
	}
	batches, err := diagnostics.Pending()
	if err != nil {
		logging.L().Warnf("cloudsync: listing pending diagnostic batches: %v", err)
		return
	}
	if len(batches) == 0 {
		return
	}
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return // not registered — the queue simply waits (bounded by diagnostics' own caps)
	}
	skipSession := map[string]bool{}
	for _, b := range batches {
		if skipSession[b.SessionID] {
			continue
		}
		_, err := postJSON(ctx, cfg, "/v1/stores/diagnostics/batch", map[string]any{
			"store_id":   m.StoreID,
			"session_id": b.SessionID,
			"seq":        b.Seq,
			"events":     b.Events,
		})
		if err == nil {
			if derr := diagnostics.Discard(b); derr != nil {
				logging.L().Warnf("cloudsync: diagnostic batch %s/%d acked but not cleared locally: %v", b.SessionID, b.Seq, derr)
			}
			continue
		}
		var ce *CloudError
		if errors.As(err, &ce) && (ce.Status == http.StatusConflict || ce.Status == http.StatusNotFound) {
			// Terminal for the whole SESSION (ADR-0092 §4): 409
			// session_not_active and 404 session_not_found both mean the
			// cloud will never accept another batch for this session id —
			// never retry against a dead session.
			logging.L().Infof("cloudsync: diagnostic session %s is no longer valid on the cloud (%d %s) — discarding its queue", b.SessionID, ce.Status, ce.Code)
			skipSession[b.SessionID] = true
			if cur, ok := diagnostics.Current(); ok && cur.ID == b.SessionID {
				if _, serr := diagnostics.Stop(ctx, kv, diagnostics.EndedRejected); serr != nil {
					logging.L().Warnf("cloudsync: diagnostics stop after %d: %v", ce.Status, serr)
				}
			} else {
				diagnostics.DrainSession(b.SessionID)
			}
			continue
		}
		if errors.As(err, &ce) && ce.Status == http.StatusBadRequest {
			// Terminal for this ONE BATCH only (ADR-0092 §4): the cloud's
			// own second-pass revalidation rejected it as malformed —
			// resending the identical bytes can never succeed. Discard just
			// this batch and move on to the next one in the queue (never
			// `return` here) so a single permanently-rejected batch cannot
			// block every batch queued behind it.
			logging.L().Warnf("cloudsync: diagnostic batch %s/%d rejected as invalid (%s) — discarding it, not retrying", b.SessionID, b.Seq, ce.Code)
			if derr := diagnostics.Discard(b); derr != nil {
				logging.L().Warnf("cloudsync: diagnostic batch %s/%d rejected but not cleared locally: %v", b.SessionID, b.Seq, derr)
			}
			continue
		}
		logging.L().Warnf("cloudsync: diagnostic batch %s/%d not uploaded (will retry): %v", b.SessionID, b.Seq, err)
		return
	}
}
