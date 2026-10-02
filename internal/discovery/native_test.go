package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// fakeNativeBridge stands in for the iOS shell's BonjourBridge.swift
// (ut-docs#3218): Browse returns a canned JSON answer, Advertise and
// StopAdvertising record what they were asked to do.
type fakeNativeBridge struct {
	mu          sync.Mutex
	browseJSON  string
	browseBlock chan struct{} // non-nil: Browse waits on it before answering
	browsePanic bool
	browsedType string
	browsedMs   int64

	advertiseErr string
	advertised   []string // "instance|type|port|txtJSON" per call
	stops        int
}

func (f *fakeNativeBridge) Browse(serviceType string, timeoutMillis int64) string {
	f.mu.Lock()
	f.browsedType, f.browsedMs = serviceType, timeoutMillis
	block, answer, panics := f.browseBlock, f.browseJSON, f.browsePanic
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if panics {
		panic("bridge exploded")
	}
	return answer
}

func (f *fakeNativeBridge) Advertise(instance, serviceType string, port int64, txtJSON string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advertised = append(f.advertised, fmt.Sprintf("%s|%s|%d|%s", instance, serviceType, port, txtJSON))
	return f.advertiseErr
}

func (f *fakeNativeBridge) StopAdvertising() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

// useNativeBridge registers b for one test and makes any fall-through to the
// raw-socket path a test failure.
func useNativeBridge(t *testing.T, b NativeBridge) {
	t.Helper()
	SetNativeBridge(b)
	t.Cleanup(func() { SetNativeBridge(nil) })
	orig := mdnsQuery
	mdnsQuery = func(*mdns.QueryParam) error {
		t.Error("hashicorp/mdns was queried although a native bridge is registered")
		return nil
	}
	t.Cleanup(func() { mdnsQuery = orig })
}

func browseJSON(t *testing.T, entries []nativeEntry, errCode string) string {
	t.Helper()
	b, err := json.Marshal(nativeBrowseResult{Entries: entries, Error: errCode})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNativeBrowse_UsesBridgeAndParsesTills(t *testing.T) {
	f := &fakeNativeBridge{browseJSON: browseJSON(t, []nativeEntry{
		{Name: "till-a", Host: "192.168.1.136", Port: 8080, TXT: []string{"v=1", "name=Corner Café", "id=till-a", "link=1"}},
	}, "")}
	useNativeBridge(t, f)

	got, err := Browse(context.Background(), 4*time.Second)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	want := Candidate{Name: "Corner Café", TillID: "till-a", BaseURL: "http://192.168.1.136:8080", Link: 1}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Browse = %+v, want [%+v]", got, want)
	}
	if f.browsedType != ServiceName || f.browsedMs != 4000 {
		t.Fatalf("bridge asked for %q/%dms, want %q/4000ms", f.browsedType, f.browsedMs, ServiceName)
	}
}

func TestNativeBrowse_DropsUnusableEntries(t *testing.T) {
	f := &fakeNativeBridge{browseJSON: browseJSON(t, []nativeEntry{
		{Name: "no-id", Host: "192.168.1.2", Port: 8080, TXT: []string{"name=x"}},
		{Name: "hostname", Host: "till.local.", Port: 8080, TXT: []string{"id=h"}},
		{Name: "no-port", Host: "192.168.1.3", Port: 0, TXT: []string{"id=p"}},
		{Name: "big-port", Host: "192.168.1.3", Port: 70000, TXT: []string{"id=q"}},
		{Name: "v6", Host: "fe80::1%en0", Port: 8081, TXT: []string{"id=six"}},
	}, "")}
	useNativeBridge(t, f)

	got, err := Browse(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(got) != 1 || got[0].TillID != "six" || got[0].BaseURL != "http://[fe80::1]:8081" {
		t.Fatalf("Browse = %+v, want only the IPv6 till, zone stripped", got)
	}
}

func TestNativeBrowse_CapsCandidates(t *testing.T) {
	var entries []nativeEntry
	for i := 0; i < maxCandidates+10; i++ {
		entries = append(entries, nativeEntry{Name: fmt.Sprint(i), Host: "10.0.0.1", Port: 8080 + i, TXT: []string{"id=" + fmt.Sprint(i)}})
	}
	useNativeBridge(t, &fakeNativeBridge{browseJSON: browseJSON(t, entries, "")})

	got, err := Browse(context.Background(), time.Second)
	if err != nil || len(got) != maxCandidates {
		t.Fatalf("Browse = %d candidates, %v; want %d, nil", len(got), err, maxCandidates)
	}
}

func TestNativeBrowse_LocalNetworkDenied(t *testing.T) {
	useNativeBridge(t, &fakeNativeBridge{browseJSON: browseJSON(t, nil, LocalNetworkDeniedCode)})

	_, err := Browse(context.Background(), time.Second)
	if !errors.Is(err, ErrLocalNetworkDenied) {
		t.Fatalf("err = %v, want ErrLocalNetworkDenied", err)
	}
}

func TestNativeBrowse_KeepsFoundPeersDespiteError(t *testing.T) {
	useNativeBridge(t, &fakeNativeBridge{browseJSON: browseJSON(t, []nativeEntry{
		{Name: "a", Host: "10.0.0.5", Port: 8080, TXT: []string{"id=a"}},
	}, LocalNetworkDeniedCode)})

	got, err := Browse(context.Background(), time.Second)
	if err != nil || len(got) != 1 {
		t.Fatalf("Browse = %+v, %v; want the one peer and no error", got, err)
	}
}

func TestNativeBrowse_OtherErrorsAreGeneric(t *testing.T) {
	for _, answer := range []string{`{"entries":[],"error":"NWError -65563"}`, `not json`} {
		useNativeBridge(t, &fakeNativeBridge{browseJSON: answer})
		_, err := Browse(context.Background(), time.Second)
		if err == nil || errors.Is(err, ErrLocalNetworkDenied) {
			t.Fatalf("answer %q: err = %v, want a generic error", answer, err)
		}
	}
}

func TestNativeBrowse_PanicIsAnError(t *testing.T) {
	useNativeBridge(t, &fakeNativeBridge{browsePanic: true})
	if _, err := Browse(context.Background(), time.Second); err == nil {
		t.Fatal("Browse returned no error after the bridge panicked")
	}
}

func TestNativeBrowse_CancelReturnsPromptly(t *testing.T) {
	f := &fakeNativeBridge{browseBlock: make(chan struct{}), browseJSON: `{"entries":[]}`}
	useNativeBridge(t, f)
	defer close(f.browseBlock)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Browse(ctx, time.Minute)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Browse kept waiting on the bridge after its context was cancelled")
	}
}

func TestNativeBrowse_Printers(t *testing.T) {
	f := &fakeNativeBridge{browseJSON: browseJSON(t, []nativeEntry{
		{Name: "Kitchen", Host: "192.168.1.50", Port: 9100, TXT: []string{"ty=Epson TM-m30", "pdl=application/vnd.epson.escpos"}},
	}, "")}
	useNativeBridge(t, f)

	got, err := BrowsePrinters(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("BrowsePrinters: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Epson TM-m30" || got[0].Address != "192.168.1.50:9100" || got[0].PDL == "" {
		t.Fatalf("BrowsePrinters = %+v", got)
	}
	if f.browsedType != PrinterServiceName {
		t.Fatalf("bridge asked for %q, want %q", f.browsedType, PrinterServiceName)
	}
}

func TestNativeAdvertiser_PublishesThroughBridgeAndWithdraws(t *testing.T) {
	f := &fakeNativeBridge{}
	SetNativeBridge(f)
	t.Cleanup(func() { SetNativeBridge(nil) })
	a, starts, setRole := newTestAdvertiser(t, true)

	a.tick(context.Background())
	if *starts != 0 {
		t.Fatalf("hashicorp/mdns server started %d times although a native bridge is registered", *starts)
	}
	if len(f.advertised) != 1 {
		t.Fatalf("Advertise calls = %v, want exactly one", f.advertised)
	}
	parts := strings.SplitN(f.advertised[0], "|", 4)
	id, err := TillID(context.Background(), a.settings)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0] != id || parts[1] != ServiceName || parts[2] != "8080" {
		t.Fatalf("Advertise(%q), want instance %q, type %q, port 8080", f.advertised[0], id, ServiceName)
	}
	var txt []string
	if err := json.Unmarshal([]byte(parts[3]), &txt); err != nil {
		t.Fatalf("txtJSON %q: %v", parts[3], err)
	}
	if strings.Join(txt, ",") != strings.Join(txtRecord(advertisedName(context.Background(), a.settings, id), id), ",") {
		t.Fatalf("txt = %v, want the same record the mDNS path publishes", txt)
	}

	setRole(false)
	a.tick(context.Background())
	if f.stops != 1 || a.server != nil {
		t.Fatalf("after becoming a replica: stops = %d, server = %v; want 1, nil", f.stops, a.server)
	}
}

func TestNativeAdvertiser_FailureIsRetriedNextTick(t *testing.T) {
	f := &fakeNativeBridge{advertiseErr: "DNSServiceRegister failed: -65537"}
	SetNativeBridge(f)
	t.Cleanup(func() { SetNativeBridge(nil) })
	a, _, _ := newTestAdvertiser(t, true)

	a.tick(context.Background())
	if a.server != nil {
		t.Fatal("a failed Advertise left the Advertiser thinking it is advertising")
	}
	f.mu.Lock()
	f.advertiseErr = ""
	f.mu.Unlock()
	a.tick(context.Background())
	if a.server == nil || len(f.advertised) != 2 {
		t.Fatalf("retry: server = %v, Advertise calls = %d; want advertising after 2 calls", a.server, len(f.advertised))
	}
}

func TestSetNativeBridgeNilRestoresMDNS(t *testing.T) {
	SetNativeBridge(&fakeNativeBridge{browseJSON: `{"entries":[]}`})
	SetNativeBridge(nil)
	orig, origV6 := mdnsQuery, ipv6Supported
	t.Cleanup(func() { mdnsQuery, ipv6Supported = orig, origV6 })
	ipv6Supported = func() bool { return true }
	queried := false
	mdnsQuery = func(*mdns.QueryParam) error { queried = true; return nil }

	if _, err := Browse(context.Background(), time.Second); err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if !queried {
		t.Fatal("hashicorp/mdns was not used after the bridge was removed")
	}
}
