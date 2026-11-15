package pages

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3137: known-offline is decided per signer. A declared offline
// state skips only signers that need the network; an undeclared one skips a
// signer whose backend host a fresh background probe found unreachable.

const signerHost = "signer.example.test"

type signerFailingTransport struct{}

func (signerFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.DNSError{Err: "no such host", Name: "signer", IsNotFound: true}
}

type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) now() time.Time {
	return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC).Add(time.Duration(c.off.Load()))
}

// useSignerReach installs a Set whose probes always fail (no real network)
// on a fake clock.
func useSignerReach(dp *common.Deps) *offsetClock {
	clk := &offsetClock{}
	dp.SignerReach = netreach.NewSet(netreach.SetOptions{
		Client: &http.Client{Transport: signerFailingTransport{}}, Now: clk.now, TTL: 30 * time.Second, Path: "/",
	})
	return clk
}

// primeUnreachable runs the host's probe to completion (it fails) so Fresh
// reads Unreachable.
func primeUnreachable(t *testing.T, dp *common.Deps, host string) {
	t.Helper()
	m := dp.SignerReach.For("https://" + host)
	deadline := time.Now().Add(5 * time.Second)
	for m.Fresh() != netreach.Unreachable {
		if time.Now().After(deadline) {
			t.Fatalf("probe of %s never completed", host)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func grantSignerPerm(t *testing.T, dp *common.Deps, pluginID, perm string) {
	t.Helper()
	repo := data.NewPluginRepo(dp.Db)
	if err := repo.InsertPluginPermissions(context.Background(), nil, pluginID, []string{perm}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPermission(context.Background(), pluginID, perm, true); err != nil {
		t.Fatal(err)
	}
}

// askSigner subscribes a counting fiscal.sign.ask signer holding perm ("" =
// network-free).
func askSigner(t *testing.T, dp *common.Deps, perm string) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.ko-signer", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		n.Add(1)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if perm != "" {
		grantSignerPerm(t, dp, "com.test.ko-signer", perm)
	}
	return &n
}

func TestFiscalKnownOffline_UnreachableBackendIsNotAsked(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	useSignerReach(dp)
	n := askSigner(t, dp, "net:"+signerHost)
	primeUnreachable(t, dp, signerHost)
	in := pos.SaleInput{Currency: "EUR"}
	res := dispatchFiscalSignAsk(context.Background(), dp, &in)
	if res.Outcome != fiscalSignSkippedOffline || n.Load() != 0 {
		t.Fatalf("outcome %v, invocations %d; want skipped-offline and 0", res.Outcome, n.Load())
	}
	if res.Reason != "signer backend unreachable: "+signerHost || strings.Contains(res.Reason, "://") {
		t.Fatalf("reason %q must name the host only", res.Reason)
	}
	if in.Offline {
		t.Fatal("a probe-derived skip must not mark the sale offline")
	}
}

func TestFiscalKnownOffline_UnknownOrStaleProbeStillAsks(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	clk := useSignerReach(dp)
	n := askSigner(t, dp, "net:"+signerHost)
	// Unknown: no probe has completed yet.
	if res := dispatchFiscalSignAsk(context.Background(), dp, &pos.SaleInput{Currency: "EUR"}); res.Outcome == fiscalSignSkippedOffline {
		t.Fatal("unknown probe state must dispatch")
	}
	primeUnreachable(t, dp, signerHost)
	clk.off.Add(int64(time.Minute)) // stale now
	before := n.Load()
	if res := dispatchFiscalSignAsk(context.Background(), dp, &pos.SaleInput{Currency: "EUR"}); res.Outcome == fiscalSignSkippedOffline || n.Load() != before+1 {
		t.Fatalf("stale probe must dispatch, outcome %v invocations %d->%d", res.Outcome, before, n.Load())
	}
}

func TestFiscalKnownOffline_DeclaredOfflineAsksNetworkFreeSigner(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	n := askSigner(t, dp, "")
	res := dispatchFiscalSignAsk(context.Background(), dp, &pos.SaleInput{Currency: "EUR", Offline: true})
	if res.Outcome == fiscalSignSkippedOffline || n.Load() != 1 {
		t.Fatalf("network-free signer must sign offline, outcome %v invocations %d", res.Outcome, n.Load())
	}
}

func TestFiscalKnownOffline_DeclaredOfflineSkipsNetworkSigner(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	n := askSigner(t, dp, "net:*")
	res := dispatchFiscalSignAsk(context.Background(), dp, &pos.SaleInput{Currency: "EUR", Offline: true})
	if res.Outcome != fiscalSignSkippedOffline || n.Load() != 0 || res.Reason != fiscalKnownOfflineReason {
		t.Fatalf("outcome %v invocations %d reason %q; want skipped with the declared reason", res.Outcome, n.Load(), res.Reason)
	}
}

func TestFiscalKnownOffline_UnprobeableSignerIsAlwaysAsked(t *testing.T) {
	for _, perm := range []string{"net:*", "tcp:192.168.1.5:80", "net:192.168.1.5"} {
		_, dp := newFiscalSignDeps(t)
		useSignerReach(dp)
		n := askSigner(t, dp, perm)
		primeUnreachable(t, dp, signerHost) // irrelevant to this signer
		if res := dispatchFiscalSignAsk(context.Background(), dp, &pos.SaleInput{Currency: "EUR"}); res.Outcome == fiscalSignSkippedOffline || n.Load() != 1 {
			t.Fatalf("%s: outcome %v invocations %d; want asked", perm, res.Outcome, n.Load())
		}
	}
}

func TestFiscalKnownOffline_NilSignerReachAsks(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	n := askSigner(t, dp, "net:"+signerHost)
	if res := dispatchFiscalSignAsk(context.Background(), dp, &pos.SaleInput{Currency: "EUR"}); res.Outcome == fiscalSignSkippedOffline || n.Load() != 1 {
		t.Fatalf("no Set configured: outcome %v invocations %d; want asked", res.Outcome, n.Load())
	}
}

func TestFiscalSignStart_UnreachableBackendSkipsAndNetworkFreeRunsOffline(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	useSignerReach(dp)
	var n atomic.Int32
	subscribeFiscalSignStartHandler(t, dp, "com.test.ko-start", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		n.Add(1)
		return json.RawMessage(`{"status":"acknowledged","tx_id":"t","tx_revision":1}`), nil
	})
	grantSignerPerm(t, dp, "com.test.ko-start", "net:"+signerHost)
	primeUnreachable(t, dp, signerHost)
	dispatchFiscalSignStart(context.Background(), dp, &pos.SaleInput{Currency: "EUR"})
	dp.WaitForAsyncWork()
	if n.Load() != 0 {
		t.Fatalf("unreachable backend: fiscal.sign.start dispatched %d times", n.Load())
	}
	// Declared offline but the signer holds no network grant: dispatched.
	if err := data.NewPluginRepo(dp.Db).SetPermission(context.Background(), "com.test.ko-start", "net:"+signerHost, false); err != nil {
		t.Fatal(err)
	}
	dispatchFiscalSignStart(context.Background(), dp, &pos.SaleInput{Currency: "EUR", Offline: true})
	dp.WaitForAsyncWork()
	if n.Load() != 1 {
		t.Fatalf("network-free signer declared offline: dispatched %d times, want 1", n.Load())
	}
}

func TestFiscalOrderStartAndCancel_UnreachableBackendSkips(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	useSignerReach(dp)
	var starts, cancels atomic.Int32
	subscribeFiscalOrderStartHandler(t, dp, "com.test.ko-ostart", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		starts.Add(1)
		return nil, nil
	})
	subscribeFiscalOrderCancelHandler(t, dp, "com.test.ko-ocancel", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		cancels.Add(1)
		return nil, nil
	})
	grantSignerPerm(t, dp, "com.test.ko-ostart", "net:"+signerHost)
	grantSignerPerm(t, dp, "com.test.ko-ocancel", "net:"+signerHost)
	primeUnreachable(t, dp, signerHost)
	dispatchFiscalOrderStart(context.Background(), dp, "hold-1", fiscalOrderKindHeld, false)
	dispatchFiscalOrderCancel(context.Background(), dp, "hold-1", false)
	dp.WaitForAsyncWork()
	if starts.Load() != 0 || cancels.Load() != 0 {
		t.Fatalf("unreachable backend: order start %d, cancel %d dispatches, want 0", starts.Load(), cancels.Load())
	}
	// Declared offline + network-free signers are still dispatched.
	for _, id := range []string{"com.test.ko-ostart", "com.test.ko-ocancel"} {
		if err := data.NewPluginRepo(dp.Db).SetPermission(context.Background(), id, "net:"+signerHost, false); err != nil {
			t.Fatal(err)
		}
	}
	dispatchFiscalOrderStart(context.Background(), dp, "hold-2", fiscalOrderKindHeld, true)
	dispatchFiscalOrderCancel(context.Background(), dp, "hold-2", true)
	dp.WaitForAsyncWork()
	if starts.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("network-free + declared offline: start %d, cancel %d, want 1 each", starts.Load(), cancels.Load())
	}
}

type cloudOKTransport struct{}

func (cloudOKTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
}

// Review M3 (ut-docs#3137): a signer host that fails while the till's own
// cloud probe answers is not taken as "offline" — the host, or a port the
// probe didn't try, is the problem, so the signer is still asked.
func TestFiscalKnownOffline_CloudReachableStillAsks(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	useSignerReach(dp)
	dp.NetReach = netreach.New(netreach.Options{Endpoint: "https://cloud.example.test/api",
		Client: &http.Client{Transport: cloudOKTransport{}}})
	deadline := time.Now().Add(5 * time.Second)
	for dp.NetReach.Status() != netreach.Reachable {
		if time.Now().After(deadline) {
			t.Fatal("cloud probe never completed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	n := askSigner(t, dp, "net:"+signerHost)
	primeUnreachable(t, dp, signerHost)
	in := pos.SaleInput{Currency: "EUR"}
	res := dispatchFiscalSignAsk(context.Background(), dp, &in)
	if res.Outcome == fiscalSignSkippedOffline || n.Load() != 1 {
		t.Fatalf("outcome %v, invocations %d; want the signer asked once", res.Outcome, n.Load())
	}
}

// Review m2: the probe client dials direct like plugin egress and never
// follows a redirect (any response already proves the host answers).
func TestSignerProbeClient_NoProxyNoRedirect(t *testing.T) {
	c := signerProbeClient()
	if c.CheckRedirect == nil || c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("probe client must not follow redirects")
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("probe went through the environment proxy or followed the redirect: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want the 302 itself", resp.StatusCode)
	}
}
