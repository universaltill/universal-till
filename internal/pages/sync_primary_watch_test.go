package pages

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const testNonce = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// proofLocalAddr is the address the fake main till "accepted" the
// connection on — what net/http puts under http.LocalAddrContextKey.
const proofLocalAddr = "192.0.2.20:8080"

func postProof(t *testing.T, mux http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postProofAt(t, mux, body, proofLocalAddr, proofLocalAddr)
}

// postProofAt sends the challenge with Host host to a till whose accepting
// socket is localAddr.
func postProofAt(t *testing.T, mux http.Handler, body, host, localAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, discovery.ProofPath, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Host = host
	if localAddr != "" {
		la, err := net.ResolveTCPAddr("tcp", localAddr)
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, la))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPrimaryProofAPI_AnswersForAnEnrolledTill(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Back office", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPrimaryProof(mux, dp)

	rec := postProof(t, mux, `{"till_id":"`+tillID+`","nonce":"`+testNonce+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data discovery.ProofResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	primaryID, err := discovery.TillID(ctx, data.NewSettingsRepo(dp.Db))
	if err != nil {
		t.Fatal(err)
	}
	if out.Data.PrimaryTillID != primaryID {
		t.Fatalf("primary_till_id = %q, want this till's discovery id %q", out.Data.PrimaryTillID, primaryID)
	}
	// The replica verifies with discovery.HashBearer of its own bearer —
	// it must be the very hash the primary stored at enrolment.
	if want := discovery.PrimaryProof(discovery.HashBearer("token-abc"), primaryID, tillID, testNonce, proofLocalAddr); out.Data.Proof != want {
		t.Fatal("proof does not verify against the replica's own bearer")
	}
	if strings.Contains(rec.Body.String(), hashBearer("token-abc")) {
		t.Fatal("response leaks the stored bearer hash")
	}
}

func TestPrimaryProofAPI_RefusesUnknownTillBadInputAndReplicas(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	tillID, _ := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Back office", hashBearer("token-abc"))
	mux := http.NewServeMux()
	registerPrimaryProof(mux, dp)

	if rec := postProof(t, mux, `{"till_id":"not-enrolled","nonce":"`+testNonce+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown till: status %d, want 404", rec.Code)
	}
	for _, bad := range []string{
		`{"till_id":"` + tillID + `","nonce":"short"}`,
		`{"till_id":"` + tillID + `","nonce":"` + strings.Repeat("zz", 32) + `"}`,
		`{"till_id":"","nonce":"` + testNonce + `"}`,
		`not json`,
	} {
		if rec := postProof(t, mux, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", bad, rec.Code)
		}
	}
	// A replica must never answer as a main till, even if its synced
	// tills roster happens to hold a matching row.
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.0.2.1:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postProof(t, mux, `{"till_id":"`+tillID+`","nonce":"`+testNonce+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("replica answered a proof: status %d", rec.Code)
	}
}

// ut-docs#2722 review: the proof binds the Host the replica dialled, so the
// main till must only answer for a Host that is its OWN address. A relay
// forwarding a replica's challenge can set Host to the relay's address (the
// one the replica expects the proof for) — this till must refuse that, not
// sign it.
func TestPrimaryProofAPI_OnlyAnswersForItsOwnAddress(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	tillID, _ := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Back office", hashBearer("token-abc"))
	mux := http.NewServeMux()
	registerPrimaryProof(mux, dp)
	body := `{"till_id":"` + tillID + `","nonce":"` + testNonce + `"}`
	orig := hostLookup
	t.Cleanup(func() { hostLookup = orig })
	hostLookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "till.local" {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.20")}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	for _, c := range []struct{ name, host, local string }{
		{"relay's address as Host", "192.0.2.66:8080", proofLocalAddr},
		{"own IP, another port", "192.0.2.20:9090", proofLocalAddr},
		{"no port in Host", "192.0.2.20", proofLocalAddr},
		{"hostname not resolving to this till", "relay.invalid:8080", proofLocalAddr},
		{"no local address known", proofLocalAddr, ""},
	} {
		if rec := postProofAt(t, mux, body, c.host, c.local); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 (body %s)", c.name, rec.Code, rec.Body.String())
		}
	}
	// A name that resolves to the accepting address is its own address
	// too (a replica paired via a hostname-based code).
	if rec := postProofAt(t, mux, body, "till.local:8080", proofLocalAddr); rec.Code != http.StatusOK {
		t.Fatalf("a name resolving to the accepting IP: status %d, want 200", rec.Code)
	}
}

func TestPrimaryProofAPI_RateLimited(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	mux := http.NewServeMux()
	registerPrimaryProof(mux, dp)
	limited := false
	for i := 0; i < 40; i++ {
		if postProof(t, mux, `{"till_id":"x","nonce":"`+testNonce+`"}`).Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("40 proof requests from one source in a burst were never rate-limited")
	}
}

func getMainTillChip(t *testing.T, dp *common.Deps) string {
	t.Helper()
	mux := http.NewServeMux()
	registerMainTillStatus(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/main-till-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("chip status %d", rec.Code)
	}
	return rec.Body.String()
}

// The whole ut-docs#2722 incident, in-process: two tills, the replica
// holding a dead primary_url (the main till moved port). The pull loop
// fails, the status chip appears, re-discovery finds the main till on the
// fake LAN, proves it, re-points primary_url, and the next tick syncs and
// clears the chip — bearer unchanged throughout.
func TestReplicaWithStaleURL_RecoversEndToEnd(t *testing.T) {
	chdirRoot(t)
	primary := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	tillID, err := data.NewTillsRepo(primary.Db).InsertTill(ctx, "Back office", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	primaryID, err := discovery.TillID(ctx, data.NewSettingsRepo(primary.Db))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSyncAdmin(mux, primary)
	registerPrimaryProof(mux, primary)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const dead = "http://127.0.0.1:1" // nothing listens on port 1
	replica := newPullTestReplica(t, dead)
	for k, v := range map[string]string{
		"sync.till_id":         tillID,
		"sync.last_contact_at": time.Now().Add(-9 * 24 * time.Hour).UTC().Format(time.RFC3339),
		// Pre-#2722 replica: no proven main-till id; its discovery id is
		// the main till's (copied by the join snapshot / admin pulls).
		discovery.TillIDSettingKey: primaryID,
	} {
		if err := replica.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	browses := 0
	var lan []discovery.Candidate // the main till isn't visible yet
	browse := func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		browses++
		return lan, nil
	}
	replica.PrimaryWatch = discovery.NewPrimaryWatch(replica.Settings, browse)

	// ut-docs#2742: a replica always shows its main-till chip; before any
	// failed contact (and with no link client) it reads "polling".
	if chip := getMainTillChip(t, replica); !strings.Contains(chip, `data-link-state="polling"`) {
		t.Fatalf("chip before any failed contact = %q, want polling", chip)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	syncPullTick(ctx, replica, client, func(context.Context) {})
	chip := getMainTillChip(t, replica)
	if !strings.Contains(chip, `href="/tills"`) || !strings.Contains(chip, "sb-main-till") ||
		!strings.Contains(chip, "Main till not reachable since") {
		t.Fatalf("no main-till chip linking to /tills after the main till went missing: %q", chip)
	}
	if got, _, _ := replica.Settings.Get(ctx, "sync.primary_url"); got != dead {
		t.Fatalf("primary_url changed with nothing on the LAN: %q", got)
	}

	// The main till shows up on the LAN — next to a device advertising a
	// different till id at the same address, which must never be the one
	// chosen. A fresh watch stands in for MinBrowseInterval having passed
	// (the rate limit itself is covered in internal/discovery).
	lan = []discovery.Candidate{
		{Name: "Other shop", TillID: "99999999-9999-4999-8999-999999999999", BaseURL: "http://127.0.0.1:2"},
		{Name: "Shop", TillID: primaryID, BaseURL: srv.URL},
	}
	replica.PrimaryWatch = discovery.NewPrimaryWatch(replica.Settings, browse)
	syncPullTick(ctx, replica, client, func(context.Context) {})
	if got, _, _ := replica.Settings.Get(ctx, "sync.primary_url"); got != srv.URL {
		t.Fatalf("primary_url = %q, want the re-discovered %q", got, srv.URL)
	}
	if got, _, _ := replica.Settings.Get(ctx, "sync.bearer"); got != "token-abc" {
		t.Fatalf("bearer changed to %q", got)
	}

	syncPullTick(ctx, replica, client, func(context.Context) {})
	last, _, _ := replica.Settings.Get(ctx, "sync.last_contact_at")
	if ts, err := time.Parse(time.RFC3339, last); err != nil || time.Since(ts) > time.Minute {
		t.Fatalf("sync.last_contact_at = %q after the recovered tick, want just now", last)
	}
	if chip := getMainTillChip(t, replica); strings.Contains(chip, "not reachable") ||
		!strings.Contains(chip, `data-link-state="polling"`) {
		t.Fatalf("chip after recovery = %q, want polling again", chip)
	}
	if got, _, _ := replica.Settings.Get(ctx, discovery.PrimaryTillIDSettingKey); got != primaryID {
		t.Fatalf("proven main-till id not stored: %q", got)
	}
	if browses == 0 {
		t.Fatal("never browsed")
	}
}

func TestMainTillChip_EmptyOnAPrimaryOrWithoutAWatch(t *testing.T) {
	chdirRoot(t)
	dp := newMigratedSyncDeps(t, "primary.db")
	if chip := getMainTillChip(t, dp); strings.TrimSpace(chip) != "" {
		t.Fatalf("chip on a till with no watch: %q", chip)
	}
	dp.PrimaryWatch = discovery.NewPrimaryWatch(dp.Settings, discovery.Browse)
	if chip := getMainTillChip(t, dp); strings.TrimSpace(chip) != "" {
		t.Fatalf("chip on a main till: %q", chip)
	}
}

// The chip links to /tills; that page must explain the same state and show
// when this replica last reached its main till.
func TestTillsPage_ReplicaShowsMainTillUnreachableNotice(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		"till.name":            "Front Counter",
		"sync.primary_url":     "http://127.0.0.1:1",
		"sync.last_contact_at": "2026-09-16T23:33:27Z",
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tills", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /tills = %d", rec.Code)
		}
		return rec.Body.String()
	}
	if body := get(); strings.Contains(body, "tills-main-unreachable") {
		t.Fatal("notice shown with no failed contact recorded")
	}
	dp.PrimaryWatch = discovery.NewPrimaryWatch(dp.Settings, func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		return nil, nil
	})
	dp.PrimaryWatch.ContactFailed(ctx) // last contact is days old: unreachable at once
	body := get()
	if !strings.Contains(body, "tills-main-unreachable") || !strings.Contains(body, "The main till isn&#39;t answering") {
		t.Fatalf("no unreachable notice on the replica's Tills page: %s", body)
	}
	// The main till's row carries this replica's last contact, not "—".
	if !strings.Contains(body, "2026") {
		t.Fatal("the main till's row doesn't show when this till last reached it")
	}
}
