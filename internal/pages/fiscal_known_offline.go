package pages

import (
	"context"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

const fiscalKnownOfflineReason = "known-offline: dispatch skipped without contacting the signing plugin"

// fiscalKnownOffline decides, per signer, whether a fiscal dispatch for
// event is skipped as known-offline (ADR-0044 D1, amended by ut-docs#3137).
// Callers run it AFTER the HasSubscribers check, so a zero-plugin till never
// reaches it.
//
//   - declared (navigator.onLine said offline): skip only when EVERY
//     subscriber needs the network; one network-free subscriber (no granted
//     net:/tcp:) is enough to dispatch — a local signer signs offline.
//   - not declared: skip only when EVERY subscriber's backend hosts are all
//     probeable and each was confirmed unreachable within the probe TTL
//     (d.SignerReach, background probes, never waited on) and the till's
//     own cloud probe is not Reachable either. Unknown, stale,
//     unprobeable (net:*, tcp:, @setting, http:lan, LAN names) or any read
//     error dispatches as before.
//
// A probe-derived skip never sets SaleInput.Offline: the sale is not
// sync-queued, it just takes the unsigned-and-declared path. The reason names
// the host only, never a URL.
func fiscalKnownOffline(ctx context.Context, d *common.Deps, event string, declared bool) (bool, string) {
	ids := plugins.SharedBus(d.Db).SubscriberIDs(event)
	if len(ids) == 0 {
		return false, ""
	}
	var down string
	allDown := true
	for _, id := range ids {
		needs, err := plugins.PluginNetworkNeeds(ctx, d.Db, id)
		if err != nil {
			logging.L().Warnf("fiscal known-offline: read permissions of %s: %v", id, err)
			needs = plugins.NetworkNeeds{Any: true, Unprobeable: true} // fail safe: as today
		}
		if declared {
			if !needs.Any {
				return false, "" // a network-free signer still signs offline
			}
			continue
		}
		if !needs.Any || needs.Unprobeable || len(needs.Hosts) == 0 {
			allDown = false
			continue
		}
		for _, h := range needs.Hosts {
			if d.SignerReach.For("https://"+h).Fresh() == netreach.Unreachable {
				down = h
			} else {
				allDown = false
			}
		}
	}
	if declared {
		return true, fiscalKnownOfflineReason
	}
	// Corroboration: a probe-derived skip also needs the till's own cloud
	// probe to be not Reachable. A signer host that fails while our cloud
	// answers points at that host (or a port the probe didn't try), not at
	// the network — dispatch and let the budget decide (review, M3).
	if allDown && down != "" && d.NetReach.Status() != netreach.Reachable {
		return true, "signer backend unreachable: " + down
	}
	return false, ""
}

// signerProbeClient is d.SignerReach's client: a direct dial like plugin
// egress (newPluginHTTPClient: Proxy nil), so a stale proxy environment
// can't make a reachable backend look down, and no redirect is followed —
// any response already proves the host answers. Demo-guarded by netaccess.
func signerProbeClient() *http.Client {
	tr := &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 3 * time.Second,
		DisableKeepAlives:   true,
	}
	c := netaccess.NewClientWithTransport(0, tr)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}
