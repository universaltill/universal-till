package offsitebackup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
)

// PartTimeout is the overall timeout of one part upload or download.
const PartTimeout = 180 * time.Second

// BeginRequest is POST /v1/stores/backups (contract §3.1).
type BeginRequest struct {
	StoreID       string `json:"store_id"`
	SnapshotBytes int64  `json:"snapshot_bytes"`
	TakenAt       string `json:"taken_at"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	Format        int    `json:"format"`
	// DEKGeneration and KeyFingerprint name the till's key; the key itself
	// never goes in a begin (design section 14, K3).
	DEKGeneration  int    `json:"dek_generation"`
	KeyFingerprint string `json:"key_fingerprint"`
}

// BeginResponse is the answer to begin. It carries no key: the till derives
// the per-backup key from its own shop key (DeriveKey).
type BeginResponse struct {
	BackupID      string `json:"backup_id"`
	DEKGeneration int    `json:"dek_generation"`
	PartMaxBytes  int64  `json:"part_max_bytes"`
	MaxBytes      int64  `json:"max_bytes"`
	ExpiresAt     string `json:"expires_at"`
}

// PartInfo is one stored part as the cloud lists it.
type PartInfo struct {
	N      int    `json:"n"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// BackupStatus is GET /v1/stores/backups/{id}.
type BackupStatus struct {
	Status string     `json:"status"`
	Parts  []PartInfo `json:"parts"`
}

// CompleteRequest is POST .../complete.
type CompleteRequest struct {
	StoreID         string `json:"store_id"`
	Parts           int    `json:"parts"`
	CiphertextBytes int64  `json:"ciphertext_bytes"`
	PlaintextBytes  int64  `json:"plaintext_bytes"`
}

// BackupEntry is one row of the list (contract §3.2).
type BackupEntry struct {
	BackupID        string `json:"backup_id"`
	TakenAt         string `json:"taken_at"`
	CompletedAt     string `json:"completed_at"`
	CiphertextBytes int64  `json:"ciphertext_bytes"`
	PlaintextBytes  int64  `json:"plaintext_bytes"`
	AppVersion      string `json:"app_version"`
	SchemaVersion   int    `json:"schema_version"`
	Tier            string `json:"tier"`
}

// ListResponse is GET /v1/stores/backups.
type ListResponse struct {
	Backups     []BackupEntry `json:"backups"`
	Entitled    bool          `json:"entitled"`
	DeleteAfter *string       `json:"delete_after"`
}

// RestoreRequestResponse is the answer to creating a restore request.
type RestoreRequestResponse struct {
	RequestID  string `json:"request_id"`
	Code       string `json:"code"`
	ApproveURL string `json:"approve_url"`
	ExpiresAt  string `json:"expires_at"`
}

// HaveKey tells the cloud which key this till already holds (K4); the cloud
// then releases nothing it need not.
type HaveKey struct {
	Generation  int    `json:"generation"`
	Fingerprint string `json:"fingerprint"`
}

// KeyState is GET /v1/stores/backups/key: which key the cloud holds. No key
// material, ever.
type KeyState struct {
	Generation  *int    `json:"generation"`
	Fingerprint *string `json:"fingerprint"`
	// InCloud: a wrapped copy of the key is held. EscrowBlocked: the owner
	// switched the cloud copy off on my. and the cloud refuses a new one.
	InCloud       bool `json:"key_in_cloud"`
	EscrowBlocked bool `json:"escrow_blocked"`
}

// KeyPut is the body of PUT /v1/stores/backups/key. Without DEKB64 it only
// registers the key's fingerprint (no cloud copy, E1); with it the cloud also
// keeps a wrapped copy.
type KeyPut struct {
	// StoreID is filled in by HTTPCloud.PutKey: the cloud reads it from the
	// body of PUT /key, not from the query.
	StoreID     string `json:"store_id"`
	Generation  int    `json:"generation"`
	DEKB64      string `json:"dek_b64,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// RestorePoll is the answer to polling one. The key fields and Parts are only
// present on the one call that consumes the approval. DEKB64 is the shop key
// released to this till; KeyReleased false means the till already holds it.
type RestorePoll struct {
	Status        string `json:"status"`
	DEKB64        string `json:"dek_b64,omitempty"`
	DEKGeneration int    `json:"dek_generation,omitempty"`
	KeyReleased   *bool  `json:"key_released,omitempty"`
	// KeyInCloud false with KeyReleased false: the till lacks the key and the
	// cloud holds no copy; the owner's saved key is needed (E5). DEKFingerprint
	// is the key's fingerprint, to check a typed key against.
	KeyInCloud        *bool      `json:"key_in_cloud,omitempty"`
	DEKFingerprint    string     `json:"dek_fingerprint,omitempty"`
	BackupID          string     `json:"backup_id,omitempty"`
	Parts             []PartInfo `json:"parts,omitempty"`
	DownloadExpiresAt string     `json:"download_expires_at,omitempty"`
}

// Cloud is the till's view of the §3 API; tests fake it with httptest.
type Cloud interface {
	Begin(ctx context.Context, req BeginRequest) (BeginResponse, error)
	PutPart(ctx context.Context, backupID string, n int, body []byte, sha256hex string) error
	Status(ctx context.Context, backupID string) (BackupStatus, error)
	Complete(ctx context.Context, backupID string, req CompleteRequest) error
	List(ctx context.Context) (ListResponse, error)
	RequestRestore(ctx context.Context, backupID string, have *HaveKey) (RestoreRequestResponse, error)
	GetKey(ctx context.Context) (KeyState, error)
	PutKey(ctx context.Context, k KeyPut) error
	RequestKeyLink(ctx context.Context) (RestoreRequestResponse, error)
	PollRestore(ctx context.Context, requestID string) (RestorePoll, error)
	GetPart(ctx context.Context, backupID string, n int, requestID string) (io.ReadCloser, error)
	Result(ctx context.Context, requestID, outcome, errorCode string) error
}

// APIError is a non-2xx answer. Code is the {"error":{"code"}} value.
type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
	// CurrentGeneration and KeyInCloud come with a 409 key_exists: the key
	// generation the cloud holds and whether it has a wrapped copy.
	CurrentGeneration int
	KeyInCloud        *bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("offsitebackup: cloud answered %d %s", e.Status, e.Code)
}

// Conn is the shop's cloud connection, resolved per call so a rotated
// credential is picked up.
type Conn struct {
	BaseURL string
	StoreID string
	Token   string
}

// HTTPCloud implements Cloud over HTTP.
type HTTPCloud struct {
	Resolve func() Conn
	HTTP    *http.Client
}

var errNotConnected = errors.New("offsitebackup: this till is not connected to a cloud shop")

func (c *HTTPCloud) newReq(ctx context.Context, method, path string, q url.Values, body io.Reader, ctype string) (*http.Request, Conn, error) {
	cn := c.Resolve()
	if cn.BaseURL == "" || cn.StoreID == "" || cn.Token == "" {
		return nil, cn, errNotConnected
	}
	if q == nil {
		q = url.Values{}
	}
	if method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete {
		q.Set("store_id", cn.StoreID)
	}
	u := strings.TrimRight(cn.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, cn, err
	}
	req.Header.Set("Authorization", "Bearer "+cn.Token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return req, cn, nil
}

func (c *HTTPCloud) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, decodeAPIError(resp)
	}
	return resp, nil
}

func decodeAPIError(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
	}
	var env struct {
		Error struct {
			Code              string `json:"code"`
			Message           string `json:"message"`
			CurrentGeneration int    `json:"current_generation"`
			KeyInCloud        *bool  `json:"key_in_cloud"`
		} `json:"error"`
		// The cloud puts the details of a 409 key_exists in data.
		Data struct {
			CurrentGeneration int   `json:"current_generation"`
			KeyInCloud        *bool `json:"key_in_cloud"`
		} `json:"data"`
	}
	if b, err := io.ReadAll(io.LimitReader(resp.Body, 4096)); err == nil && json.Unmarshal(b, &env) == nil {
		e.Code, e.Message = env.Error.Code, env.Error.Message
		e.CurrentGeneration, e.KeyInCloud = env.Error.CurrentGeneration, env.Error.KeyInCloud
		if env.Data.CurrentGeneration != 0 || env.Data.KeyInCloud != nil {
			e.CurrentGeneration, e.KeyInCloud = env.Data.CurrentGeneration, env.Data.KeyInCloud
		}
	}
	return e
}

func (c *HTTPCloud) jsonCall(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	ctype := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	req, _, err := c.newReq(ctx, method, path, q, body, ctype)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	// The answers are small; the cap stops a misbehaving endpoint.
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (c *HTTPCloud) Begin(ctx context.Context, r BeginRequest) (BeginResponse, error) {
	cn := c.Resolve()
	r.StoreID = cn.StoreID
	var out BeginResponse
	err := c.jsonCall(ctx, http.MethodPost, "/v1/stores/backups", nil, r, &out)
	return out, err
}

func (c *HTTPCloud) PutPart(ctx context.Context, backupID string, n int, body []byte, sha string) error {
	ctx, cancel := context.WithTimeout(ctx, PartTimeout)
	defer cancel()
	req, _, err := c.newReq(ctx, http.MethodPut, "/v1/stores/backups/"+url.PathEscape(backupID)+"/parts/"+strconv.Itoa(n), nil, bytes.NewReader(body), "application/octet-stream")
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("X-UT-Part-SHA256", sha)
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.Body.Close()
}

func (c *HTTPCloud) Status(ctx context.Context, backupID string) (BackupStatus, error) {
	var out BackupStatus
	err := c.jsonCall(ctx, http.MethodGet, "/v1/stores/backups/"+url.PathEscape(backupID), nil, nil, &out)
	return out, err
}

func (c *HTTPCloud) Complete(ctx context.Context, backupID string, r CompleteRequest) error {
	r.StoreID = c.Resolve().StoreID
	return c.jsonCall(ctx, http.MethodPost, "/v1/stores/backups/"+url.PathEscape(backupID)+"/complete", nil, r, nil)
}

func (c *HTTPCloud) List(ctx context.Context) (ListResponse, error) {
	var out ListResponse
	err := c.jsonCall(ctx, http.MethodGet, "/v1/stores/backups", nil, nil, &out)
	return out, err
}

func (c *HTTPCloud) RequestRestore(ctx context.Context, backupID string, have *HaveKey) (RestoreRequestResponse, error) {
	var out RestoreRequestResponse
	in := struct {
		StoreID string   `json:"store_id"`
		HaveKey *HaveKey `json:"have_key,omitempty"`
	}{c.Resolve().StoreID, have}
	err := c.jsonCall(ctx, http.MethodPost, "/v1/stores/backups/"+url.PathEscape(backupID)+"/restore-requests", nil, in, &out)
	return out, err
}

func (c *HTTPCloud) GetKey(ctx context.Context) (KeyState, error) {
	var out KeyState
	err := c.jsonCall(ctx, http.MethodGet, "/v1/stores/backups/key", nil, nil, &out)
	return out, err
}

func (c *HTTPCloud) PutKey(ctx context.Context, k KeyPut) error {
	k.StoreID = c.Resolve().StoreID
	return c.jsonCall(ctx, http.MethodPut, "/v1/stores/backups/key", nil, k, nil)
}

// RequestKeyLink asks the owner to give this till the shop's backup key (K5).
// It is polled like a restore request.
func (c *HTTPCloud) RequestKeyLink(ctx context.Context) (RestoreRequestResponse, error) {
	var out RestoreRequestResponse
	in := map[string]string{"store_id": c.Resolve().StoreID}
	err := c.jsonCall(ctx, http.MethodPost, "/v1/stores/backups/key-requests", nil, in, &out)
	return out, err
}

func (c *HTTPCloud) PollRestore(ctx context.Context, requestID string) (RestorePoll, error) {
	var out RestorePoll
	err := c.jsonCall(ctx, http.MethodGet, "/v1/stores/backups/restore-requests/"+url.PathEscape(requestID), nil, nil, &out)
	return out, err
}

func (c *HTTPCloud) GetPart(ctx context.Context, backupID string, n int, requestID string) (io.ReadCloser, error) {
	req, _, err := c.newReq(ctx, http.MethodGet, "/v1/stores/backups/"+url.PathEscape(backupID)+"/parts/"+strconv.Itoa(n), url.Values{"request_id": {requestID}}, nil, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *HTTPCloud) Result(ctx context.Context, requestID, outcome, errorCode string) error {
	in := map[string]string{"store_id": c.Resolve().StoreID, "outcome": outcome, "error_code": errorCode}
	return c.jsonCall(ctx, http.MethodPost, "/v1/stores/backups/restore-requests/"+url.PathEscape(requestID)+"/result", nil, in, nil)
}

func newStoreClient() *http.Client { return cloudsync.NewStoreClient(PartTimeout + 30*time.Second) }
