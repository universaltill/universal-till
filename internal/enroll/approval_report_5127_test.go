package enroll

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#5127 (design §2, review M-1): the till reports its approval key to
// POST /v1/stores/devices/approval-key itself, because /sync carries only the
// kid. Best-effort and offline-first.

const reportStore = "store-report-1"

type reportCloud struct {
	srv    *httptest.Server
	hits   atomic.Int32
	status atomic.Int32
	mu     sync.Mutex
	auth   string
	path   string
	body   []byte
}

func newReportCloud(t *testing.T, status int) *reportCloud {
	t.Helper()
	c := &reportCloud{}
	c.status.Store(int32(status))
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.auth, c.path, c.body = r.Header.Get("Authorization"), r.URL.Path, b
		c.mu.Unlock()
		c.hits.Add(1)
		w.WriteHeader(int(c.status.Load()))
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// identify gives the package a paired identity (device, store, token).
func identify(t *testing.T) {
	t.Helper()
	resetState()
	resetApprovalReport()
	t.Cleanup(func() { resetState(); resetApprovalReport() })
	mu.Lock()
	cur = identity{DeviceID: "till-report-1", StoreID: reportStore, MerchantID: "m", Token: "dev-token"}
	mu.Unlock()
}

func marketplace(c *reportCloud) config.MarketplaceConfig {
	return config.MarketplaceConfig{EndpointURL: c.srv.URL + "/api"}
}

func TestApprovalKeyReportPostsAProofTheCloudAccepts(t *testing.T) {
	identify(t)
	c := newReportCloud(t, http.StatusOK)
	if got := reportApprovalKeyOnce(context.Background(), marketplace(c)); got != reportDone {
		t.Fatalf("outcome = %v, want done", got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != "/api/v1/stores/devices/approval-key" || c.auth != "Bearer dev-token" {
		t.Fatalf("path %q auth %q", c.path, c.auth)
	}
	var body struct {
		StoreID     string          `json:"store_id"`
		ApprovalKey approvalKeyWire `json:"approval_key"`
	}
	if err := json.Unmarshal(c.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.StoreID != reportStore {
		t.Fatalf("store_id = %q", body.StoreID)
	}
	// What the cloud's verifier does: the store line is the store's external
	// id on this endpoint (empty only on pair/register), assembled by hand.
	k := body.ApprovalKey
	msg := []byte("universaltill/approval-key/v1\ntill-report-1\n" + reportStore + "\n" + k.PublicKey)
	pub, err := base64.RawURLEncoding.DecodeString(k.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || k.KID != approvalkey.KID(pub) {
		t.Fatalf("key/kid: %v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(k.Pop)
	if err != nil || !ed25519.Verify(pub, msg, sig) {
		t.Fatalf("the cloud-side verification of the proof fails (%v)", err)
	}
}

func TestApprovalKeyReportAnswersAreHarmless(t *testing.T) {
	for _, st := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusTooManyRequests} {
		identify(t)
		c := newReportCloud(t, st)
		if got := reportApprovalKeyOnce(context.Background(), marketplace(c)); got != reportIgnored {
			t.Fatalf("status %d: outcome = %v, want ignored (no retry)", st, got)
		}
		if ApprovalKeyConflict() {
			t.Fatalf("status %d must not read as a conflict", st)
		}
	}
}

func TestApprovalKeyReportConflictIsRemembered(t *testing.T) {
	identify(t)
	c := newReportCloud(t, http.StatusConflict)
	if ApprovalKeyConflict() {
		t.Fatal("conflict before any report")
	}
	if got := reportApprovalKeyOnce(context.Background(), marketplace(c)); got != reportIgnored {
		t.Fatalf("outcome = %v", got)
	}
	if !ApprovalKeyConflict() {
		t.Fatal("a 409 must be remembered for Settings > Cloud")
	}
}

func TestApprovalKeyReportOfflineIsHarmlessAndRetried(t *testing.T) {
	identify(t)
	c := newReportCloud(t, http.StatusOK)
	m := marketplace(c)
	c.srv.Close() // offline
	if got := reportApprovalKeyOnce(context.Background(), m); got != reportRetry {
		t.Fatalf("outcome = %v, want retry", got)
	}
	if ApprovalKeyConflict() {
		t.Fatal("offline is not a conflict")
	}
}

func TestApprovalKeyReportWithoutCredentialsDoesNothing(t *testing.T) {
	resetState()
	resetApprovalReport()
	t.Cleanup(func() { resetState(); resetApprovalReport() })
	c := newReportCloud(t, http.StatusOK)
	if got := reportApprovalKeyOnce(context.Background(), marketplace(c)); got != reportSkipped || c.hits.Load() != 0 {
		t.Fatalf("outcome %v, hits %d: an unpaired till has nothing to report to", got, c.hits.Load())
	}
}

func TestKickReportsOncePerIdentityAndRetriesSlowly(t *testing.T) {
	identify(t)
	c := newReportCloud(t, http.StatusOK)
	m := marketplace(c)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		kickApprovalKeyReport(context.Background(), m, &wg)
	}
	wg.Wait()
	kickApprovalKeyReport(context.Background(), m, &wg) // after success: nothing more this start
	wg.Wait()
	if c.hits.Load() != 1 {
		t.Fatalf("hits = %d, want exactly 1 per start", c.hits.Load())
	}
	// A new identity (a re-pair) reports again.
	mu.Lock()
	cur.StoreID = "store-report-2"
	mu.Unlock()
	kickApprovalKeyReport(context.Background(), m, &wg)
	wg.Wait()
	if c.hits.Load() != 2 {
		t.Fatalf("hits = %d after a re-pair, want 2", c.hits.Load())
	}
}

func TestKickRetryAfterFailureIsSlowAndBounded(t *testing.T) {
	identify(t)
	old := approvalRetryDelays
	approvalRetryDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { approvalRetryDelays = old })
	c := newReportCloud(t, http.StatusInternalServerError)
	var wg sync.WaitGroup
	kickApprovalKeyReport(context.Background(), marketplace(c), &wg)
	wg.Wait()
	if c.hits.Load() != 3 { // first try + the two delays, then it stops
		t.Fatalf("hits = %d, want 3 (bounded retries)", c.hits.Load())
	}
}
