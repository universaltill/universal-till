package pages

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
)

// stubLANIP pins lanipIPv4 for one test.
func stubLANIP(t *testing.T, ip string, err error) {
	t.Helper()
	prev := lanipIPv4
	lanipIPv4 = func() (string, error) { return ip, err }
	t.Cleanup(func() { lanipIPv4 = prev })
}

// ut-docs#2774: the main till's check-in carries the host:port a replica
// on the shop LAN dials it on — the LAN address plus this till's listen
// port — so a replica whose mDNS finds nothing can ask the cloud.
func TestBuildCloudHooks_MainTillReportsLANAddress(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dp.Cfg.ListenAddr = ":37673"
	stubLANIP(t, "192.168.1.30", nil)
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	if got := extra[cloudsync.LANAddressKey]; got != "192.168.1.30:37673" {
		t.Fatalf("lan_address = %#v, want %q", got, "192.168.1.30:37673")
	}
}

// An additional till never reports one: it is not the till a replica looks
// for (cloudsync drops it on any non-primary role as well).
func TestBuildCloudHooks_AdditionalTillOmitsLANAddress(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dp.Cfg.ListenAddr = ":37673"
	stubLANIP(t, "192.168.1.30", nil)
	setReplica(t, dp)
	if v, ok := buildCloudHooks(dp, nil).DeviceExtra(t.Context())[cloudsync.LANAddressKey]; ok {
		t.Fatalf("an additional till reported lan_address = %#v", v)
	}
}

// No LAN address (not on a network yet) → nothing is reported; never a
// loopback placeholder a replica would dial itself on.
func TestBuildCloudHooks_NoLANAddressOmitsIt(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dp.Cfg.ListenAddr = ":37673"
	stubLANIP(t, "", errors.New("no LAN address"))
	if v, ok := buildCloudHooks(dp, nil).DeviceExtra(t.Context())[cloudsync.LANAddressKey]; ok {
		t.Fatalf("lan_address = %#v with no LAN address", v)
	}
}

// The replica's cloud lookup authenticates as this till's own enrolled
// device (ut-docs#2730): the effective marketplace endpoint, store id and
// device bearer — the same identity the check-in uses.
func TestPrimaryWatchCloudCredentials_AreThisTillsOwn(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dp.Cfg.Marketplace.MerchantToken = "device-token-1"
	endpoint, storeID, token := primaryWatchCloudCredentials(dp)()
	if endpoint != "http://marketplace.test" || storeID != "store-1" || token != "device-token-1" {
		t.Fatalf("credentials = %q, %q, %q", endpoint, storeID, token)
	}
}
