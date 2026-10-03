package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#2774: when mDNS finds nothing usable, a stranded replica asks the
// cloud where its main till is. The answer is one more candidate for the
// SAME proof loop — never a shortcut to sync.primary_url.

// hostOf is a test server's host:port, the shape the cloud returns.
func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// withCloud installs a fake cloud lookup on f's watch that answers addr (or
// err) and counts its calls.
func withCloud(f *watchFixture, addr string, err error) *atomic.Int32 {
	var calls atomic.Int32
	f.w.SetCloudLookup(func(context.Context) (string, error) {
		calls.Add(1)
		return addr, err
	})
	return &calls
}

// mDNS is blocked (AP isolation, a VLAN between the tills) but the cloud
// knows where the main till is: the replica challenges that address, the
// main till proves it holds this replica's pairing, and only then does
// sync.primary_url move.
func TestPrimaryWatch_CloudCandidateRelinksWhenLANFindsNothing(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t) // the LAN browse finds nothing
	calls := withCloud(f, hostOf(t, primary.srv.URL), nil)
	ctx := context.Background()

	out := f.w.ContactFailed(ctx) // last contact is days old: unreachable at once
	if !out.Relinked || out.NewURL != primary.srv.URL {
		t.Fatalf("outcome = %+v, want relinked to %s via the cloud's answer", out, primary.srv.URL)
	}
	if calls.Load() != 1 {
		t.Fatalf("cloud lookups = %d, want 1", calls.Load())
	}
	if primary.hits.Load() == 0 {
		t.Fatal("the cloud's candidate was never challenged")
	}
	if got := f.get(t, "sync.primary_url"); got != primary.srv.URL {
		t.Fatalf("sync.primary_url = %q, want %q", got, primary.srv.URL)
	}
	if primary.sawBearer.Load() {
		t.Fatal("the bearer was sent to the cloud's candidate before it proved itself")
	}
}

// The load-bearing property: a cloud answer is never trusted on its own. A
// device at the cloud-supplied address that cannot produce the proof (a
// stale record, a re-installed main till — or a compromised cloud) changes
// nothing.
func TestPrimaryWatch_CloudCandidateFailingProofNeverSwitches(t *testing.T) {
	spoof := newFakePrimary(t, testPrimaryID, "not-the-real-bearer-hash")
	f := newWatchFixture(t)
	calls := withCloud(f, hostOf(t, spoof.srv.URL), nil)
	ctx := context.Background()

	for i := 0; i < UnreachableThreshold; i++ {
		if out := f.w.ContactFailed(ctx); out.Relinked {
			t.Fatalf("relinked to a cloud-supplied address that failed the proof: %+v", out)
		}
	}
	if calls.Load() == 0 || spoof.hits.Load() == 0 {
		t.Fatalf("cloud lookups = %d, challenges = %d — the test proves nothing unless both happened", calls.Load(), spoof.hits.Load())
	}
	if got := f.get(t, "sync.primary_url"); got != deadURL {
		t.Fatalf("sync.primary_url = %q, want unchanged (%q)", got, deadURL)
	}
	if got := f.get(t, PrimaryTillIDSettingKey); got != testPrimaryID {
		t.Fatalf("%s = %q, want unchanged", PrimaryTillIDSettingKey, got)
	}
	if spoof.sawBearer.Load() {
		t.Fatal("the bearer was sent to an unproven cloud-supplied address")
	}
}

// Same relay attack as TestPrimaryWatch_RelayedProofDoesNotSwitch, arriving
// through the cloud instead of mDNS: the proof binds the address dialled,
// so a relay of the real main till's genuine proof never verifies.
func TestPrimaryWatch_CloudCandidateRelayNeverSwitches(t *testing.T) {
	real := newFakePrimary(t, testPrimaryID, testBearerHash())
	var relayed atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Post(real.srv.URL+r.URL.Path, "application/json", r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		relayed.Add(1)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(relay.Close)

	f := newWatchFixture(t)
	withCloud(f, hostOf(t, relay.URL), nil)
	if out := f.w.ContactFailed(context.Background()); out.Relinked {
		t.Fatalf("relinked to a relay the cloud pointed at: %+v", out)
	}
	if relayed.Load() == 0 {
		t.Fatal("the relay never forwarded a challenge — the test proves nothing")
	}
	if got := f.get(t, "sync.primary_url"); got != deadURL {
		t.Fatalf("sync.primary_url = %q, want unchanged", got)
	}
}

// Fallback only: when the LAN already offered a candidate claiming to be
// this shop's main till, the cloud is not asked — healthy LANs add no cloud
// load, and the cloud never competes with what the LAN found.
func TestPrimaryWatch_CloudNotAskedWhenLANHasACandidate(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: primary.srv.URL})
	calls := withCloud(f, "192.0.2.77:8080", nil)

	if out := f.w.ContactFailed(context.Background()); !out.Relinked {
		t.Fatalf("outcome = %+v, want relinked over the LAN", out)
	}
	if calls.Load() != 0 {
		t.Fatalf("cloud lookups = %d while mDNS supplied a candidate, want 0", calls.Load())
	}
}

// "Nothing usable" includes a LAN where mDNS only sees OTHER shops' tills
// (a shopping centre's shared Wi-Fi): those are never challenged, so the
// replica asks the cloud.
func TestPrimaryWatch_CloudAskedWhenLANOnlyHasOtherTills(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	other := newFakePrimary(t, "33333333-3333-4333-8333-333333333333", testBearerHash())
	f := newWatchFixture(t, Candidate{Name: "Other shop", TillID: other.primaryID, BaseURL: other.srv.URL})
	calls := withCloud(f, hostOf(t, primary.srv.URL), nil)

	out := f.w.ContactFailed(context.Background())
	if !out.Relinked || out.NewURL != primary.srv.URL {
		t.Fatalf("outcome = %+v, want relinked via the cloud", out)
	}
	if calls.Load() != 1 || other.hits.Load() != 0 {
		t.Fatalf("cloud lookups = %d, other-shop challenges = %d; want 1 and 0", calls.Load(), other.hits.Load())
	}
}

// The browse itself erroring (no multicast route at all) is "nothing
// found" too — the cloud is still asked.
func TestPrimaryWatch_CloudAskedWhenBrowseErrors(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t)
	f.w.browse = func(context.Context, time.Duration) ([]Candidate, error) {
		return nil, errors.New("no multicast route")
	}
	calls := withCloud(f, hostOf(t, primary.srv.URL), nil)

	if out := f.w.ContactFailed(context.Background()); !out.Relinked {
		t.Fatalf("outcome = %+v, want relinked via the cloud", out)
	}
	if calls.Load() != 1 {
		t.Fatalf("cloud lookups = %d, want 1", calls.Load())
	}
}

// An old cloud, a cloud outage or an empty answer is just "no candidate
// from the cloud": mDNS-only behaviour, exactly as before.
func TestPrimaryWatch_CloudErrorOrEmptyDegradesToLANOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		addr string
		err  error
	}{
		"error": {err: errors.New("404 old cloud")},
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWatchFixture(t)
			calls := withCloud(f, tc.addr, tc.err)
			out := f.w.ContactFailed(context.Background())
			if out.Relinked || !out.Warn {
				t.Fatalf("outcome = %+v, want the one-time warn and no re-link", out)
			}
			if calls.Load() != 1 {
				t.Fatalf("cloud lookups = %d, want 1", calls.Load())
			}
			if got := f.get(t, "sync.primary_url"); got != deadURL {
				t.Fatalf("sync.primary_url = %q, want unchanged", got)
			}
		})
	}
}

// A cloud answer that is not a plain IP:port is never dialled — and the
// address that is already failing is not retried.
func TestPrimaryWatch_CloudAnswerMustBeAnIPAndPort(t *testing.T) {
	for _, addr := range []string{
		"evil.example.com:8080",
		"192.0.2.10",
		"192.0.2.10:0",
		"192.0.2.10:99999",
		"http://192.0.2.10:8080",
		"0.0.0.0:8080",
		"224.0.0.251:5353",
		hostOf(t, deadURL), // the address that is already failing
	} {
		t.Run(addr, func(t *testing.T) {
			f := newWatchFixture(t)
			var dialled atomic.Int32
			f.w.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				dialled.Add(1)
				return nil, errors.New("must not dial")
			})
			withCloud(f, addr, nil)
			if out := f.w.ContactFailed(context.Background()); out.Relinked {
				t.Fatalf("relinked to %q: %+v", addr, out)
			}
			if dialled.Load() != 0 {
				t.Fatalf("challenged the cloud answer %q", addr)
			}
		})
	}
}

// The cloud is asked on the browse's own cadence (MinBrowseInterval), never
// every tick.
func TestPrimaryWatch_CloudLookupIsRateLimitedWithTheBrowse(t *testing.T) {
	f := newWatchFixture(t)
	calls := withCloud(f, "", nil)
	for i := 0; i < 20; i++ {
		f.w.ContactFailed(context.Background())
	}
	if calls.Load() != 1 {
		t.Fatalf("cloud lookups = %d over 20 quick failures, want 1", calls.Load())
	}
	f.now = f.now.Add(MinBrowseInterval)
	f.w.ContactFailed(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("cloud lookups = %d after the interval, want 2", calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
