package offsitebackup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeCloud is an httptest server derived from the design note's section 3,
// holding backups in memory. It checks what the real cloud checks (bearer,
// store id, Content-Length, part hash, part 1 header) so the till is tested
// against the contract, not against its own assumptions.
type fakeCloud struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	token, storeID string
	partMax        int64
	maxBytes       int64

	// the escrowed shop key (K2): the real cloud keeps it wrapped; the fake
	// keeps the bytes so it can answer a release.
	keyGen        int
	keyFP         string
	keyDEK        []byte
	putKeys       int
	escrowBlocked bool
	deletes       int
	generations   int
	keyReleases   int // polls that released the DEK
	lastHave      *HaveKey

	backups  map[string]*fakeBackup
	requests map[string]*fakeRestoreReq
	order    []string // backup ids, oldest first

	// failure injection
	beginStatus   int // answer begin with this status
	beginCode     string
	putFailures   int // fail this many part PUTs with 500
	putCalls      int
	begins        int
	statusCalls   int
	partDownloads int
	rejectPutN    int // answer PUT of this part with 404 (gone), 0 = off
}

type fakeBackup struct {
	id       string
	parts    map[int][]byte
	complete bool
	begin    BeginRequest
	cbytes   int64
}

type fakeRestoreReq struct {
	id, backupID, code string
	status             string
	kind               string // "restore" or "key"
	have               *HaveKey
	polls              int
	approveAfter       int // polls before it turns approved
}

func newFakeCloud(t *testing.T) *fakeCloud {
	f := &fakeCloud{t: t, token: "tok-123", storeID: "st_1", partMax: 8 << 20, maxBytes: 2 << 30,
		backups: map[string]*fakeBackup{}, requests: map[string]*fakeRestoreReq{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/stores/backups", f.begin)
	mux.HandleFunc("GET /v1/stores/backups", f.list)
	mux.HandleFunc("PUT /v1/stores/backups/key", f.putKey)
	mux.HandleFunc("GET /v1/stores/backups/key", f.getKey)
	mux.HandleFunc("DELETE /v1/stores/backups/key/escrow", func(w http.ResponseWriter, _ *http.Request) { f.deletes++; w.WriteHeader(204) })
	mux.HandleFunc("POST /v1/stores/backups/key-requests", f.keyRequest)
	mux.HandleFunc("PUT /v1/stores/backups/{id}/parts/{n}", f.putPart)
	mux.HandleFunc("GET /v1/stores/backups/{id}/parts/{n}", f.getPart)
	mux.HandleFunc("GET /v1/stores/backups/restore-requests/{rid}", f.poll)
	mux.HandleFunc("GET /v1/stores/backups/{id}", f.status)
	mux.HandleFunc("POST /v1/stores/backups/{id}/complete", f.complete)
	mux.HandleFunc("POST /v1/stores/backups/{id}/restore-requests", f.restoreRequest)
	mux.HandleFunc("POST /v1/stores/backups/restore-requests/{rid}/result", f.result)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloud) cloud() *HTTPCloud {
	return &HTTPCloud{HTTP: f.srv.Client(), Resolve: func() Conn {
		return Conn{BaseURL: f.srv.URL, StoreID: f.storeID, Token: f.token}
	}}
}

func apiErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func (f *fakeCloud) auth(w http.ResponseWriter, r *http.Request, bodyStore string) bool {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		apiErr(w, 401, "unauthorized")
		return false
	}
	sid := bodyStore
	if sid == "" {
		sid = r.URL.Query().Get("store_id")
	}
	if sid != f.storeID {
		apiErr(w, 403, "forbidden")
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (f *fakeCloud) begin(w http.ResponseWriter, r *http.Request) {
	var req BeginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.auth(w, r, req.StoreID) {
		return
	}
	f.begins++
	if f.beginStatus != 0 {
		apiErr(w, f.beginStatus, f.beginCode)
		return
	}
	if req.SnapshotBytes > f.maxBytes {
		apiErr(w, 413, "too_large")
		return
	}
	if f.keyGen == 0 {
		apiErr(w, 409, "key_required")
		return
	}
	if req.DEKGeneration != f.keyGen || req.KeyFingerprint != f.keyFP {
		apiErr(w, 409, "key_mismatch")
		return
	}
	b := &fakeBackup{id: newUUID(), parts: map[int][]byte{}, begin: req}
	f.backups[b.id] = b
	f.order = append(f.order, b.id)
	// No key in the answer (K3).
	writeJSON(w, 201, map[string]any{"backup_id": b.id, "dek_generation": f.keyGen,
		"part_max_bytes": f.partMax, "max_bytes": f.maxBytes, "expires_at": "2099-01-01T00:00:00Z"})
}

func (f *fakeCloud) putKey(w http.ResponseWriter, r *http.Request) {
	// The real cloud reads store_id from the JSON body of PUT /key (no query
	// fallback), so the fake demands it too: a till that omits it gets 400.
	var in KeyPut
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.StoreID == "" {
		apiErr(w, 400, "invalid_request")
		return
	}
	if !f.auth(w, r, in.StoreID) {
		return
	}
	var dek []byte
	if in.DEKB64 != "" {
		var err error
		dek, err = base64.StdEncoding.DecodeString(in.DEKB64)
		if err != nil || len(dek) != KeySize || Fingerprint(dek) != in.Fingerprint {
			apiErr(w, 400, "invalid_request")
			return
		}
	} else if len(in.Fingerprint) != 32 {
		apiErr(w, 400, "invalid_request")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putKeys++
	if dek != nil && f.escrowBlocked {
		apiErr(w, 409, "escrow_blocked")
		return
	}
	switch {
	case f.keyGen == 0:
		f.keyGen, f.keyFP, f.keyDEK = in.Generation, in.Fingerprint, dek
		w.WriteHeader(201)
	case f.keyGen == in.Generation && f.keyFP == in.Fingerprint:
		if dek != nil {
			f.keyDEK = dek // escrow turned on
		}
		w.WriteHeader(200)
	case in.Generation == f.keyGen+1:
		f.keyGen, f.keyFP, f.keyDEK = in.Generation, in.Fingerprint, dek
		f.generations++
		w.WriteHeader(201)
	default:
		writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "key_exists", "message": "key_exists"},
			"data": map[string]any{"current_generation": f.keyGen, "key_in_cloud": f.keyDEK != nil}})
	}
}

func (f *fakeCloud) getKey(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyGen == 0 {
		writeJSON(w, 200, map[string]any{"generation": nil, "fingerprint": nil, "key_in_cloud": false, "escrow_blocked": f.escrowBlocked})
		return
	}
	writeJSON(w, 200, map[string]any{"generation": f.keyGen, "fingerprint": f.keyFP, "key_in_cloud": f.keyDEK != nil, "escrow_blocked": f.escrowBlocked})
}

func (f *fakeCloud) keyRequest(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	_ = json.NewDecoder(r.Body).Decode(&in)
	if !f.auth(w, r, in["store_id"]) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyDEK == nil {
		apiErr(w, 409, "no_key_in_cloud")
		return
	}
	rq := &fakeRestoreReq{id: newUUID(), code: "LINK2X9P", status: "pending", kind: "key", approveAfter: 1}
	f.requests[rq.id] = rq
	writeJSON(w, 201, RestoreRequestResponse{RequestID: rq.id, Code: rq.code,
		ApproveURL: "https://my.example.test/en-gb/restore?code=" + rq.code, ExpiresAt: "2099-01-01T00:00:00Z"})
}

func (f *fakeCloud) putPart(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.putCalls++
	fail := f.putFailures > 0
	if fail {
		f.putFailures--
	}
	b := f.backups[r.PathValue("id")]
	f.mu.Unlock()
	if !f.auth(w, r, "") {
		return
	}
	if fail {
		apiErr(w, 500, "internal")
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	if b == nil || (f.rejectPutN == n && n != 0) {
		apiErr(w, 404, "backup_not_found")
		return
	}
	if r.ContentLength < 0 {
		apiErr(w, 411, "length_required")
		return
	}
	if r.ContentLength > f.partMax {
		apiErr(w, 413, "part_too_large")
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, f.partMax))
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != r.Header.Get("X-UT-Part-SHA256") {
		apiErr(w, 422, "sha256_mismatch")
		return
	}
	if n == 1 {
		if _, _, err := ParseHeader(body); err != nil {
			apiErr(w, 422, "bad_header")
			return
		}
		if !bytes.Equal(body[8:24], uuidBytes(b.id)) {
			apiErr(w, 422, "bad_header")
			return
		}
	}
	f.mu.Lock()
	b.parts[n] = body
	f.mu.Unlock()
	writeJSON(w, 200, map[string]any{"n": n, "bytes": len(body), "sha256": hex.EncodeToString(sum[:])})
}

func uuidBytes(s string) []byte {
	b, _ := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return b
}

func (b *fakeBackup) partList() []PartInfo {
	var out []PartInfo
	for n := 1; ; n++ {
		p, ok := b.parts[n]
		if !ok {
			break
		}
		sum := sha256.Sum256(p)
		out = append(out, PartInfo{N: n, Bytes: int64(len(p)), SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

func (f *fakeCloud) status(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	b := f.backups[r.PathValue("id")]
	if b == nil {
		apiErr(w, 404, "backup_not_found")
		return
	}
	st := "pending"
	if b.complete {
		st = "complete"
	}
	writeJSON(w, 200, BackupStatus{Status: st, Parts: b.partList()})
}

func (f *fakeCloud) complete(w http.ResponseWriter, r *http.Request) {
	var req CompleteRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if !f.auth(w, r, req.StoreID) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.backups[r.PathValue("id")]
	if b == nil {
		apiErr(w, 404, "backup_not_found")
		return
	}
	var sum int64
	pl := b.partList()
	for _, p := range pl {
		sum += p.Bytes
	}
	if len(pl) != req.Parts || sum != req.CiphertextBytes {
		apiErr(w, 409, "parts_incomplete")
		return
	}
	b.complete, b.cbytes = true, sum
	writeJSON(w, 200, map[string]any{"backup_id": b.id, "completed_at": "2026-11-16T03:00:00Z"})
}

func (f *fakeCloud) list(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := ListResponse{Entitled: true, Backups: []BackupEntry{}}
	for i := len(f.order) - 1; i >= 0; i-- {
		b := f.backups[f.order[i]]
		if !b.complete {
			continue
		}
		out.Backups = append(out.Backups, BackupEntry{BackupID: b.id, TakenAt: b.begin.TakenAt, CompletedAt: "2026-11-16T03:00:00Z",
			CiphertextBytes: b.cbytes, PlaintextBytes: b.begin.SnapshotBytes, AppVersion: b.begin.AppVersion,
			SchemaVersion: b.begin.SchemaVersion, Tier: "daily"})
	}
	writeJSON(w, 200, out)
}

func (f *fakeCloud) restoreRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StoreID string   `json:"store_id"`
		HaveKey *HaveKey `json:"have_key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if !f.auth(w, r, in.StoreID) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastHave = in.HaveKey
	b := f.backups[r.PathValue("id")]
	if b == nil || !b.complete {
		apiErr(w, 404, "backup_not_found")
		return
	}
	rq := &fakeRestoreReq{id: newUUID(), backupID: b.id, code: "K7QX29PM", status: "pending", kind: "restore", have: in.HaveKey, approveAfter: 1}
	f.requests[rq.id] = rq
	writeJSON(w, 201, RestoreRequestResponse{RequestID: rq.id, Code: rq.code,
		ApproveURL: "https://my.example.test/en-gb/restore?code=" + rq.code, ExpiresAt: "2099-01-01T00:00:00Z"})
}

func (f *fakeCloud) poll(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rq := f.requests[r.PathValue("rid")]
	if rq == nil {
		apiErr(w, 404, "not_found")
		return
	}
	rq.polls++
	if rq.status == "pending" && rq.polls > rq.approveAfter {
		rq.status = "approved"
	}
	if rq.status == "approved" {
		rq.status = "consumed" // the key is released once
		release := rq.kind == "key" || rq.have == nil || rq.have.Generation != f.keyGen || rq.have.Fingerprint != f.keyFP
		out := RestorePoll{Status: "consumed", DownloadExpiresAt: "2099-01-01T00:00:00Z"}
		if rq.kind == "restore" {
			b := f.backups[rq.backupID]
			out.BackupID, out.Parts = b.id, b.partList()
		}
		if release && f.keyDEK == nil {
			no := false
			out.KeyReleased, out.KeyInCloud, out.DEKFingerprint = &no, &no, f.keyFP
		} else if release {
			f.keyReleases++
			out.DEKB64, out.DEKGeneration = base64.StdEncoding.EncodeToString(f.keyDEK), f.keyGen
		} else {
			no := false
			out.KeyReleased = &no
		}
		writeJSON(w, 200, out)
		return
	}
	writeJSON(w, 200, RestorePoll{Status: rq.status})
}

func (f *fakeCloud) getPart(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rq := f.requests[r.URL.Query().Get("request_id")]
	if rq == nil || rq.status != "consumed" {
		apiErr(w, 403, "restore_not_approved")
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	p, ok := f.backups[r.PathValue("id")].parts[n]
	if !ok {
		apiErr(w, 404, "backup_not_found")
		return
	}
	f.partDownloads++
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(p)))
	_, _ = w.Write(p)
}

func (f *fakeCloud) result(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	_ = json.NewDecoder(r.Body).Decode(&in)
	if !f.auth(w, r, in["store_id"]) {
		return
	}
	f.mu.Lock()
	f.requests[r.PathValue("rid")].status = "result:" + in["outcome"] + ":" + in["error_code"]
	f.mu.Unlock()
	w.WriteHeader(204)
}

// plaintextOf reassembles a completed backup's parts and decrypts it.
func (f *fakeCloud) decrypt(id string) ([]byte, error) { return f.decryptWith(id, f.keyDEK) }

func (f *fakeCloud) decryptWith(id string, dek []byte) ([]byte, error) {
	b := f.backups[id]
	key, err := DeriveKey(dek, [16]byte(uuidBytes(id)), f.storeID)
	if err != nil {
		return nil, err
	}
	var ct bytes.Buffer
	for _, p := range b.partList() {
		ct.Write(b.parts[p.N])
	}
	var out bytes.Buffer
	if err := Open(&out, key, [16]byte(uuidBytes(id)), &ct); err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return out.Bytes(), nil
}
