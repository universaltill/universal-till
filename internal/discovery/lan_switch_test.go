package discovery

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// ut-docs#3814: with the LAN switch off, nothing touches the network. These
// tests mutate process-wide state, so they deliberately do not use t.Parallel.
func disableLANForTest(t *testing.T) {
	t.Helper()
	SetLANDisabled(true)
	t.Cleanup(func() { SetLANDisabled(false) })
}

func TestScan_LANDisabledNeverQueriesMDNS(t *testing.T) {
	forceIPv6Supported(t, true)
	disableLANForTest(t)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	calls := 0
	mdnsQuery = func(*mdns.QueryParam) error {
		calls++
		return nil
	}
	bridge := &fakeNativeBridge{browseJSON: `{"entries":[]}`}
	SetNativeBridge(bridge)
	t.Cleanup(func() { SetNativeBridge(nil) })

	tills, err := Browse(context.Background(), time.Second)
	if err != nil || len(tills) != 0 {
		t.Fatalf("Browse = %v, %v; want empty, nil", tills, err)
	}
	printers, err := BrowsePrinters(context.Background(), time.Second)
	if err != nil || len(printers) != 0 {
		t.Fatalf("BrowsePrinters = %v, %v; want empty, nil", printers, err)
	}
	if calls != 0 {
		t.Fatalf("mdnsQuery called %d times with LAN discovery off", calls)
	}
	if bridge.browsedType != "" {
		t.Fatalf("native bridge browsed %q with LAN discovery off", bridge.browsedType)
	}
}

func TestAdvertiser_StartLANDisabledDoesNothing(t *testing.T) {
	disableLANForTest(t)
	a, starts, _ := newTestAdvertiser(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	a.Start(ctx, &wg)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start registered on the WaitGroup with LAN discovery off")
	}
	time.Sleep(50 * time.Millisecond)
	if *starts != 0 {
		t.Fatalf("mDNS server constructed %d times with LAN discovery off", *starts)
	}
}

// Review F2: the raw-socket path (no native bridge) is gated too.
func TestScan_LANDisabledNoBridgeNeverQueriesMDNS(t *testing.T) {
	forceIPv6Supported(t, true)
	disableLANForTest(t)
	SetNativeBridge(nil)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	calls := 0
	mdnsQuery = func(*mdns.QueryParam) error {
		calls++
		return nil
	}

	if tills, err := Browse(context.Background(), time.Second); err != nil || len(tills) != 0 {
		t.Fatalf("Browse = %v, %v; want empty, nil", tills, err)
	}
	if calls != 0 {
		t.Fatalf("mdnsQuery called %d times with LAN discovery off", calls)
	}
}

// Review F1: printer discovery's :9100 subnet sweep is LAN discovery too.
func TestDiscoverPrinters_LANDisabledNeverSweeps(t *testing.T) {
	disableLANForTest(t)
	orig := localSubnetHosts
	t.Cleanup(func() { localSubnetHosts = orig })
	enumerated := 0
	localSubnetHosts = func() ([]string, error) {
		enumerated++
		return nil, nil
	}

	printers, err := DiscoverPrinters(context.Background(), time.Second)
	if err != nil || len(printers) != 0 {
		t.Fatalf("DiscoverPrinters = %v, %v; want empty, nil", printers, err)
	}
	if enumerated != 0 {
		t.Fatalf("subnet sweep enumerated hosts %d times with LAN discovery off", enumerated)
	}
}
