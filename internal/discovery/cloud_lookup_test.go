package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAddressCloud answers GET /v1/stores/main-till-address for store-1 /
// tok-1 with status and addr, counting requests.
type fakeAddressCloud struct {
	srv    *httptest.Server
	hits   atomic.Int32
	status atomic.Int32
	addr   atomic.Value // string
	bad    atomic.Bool  // the request didn't look like the contract
}

func newFakeAddressCloud(t *testing.T, status int, addr string) *fakeAddressCloud {
	t.Helper()
	c := &fakeAddressCloud{}
	c.addr.Store(addr)
	c.status.Store(int32(status))
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != CloudLookupPath ||
			r.URL.Query().Get("store_id") != "store-1" || r.Header.Get("Authorization") != "Bearer tok-1" {
			c.bad.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		st := int(c.status.Load())
		w.WriteHeader(st)
		if st == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"lan_address": c.addr.Load()}, "error": nil})
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func creds(endpoint string) CloudCredentials {
	return func() (string, string, string) { return endpoint, "store-1", "tok-1" }
}

func TestCloudLookup_ReturnsTheStoresMainTillAddress(t *testing.T) {
	cloud := newFakeAddressCloud(t, http.StatusOK, "192.168.1.20:37673")
	lookup := NewCloudLookup(creds(cloud.srv.URL))
	addr, err := lookup(context.Background())
	if err != nil || addr != "192.168.1.20:37673" {
		t.Fatalf("lookup = %q, %v; want the reported address", addr, err)
	}
	if cloud.bad.Load() {
		t.Fatal("request did not carry GET, the path, store_id and the device bearer")
	}
}

// A cloud that predates the endpoint answers 404: no candidate, no error
// noise, and it is not asked again for a while (like checkin.go's
// checkinRetryOld), so an old cloud isn't asked every five minutes.
func TestCloudLookup_OldCloudIsNotAskedAgainForAnHour(t *testing.T) {
	cloud := newFakeAddressCloud(t, http.StatusNotFound, "")
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	lookup := newCloudLookup(creds(cloud.srv.URL), func() time.Time { return now })

	if addr, err := lookup(context.Background()); addr != "" || err != nil {
		t.Fatalf("old cloud lookup = %q, %v; want empty and no error", addr, err)
	}
	if addr, _ := lookup(context.Background()); addr != "" || cloud.hits.Load() != 1 {
		t.Fatalf("asked an old cloud again straight away (hits %d)", cloud.hits.Load())
	}
	now = now.Add(cloudLookupRetryOld)
	cloud.status.Store(http.StatusOK)
	cloud.addr.Store("192.168.1.20:8080")
	if addr, err := lookup(context.Background()); addr != "192.168.1.20:8080" || err != nil {
		t.Fatalf("after the retry window lookup = %q, %v", addr, err)
	}
}

// 401/429/5xx are not "old cloud": an error for the caller's info log, and
// the next re-discovery asks again.
func TestCloudLookup_TransientRefusalsAreErrorsAndRetried(t *testing.T) {
	for _, st := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		cloud := newFakeAddressCloud(t, st, "")
		lookup := NewCloudLookup(creds(cloud.srv.URL))
		if addr, err := lookup(context.Background()); addr != "" || err == nil {
			t.Fatalf("status %d: lookup = %q, %v; want empty and an error", st, addr, err)
		}
		_, _ = lookup(context.Background())
		if cloud.hits.Load() != 2 {
			t.Fatalf("status %d: hits = %d, want 2 (asked again)", st, cloud.hits.Load())
		}
	}
}

// A till not enrolled with the cloud never makes the call.
func TestCloudLookup_NotEnrolledMakesNoRequest(t *testing.T) {
	cloud := newFakeAddressCloud(t, http.StatusOK, "192.168.1.20:8080")
	for _, c := range []CloudCredentials{
		func() (string, string, string) { return "", "store-1", "tok-1" },
		func() (string, string, string) { return cloud.srv.URL, "", "tok-1" },
		func() (string, string, string) { return cloud.srv.URL, "store-1", "" },
	} {
		if addr, err := NewCloudLookup(c)(context.Background()); addr != "" || err != nil {
			t.Fatalf("unenrolled lookup = %q, %v", addr, err)
		}
	}
	if cloud.hits.Load() != 0 {
		t.Fatalf("an unenrolled till called the cloud %d times", cloud.hits.Load())
	}
}

// The redirect a hostile proxy might answer with is not followed: the
// bearer goes to the configured cloud only.
func TestCloudLookup_DoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	t.Cleanup(other.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	if addr, _ := NewCloudLookup(creds(redir.URL))(context.Background()); addr != "" {
		t.Fatalf("lookup = %q through a redirect", addr)
	}
	if elsewhere.Load() != 0 {
		t.Fatal("followed a redirect with the device bearer")
	}
}
