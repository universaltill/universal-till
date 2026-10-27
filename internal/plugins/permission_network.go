package plugins

// What a plugin's backend needs from the network, read off its GRANTED
// permissions, for the per-signer fiscal known-offline check (ADR-0044 D1,
// ut-docs#3137). No new manifest field: a signer already declares its
// backend as net:<host>. Only a public DNS name the plugin reaches by name
// is probeable (the probe tries https on 443 and counts only network-down
// errors, since a net:<host> grant does not pin the port); any
// doubt makes the plugin unprobeable, because a false "unreachable" leaves a
// sale unsigned for good (ADR-0056) while a missed one only costs the budget.

import (
	"context"
	"database/sql"
	"net/netip"
	"strings"
)

// NetworkNeeds summarises the granted net:/tcp: permissions of one plugin.
type NetworkNeeds struct {
	// Any: the plugin may use the network at all. False means it is
	// network-free and can work while the till is offline.
	Any bool
	// Hosts are the normalised public host names a probe can target
	// (net:<host>, or the host of a net:@setting: URL).
	Hosts []string
	// Unprobeable: some grant cannot be probed (net:*, net:{account}, tcp:,
	// net:@setting: — its URL may carry a scheme or port the probe would not
	// use —, http:lan or net:validation: — the plugin may speak plain http —,
	// an IP literal, a single-label or reserved LAN name).
	// Such a plugin is never treated as unreachable.
	Unprobeable bool
}

// PluginNetworkNeeds reads the plugin's granted permissions. A read error
// is returned; callers treat it as "needs the network" (fail safe).
func PluginNetworkNeeds(ctx context.Context, db *sql.DB, pluginID string) (NetworkNeeds, error) {
	perms, err := grantedPermissions(ctx, db, pluginID)
	if err != nil {
		return NetworkNeeds{}, err
	}
	var n NetworkNeeds
	add := func(host string) {
		host = normGrantHost(host)
		if !probeableHost(host) {
			n.Unprobeable = true
			return
		}
		for _, h := range n.Hosts {
			if h == host {
				return
			}
		}
		n.Hosts = append(n.Hosts, host)
	}
	for _, p := range perms {
		switch {
		case strings.HasPrefix(p, "tcp:"):
			n.Any, n.Unprobeable = true, true
		case p == "http:lan":
			n.Unprobeable = true
		case strings.HasPrefix(p, netValidationPrefix), strings.HasPrefix(p, "net:@"):
			n.Any, n.Unprobeable = true, true
		case strings.HasPrefix(p, "net:"):
			n.Any = true
			if h := strings.TrimPrefix(p, "net:"); h == "*" || strings.HasPrefix(h, "{") || strings.HasPrefix(h, "@") {
				n.Unprobeable = true
			} else {
				add(h)
			}
		}
	}
	return n, nil
}

// lanSuffixes are reserved or conventional local-network name suffixes; a
// device behind one may speak plain http (RFC 6762, RFC 8375).
var lanSuffixes = []string{".local", ".localhost", ".invalid", ".lan", ".home.arpa", ".internal", ".localdomain", ".box", ".home", ".corp"}

// probeableHost: a dotted public-looking DNS name (no IP literal, port,
// path, single-label or reserved LAN name).
func probeableHost(h string) bool {
	if h == "" || !strings.Contains(h, ".") || strings.ContainsAny(h, ":/@ ") {
		return false
	}
	for _, suf := range lanSuffixes {
		if strings.HasSuffix(h, suf) {
			return false
		}
	}
	_, err := netip.ParseAddr(h)
	return err != nil
}
