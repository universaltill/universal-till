package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/universaltill/universal-till/internal/lantls"
)

// tlsPrimary is a main till on a real same-port TLS listener (ADR-0114 §7)
// answering the proof the way pages.registerPrimaryProof does: over the pin
// it served on the connection. proofPin, when set, overrides that pin — a
// MITM serving its own certificate but relaying a proof from the real main
// till covers the real till's pin, not the one the replica saw.
func tlsPrimary(t *testing.T, proofPin func(served string) string) (baseURL string, cert *lantls.Cert) {
	t.Helper()
	cert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		ConnContext: lantls.ConnContext(cert),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req ProofRequest
			if r.URL.Path != ProofPath || json.NewDecoder(r.Body).Decode(&req) != nil || req.TillID != testTillID {
				http.NotFound(w, r)
				return
			}
			pin := lantls.ServedPin(r.Context())
			if proofPin != nil {
				pin = proofPin(pin)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": ProofResponse{
				PrimaryTillID: testPrimaryID,
				Proof:         PrimaryProof(testBearerHash(), testPrimaryID, req.TillID, req.Nonce, r.Host, pin),
			}, "error": nil})
		}),
	}
	go func() { _ = srv.Serve(lantls.Listen(inner, cert.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + inner.Addr().String(), cert
}

func relink(t *testing.T, f *watchFixture) FailureOutcome {
	t.Helper()
	var out FailureOutcome
	for i := 0; i < UnreachableThreshold && !out.Relinked; i++ {
		out = f.w.ContactFailed(context.Background())
	}
	return out
}

// Without a pin the proof is byte-for-byte the pre-#4091 HMAC, so a new
// replica and an older main till (or plain HTTP) still agree.
func TestPrimaryProof_NoPinIsTheLegacyHMAC(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("h"))
	for _, part := range []string{proofLabel, "p", "t", "n", "192.0.2.5:8080"} {
		mac.Write([]byte(part))
		mac.Write([]byte{'\n'})
	}
	if got, want := PrimaryProof("h", "p", "t", "n", "192.0.2.5:8080", ""), hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("no-pin proof %s, want legacy %s", got, want)
	}
	if PrimaryProof("h", "p", "t", "n", "192.0.2.5:8080", "ab") == PrimaryProof("h", "p", "t", "n", "192.0.2.5:8080", "") ||
		PrimaryProof("h", "p", "t", "n", "192.0.2.5:8080", "ab") == PrimaryProof("h", "p", "t", "n", "192.0.2.5:8080", "cd") {
		t.Fatal("PrimaryProof ignores the pin")
	}
}

// A main till serving TLS: the proof covers the pin the replica saw, and a
// valid proof stores it (ADR-0114 §7, ut-docs#4091).
func TestPrimaryWatch_TLSProofStoresThePin(t *testing.T) {
	base, cert := tlsPrimary(t, nil)
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: base})
	if out := relink(t, f); !out.Relinked || out.NewURL != base {
		t.Fatalf("not relinked: %+v", out)
	}
	if got := f.get(t, PrimaryCertPinSettingKey); got != cert.Pin() {
		t.Fatalf("stored pin %q, want the main till's %q", got, cert.Pin())
	}
	// sync.primary_url stays the plain base: dialling https is slice 3/3.
	if got := f.get(t, "sync.primary_url"); got != base {
		t.Fatalf("primary_url %q, want %q", got, base)
	}
}

// A device presenting a certificate the main till doesn't hold can relay a
// genuine proof, but that proof covers the real till's pin, not the one the
// replica saw: the proof fails, nothing switches and no pin is stored.
func TestPrimaryWatch_TLSProofWithWrongPinFails(t *testing.T) {
	realMain, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base, _ := tlsPrimary(t, func(string) string { return realMain.Pin() })
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: base})
	if out := relink(t, f); out.Relinked {
		t.Fatalf("relinked through a proof over the wrong pin: %+v", out)
	}
	if got := f.get(t, "sync.primary_url"); got != deadURL {
		t.Fatalf("primary_url moved to %q", got)
	}
	if got := f.get(t, PrimaryCertPinSettingKey); got != "" {
		t.Fatalf("stored pin %q from a failed proof", got)
	}
}

// A main till without TLS (older, or its key unusable): the proof runs over
// plain HTTP as before, and never clears a pin the replica already holds.
func TestPrimaryWatch_PlainProofKeepsAStoredPin(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: primary.srv.URL})
	if err := f.settings.Set(context.Background(), PrimaryCertPinSettingKey, "earlier-pin"); err != nil {
		t.Fatal(err)
	}
	if out := relink(t, f); !out.Relinked {
		t.Fatalf("plain main till not relinked: %+v", out)
	}
	if got := f.get(t, PrimaryCertPinSettingKey); got != "earlier-pin" {
		t.Fatalf("pin %q after a plain proof, want it kept", got)
	}
}

// A replica paired before #4091 learns the pin on its first good contact,
// through the same proof, once per process.
func TestPrimaryWatch_ContactOKLearnsThePin(t *testing.T) {
	base, cert := tlsPrimary(t, nil)
	f := newWatchFixture(t)
	if err := f.settings.Set(context.Background(), "sync.primary_url", base); err != nil {
		t.Fatal(err)
	}
	f.w.ContactOK(context.Background())
	if got := f.get(t, PrimaryCertPinSettingKey); got != cert.Pin() {
		t.Fatalf("pin %q after ContactOK, want %q", got, cert.Pin())
	}
	if got := f.get(t, PrimaryTillIDSettingKey); got != testPrimaryID {
		t.Fatalf("primary till id %q changed", got)
	}
}

// A candidate that answers over TLS with a proof that fails is final: the
// replica doesn't retry over plain HTTP, where a relay could hand it the
// unpinned proof (ut-docs#4091 review).
func TestPrimaryWatch_AnsweredTLSProofDoesNotFallBackToPlain(t *testing.T) {
	base, _ := tlsPrimary(t, func(served string) string {
		if served == "" {
			return "" // plain HTTP: a valid, unpinned proof
		}
		return "not-the-served-pin"
	})
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: base})
	if out := relink(t, f); out.Relinked {
		t.Fatalf("fell back to plain HTTP after a failed TLS proof: %+v", out)
	}
}
