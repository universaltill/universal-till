package pages

// Cloud-assisted main-till lookup (ut-docs#2774), the till-side wiring.
//
// Main till: its check-in's DeviceExtra carries lan_address — the host:port
// a replica on the shop LAN dials it on. cloudsync keeps it only on a
// role "primary" report, and the cloud stores it on that device's row.
//
// Replica: discovery.PrimaryWatch asks the cloud for that address when an
// mDNS browse finds nothing usable, authenticated as this till's own
// enrolled device. The answer is only a candidate for the existing
// primary-proof challenge (discovery/primary_watch.go).

import (
	"net"
	"strconv"

	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// mainTillLANAddress is this till's LAN host:port, or false when it has no
// LAN address (never a loopback placeholder: lanip refuses those) or no
// usable listen port. The port is the configured listen port — the one
// the mDNS advertiser publishes too (app.listenPort): a fallback bind is
// loopback-only (server.listenWithFallback), so no LAN peer could reach
// the moved port anyway, and a wrong answer can only fail the proof.
func mainTillLANAddress(d *common.Deps) (string, bool) {
	if d == nil || d.Cfg == nil {
		return "", false
	}
	_, portStr, err := net.SplitHostPort(d.Cfg.ListenAddr)
	if err != nil {
		return "", false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	ip, err := lanipIPv4()
	if err != nil || ip == "" {
		return "", false
	}
	return net.JoinHostPort(ip, strconv.Itoa(port)), true
}

// primaryWatchCloudCredentials is the replica's identity for the lookup:
// the effective marketplace endpoint, store id and device bearer — what
// cloudsync's check-in sends (ut-docs#2730: per till, never the main
// till's).
func primaryWatchCloudCredentials(d *common.Deps) discovery.CloudCredentials {
	return func() (string, string, string) {
		m := enroll.Effective(d.Cfg).Marketplace
		return m.EndpointURL, m.StoreID, m.MerchantToken
	}
}
