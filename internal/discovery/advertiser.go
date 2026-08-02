package discovery

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ServiceName is the mDNS service type this till advertises (as a primary)
// and browses for (looking for a primary), per ADR-0033 part 1.
const ServiceName = "_unitill-sync._tcp"

// protocolVersion is the TXT record's "v=" field — reserved for a future
// wire-format bump; no consumer reads it yet.
const protocolVersion = 1

// roleCheckInterval matches internal/pages/sync_admin.go's runSyncLoop
// cadence — the established ~30s convention this codebase already uses for
// background polling loops, not a fresh value picked for this feature.
const roleCheckInterval = 30 * time.Second

// RoleCheck reports whether this till is currently a primary (true) or a
// replica (false) — same rule as pages.Deps.SyncPrimaryURL: empty
// "sync.primary_url" means primary/standalone. Injected so Advertiser's
// start/stop logic is testable without a real pages.Deps or settings DB.
type RoleCheck func(ctx context.Context) bool

// mdnsServer is the subset of *mdns.Server Advertiser depends on, narrowed
// to a seam: tests inject a fake so the start/stop logic never touches a
// real UDP multicast socket.
type mdnsServer interface {
	Shutdown() error
}

// Advertiser publishes this till's presence via mDNS while — and only
// while — it is a primary ("Primaries advertise themselves", ADR-0033).
// Role can change at runtime (join-as-replica / promote-to-primary) with no
// process restart, so callers drive Advertiser via repeated tick calls (see
// Start) rather than deciding once at construction.
type Advertiser struct {
	mu        sync.Mutex
	server    mdnsServer // non-nil only while actively advertising
	settings  *data.SettingsRepo
	isPrimary RoleCheck
	port      int

	// newServer constructs the mDNS server for a zone. A seam so tests can
	// substitute a fake; production wires mdns.NewServer.
	newServer func(zone mdns.Zone) (mdnsServer, error)
}

// NewAdvertiser builds an Advertiser. port is recorded in the advertised
// SRV record on a best-effort basis — nothing built by this card (#264)
// consumes it yet (selecting a discovered primary is #185's job).
func NewAdvertiser(settings *data.SettingsRepo, isPrimary RoleCheck, port int) *Advertiser {
	return &Advertiser{
		settings:  settings,
		isPrimary: isPrimary,
		port:      port,
		newServer: func(zone mdns.Zone) (mdnsServer, error) {
			return mdns.NewServer(&mdns.Config{Zone: zone})
		},
	}
}

// Start runs the role-check loop until ctx ends, joined to wg so
// internal/app's graceful-shutdown drain (drainBackgroundServices) covers
// it too — same wg.Add(1)/go func/defer wg.Done() shape as
// internal/alerts.Start and internal/updates.Start.
func (a *Advertiser) Start(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.tick(ctx) // check immediately — don't wait a full interval to advertise a primary that already was one at boot
		ticker := time.NewTicker(roleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				a.shutdownIfAdvertising()
				return
			case <-ticker.C:
				a.tick(ctx)
			}
		}
	}()
}

// tick is one check of the role-check loop, extracted so tests can drive it
// directly instead of only via Start's real ticker (same pattern as
// sync_admin.go's syncPullTick alongside StartSyncPull).
func (a *Advertiser) tick(ctx context.Context) {
	primary := a.isPrimary(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()

	switch {
	case primary && a.server == nil:
		srv, err := a.startLocked(ctx)
		if err != nil {
			logging.L().Warnf("lan discovery: failed to start mDNS advertiser (will retry): %v", err)
			return
		}
		a.server = srv
	case !primary && a.server != nil:
		if err := a.server.Shutdown(); err != nil {
			logging.L().Warnf("lan discovery: mDNS advertiser shutdown: %v", err)
		}
		a.server = nil
	}
}

func (a *Advertiser) shutdownIfAdvertising() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.server == nil {
		return
	}
	if err := a.server.Shutdown(); err != nil {
		logging.L().Warnf("lan discovery: mDNS advertiser shutdown: %v", err)
	}
	a.server = nil
}

// startLocked builds the mDNS zone and starts serving it. Caller holds a.mu.
func (a *Advertiser) startLocked(ctx context.Context) (mdnsServer, error) {
	id, err := TillID(ctx, a.settings)
	if err != nil {
		return nil, err
	}
	name := storeNameOrDefault(ctx, a.settings)
	port := a.port
	if port == 0 {
		port = 8080 // config.Init's own UT_LISTEN_ADDR default; NewMDNSService requires a non-zero port
	}
	zone, err := mdns.NewMDNSService(id, ServiceName, "", "", port, localIPs(), txtRecord(name, id))
	if err != nil {
		return nil, err
	}
	return a.newServer(zone)
}

// txtRecord is the ONLY data this till broadcasts over mDNS — deliberately
// just enough for a replica to show a human a name and independently
// compute the pairing verification code (ADR-0033 §4/§8): no secret,
// token, or commitment ever goes in a TXT record broadcast to the whole
// LAN. TestAdvertiser_TXTRecordCarriesNoSecrets guards this.
func txtRecord(shopName, tillID string) []string {
	return []string{
		"v=" + strconv.Itoa(protocolVersion),
		"name=" + shopName,
		"id=" + tillID,
	}
}

// localIPs gathers this host's non-loopback IPv4 addresses for the mDNS
// service record, falling back to loopback only if none are found — so
// mdns.NewMDNSService (which otherwise falls back to a DNS lookup of the
// machine hostname when given an empty IP list) never depends on the
// host's own name actually resolving, which containers/sandboxes commonly
// don't provide.
func localIPs() []net.IP {
	var ips []net.IP
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		ips = append(ips, net.IPv4(127, 0, 0, 1))
	}
	return ips
}
