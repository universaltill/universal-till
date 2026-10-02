package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"

	"github.com/universaltill/universal-till/internal/logging"
)

// NativeBridge lets a platform shell replace this package's own mDNS sockets
// with the OS's service-discovery API (ut-docs#3218). On iOS, hashicorp/mdns's
// raw UDP multicast needs Apple's managed multicast entitlement, which the app
// does not have — so an iOS till could neither find nor announce other tills.
// Apple's Bonjour APIs (NWBrowser, DNSServiceRegister) go through
// mDNSResponder and need only the Local Network permission plus the
// NSBonjourServices the app already declares.
//
// Linux, Windows, macOS and Android register nothing and keep the
// hashicorp/mdns path unchanged.
//
// Every method uses only gomobile-bind-safe types (string, int64) and returns
// errors in-band rather than as a Go error: the shell implements this in
// Swift, and keeping NSError** out of the ObjC protocol keeps the Swift
// conformance a plain method. mobile.DiscoveryBridge mirrors this interface
// method-for-method (gobind binds only types declared in ./mobile — see
// mobile.BluetoothBridge for why).
type NativeBridge interface {
	// Browse runs one bounded browse for serviceType (e.g. "_unitill-sync._tcp")
	// lasting at most timeoutMillis, and returns a JSON-encoded nativeBrowseResult.
	Browse(serviceType string, timeoutMillis int64) string
	// Advertise publishes this till as instance under serviceType on port, with
	// txtJSON (a JSON array of "key=value" strings) as its TXT record. It
	// replaces any advertisement this bridge already holds. Returns "" on
	// success, otherwise an error description.
	Advertise(instance, serviceType string, port int64, txtJSON string) string
	// StopAdvertising withdraws the advertisement, if any.
	StopAdvertising()
}

// LocalNetworkDeniedCode is the error code a NativeBridge reports when the
// user has refused this app the Local Network permission (iOS:
// kDNSServiceErr_PolicyDenied).
const LocalNetworkDeniedCode = "local_network_denied"

// ErrLocalNetworkDenied is returned by Browse/BrowsePrinters when the OS has
// refused this app access to the local network. The Tills page shows a
// specific message for it (tills.discovery.denied) instead of the generic
// "could not search" one, pointing at Settings and at pairing by code.
var ErrLocalNetworkDenied = errors.New("local network access denied")

var (
	nativeMu     sync.RWMutex
	nativeBridge NativeBridge
)

// SetNativeBridge registers (or, with nil, removes) the platform's discovery
// bridge. mobile.SetDiscoveryBridge calls it from the iOS shell before the
// server starts.
func SetNativeBridge(b NativeBridge) {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	nativeBridge = b
}

func currentNativeBridge() NativeBridge {
	nativeMu.RLock()
	defer nativeMu.RUnlock()
	return nativeBridge
}

// nativeBrowseResult is the JSON a NativeBridge's Browse returns.
type nativeBrowseResult struct {
	Entries []nativeEntry `json:"entries"`
	// Error is "" on success, LocalNetworkDeniedCode when the permission was
	// refused, or a free-text description of any other failure.
	Error string `json:"error"`
}

// nativeEntry is one resolved service instance.
type nativeEntry struct {
	// Name is the instance name, without the service type or domain.
	Name string `json:"name"`
	// Host is the resolved IP address (an IPv6 zone suffix is tolerated).
	Host string `json:"host"`
	Port int    `json:"port"`
	// TXT is the TXT record as "key=value" strings.
	TXT []string `json:"txt"`
}

// nativeScan is scan's NativeBridge path. It turns each resolved entry into
// the same *mdns.ServiceEntry shape hashicorp/mdns produces, so the existing
// parsers (candidateFromEntry, printerCandidateFromEntry), the service filter
// and the maxCandidates cap apply unchanged.
func nativeScan[T any](ctx context.Context, b NativeBridge, timeout time.Duration, serviceName string, parse func(*mdns.ServiceEntry) (T, bool)) ([]T, error) {
	done := make(chan string, 1)
	go func() {
		defer logging.RecoverAndLog("discovery.nativeBrowse")
		raw := `{"error":"native browse panicked"}`
		defer func() { done <- raw }()
		raw = b.Browse(serviceName, timeout.Milliseconds())
	}()

	var raw string
	select {
	case raw = <-done:
	case <-ctx.Done():
		// The bridge's browse is bounded by timeout and finishes by itself;
		// done is buffered, so its goroutine never blocks on the send.
		return nil, ctx.Err()
	}

	var res nativeBrowseResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("native browse: bad response: %w", err)
	}
	candidates := make([]T, 0)
	for _, ne := range res.Entries {
		e, ok := nativeServiceEntry(ne, serviceName)
		if !ok || !entryMatchesService(e, serviceName) {
			continue
		}
		c, ok := parse(e)
		if !ok {
			continue
		}
		if len(candidates) >= maxCandidates {
			break
		}
		candidates = append(candidates, c)
	}
	switch {
	case res.Error == "":
		return candidates, nil
	case len(candidates) > 0:
		// Same rule as scan: peers already found are not thrown away over an
		// error on the way.
		return candidates, nil
	case res.Error == LocalNetworkDeniedCode:
		return nil, ErrLocalNetworkDenied
	default:
		return nil, fmt.Errorf("native browse: %s", res.Error)
	}
}

// nativeServiceEntry converts one bridge entry. An entry whose host is not an
// IP address is dropped: the parsers need an address a replica can dial.
func nativeServiceEntry(ne nativeEntry, serviceName string) (*mdns.ServiceEntry, bool) {
	host := ne.Host
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // an IPv6 zone means nothing in a URL another till dials
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || ne.Port <= 0 || ne.Port > 65535 {
		return nil, false
	}
	e := &mdns.ServiceEntry{
		Name:       ne.Name + "." + serviceName + ".local.",
		Port:       ne.Port,
		InfoFields: ne.TXT,
	}
	if v4 := ip.To4(); v4 != nil {
		e.AddrV4 = v4
	} else {
		e.AddrV6 = ip
	}
	return e, true
}

// nativeServer is the mdnsServer an Advertiser holds while a NativeBridge
// publishes on its behalf.
type nativeServer struct{ b NativeBridge }

func (s nativeServer) Shutdown() error {
	s.b.StopAdvertising()
	return nil
}

// startNative publishes through the bridge instead of a hashicorp/mdns zone.
func startNative(b NativeBridge, instance string, port int, txt []string) (mdnsServer, error) {
	txtJSON, err := json.Marshal(txt)
	if err != nil {
		return nil, err
	}
	if msg := b.Advertise(instance, ServiceName, int64(port), string(txtJSON)); msg != "" {
		return nil, fmt.Errorf("native advertise: %s", msg)
	}
	return nativeServer{b: b}, nil
}
