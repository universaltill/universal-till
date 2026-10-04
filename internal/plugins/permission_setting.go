package plugins

// Setting-bound exact grants (ut-docs#2899).
//
// Since #2891, net:* / tcp:* reach PUBLIC addresses only; a LAN or loopback
// target needs the EXACT grant for that host. A plugin whose target is an
// admin-configured endpoint (a fiscal register on the shop network, a local
// bridge, a webhook to a LAN ERP) cannot know that host when its manifest is
// signed, so it declares one of:
//
//	net:@setting:<urlKey>             the host of the URL stored in <urlKey>
//	tcp:@setting:<hostKey>:<portKey>  the (host, port) stored in those keys
//
// Once an admin grants the permission, the address CURRENTLY stored in those
// settings counts as the exact grant. It is resolved on every check (never
// copied into a permission row), so changing the setting moves the grant,
// and an open tcp handle to the old address is refused on its next call.
//
// It relies on the plugin's CODE not writing its own settings. That holds
// for the wasm half: the host has settings_get and no setter; settings are
// written by the admin UI, the owner's cloud directives and LAN sync from
// the main till (and a manifest's default_value at install — the resolved
// address is shown on the settings page next to the permission). It does
// NOT yet hold for a plugin's PAGE: content/index.html is rendered as raw
// same-origin HTML with no CSP (internal/pages/plugin_page.go), so a page
// script opened by a manager could POST /api/plugins/{id}/settings with that
// session and move the grant. Closing that is ut-docs#2892 (CSP / sandboxed
// plugin pages, p1); until it lands, a malicious signed plugin page can
// retarget its own setting-bound grant — the same trust already extended to
// that page for everything else a manager session can do.
// Everything else of the #2891 egress policy still applies:
// the till's own listen port is never reachable, redirects re-check the new
// host, and plain http goes to loopback only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"

	"github.com/universaltill/universal-till/internal/data"
)

const (
	netSettingPrefix = "net:@setting:"
	tcpSettingPrefix = "tcp:@setting:"
)

// ParseSettingBoundPermission reports whether perm is a setting-bound form
// (any net:@… or tcp:@…) and, if well-formed, the setting keys it names: one
// URL key for net:, a host key and a port key for tcp:. A net:@/tcp:@ string
// that does not match either form is bound=true with an error, so a
// manifest carrying a typo is refused instead of silently granting nothing.
func ParseSettingBoundPermission(perm string) (keys []string, bound bool, err error) {
	var rest string
	var want int
	switch {
	case strings.HasPrefix(perm, netSettingPrefix):
		rest, want = strings.TrimPrefix(perm, netSettingPrefix), 1
	case strings.HasPrefix(perm, tcpSettingPrefix):
		rest, want = strings.TrimPrefix(perm, tcpSettingPrefix), 2
	case strings.HasPrefix(perm, "net:@"), strings.HasPrefix(perm, "tcp:@"):
		return nil, true, fmt.Errorf("permission %q: unknown form (use net:@setting:<urlKey> or tcp:@setting:<hostKey>:<portKey>)", perm)
	default:
		return nil, false, nil
	}
	parts := strings.Split(rest, ":")
	if len(parts) != want {
		return nil, true, fmt.Errorf("permission %q: expected %d setting key(s)", perm, want)
	}
	for _, k := range parts {
		if strings.TrimSpace(k) == "" || k != strings.TrimSpace(k) {
			return nil, true, fmt.Errorf("permission %q: empty or padded setting key", perm)
		}
	}
	return parts, true, nil
}

// validateSettingBoundPermissions refuses a manifest whose setting-bound
// permission is malformed or names a key the manifest does not declare in
// its settings (a grant bound to a key nobody can set is a typo).
func validateSettingBoundPermissions(m *Manifest) error {
	declared := make(map[string]bool, len(m.Settings))
	for _, s := range m.Settings {
		declared[s.Key] = true
	}
	for _, p := range m.Permissions {
		keys, bound, err := ParseSettingBoundPermission(p)
		if !bound {
			continue
		}
		if err != nil {
			return fmt.Errorf("manifest %w", err)
		}
		for _, k := range keys {
			if !declared[k] {
				return fmt.Errorf("manifest permission %q names setting %q, which the manifest does not declare", p, k)
			}
		}
	}
	return nil
}

// normGrantHost is the canonical form every grant comparison uses: brackets
// and one trailing dot stripped, IP literals in canonical form (IPv4-mapped
// IPv6 unmapped — it dials the same address), names IDNA-mapped to ASCII
// and lower-cased. A name IDNA refuses falls back to plain lower-casing;
// both sides go through the same function, so that only ever fails closed.
func normGrantHost(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	h = strings.TrimSuffix(h, ".")
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().String()
	}
	if a, err := idna.Lookup.ToASCII(h); err == nil {
		h = a
	}
	return strings.ToLower(h)
}

// grantedPermissions lists the plugin's currently granted permission strings.
func grantedPermissions(ctx context.Context, db *sql.DB, pluginID string) ([]string, error) {
	rows, err := data.NewPluginRepo(db).ListPermissions(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Granted {
			out = append(out, r.Permission)
		}
	}
	return out, nil
}

// pluginSettingString reads one of the plugin's own settings as the plugin
// sees it through settings_get (same scope precedence, JSON string
// unwrapped). ok is false when unset or unreadable — no grant.
func pluginSettingString(ctx context.Context, db *sql.DB, pluginID, key string) (string, bool) {
	raw, found, err := data.NewPluginRepo(db).GetPluginSetting(ctx, pluginID, key)
	if err != nil || !found {
		return "", false
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		raw = s
	}
	raw = strings.TrimSpace(raw)
	return raw, raw != ""
}

// settingBoundNetHost is the normalised host of the URL stored in urlKey.
// A value that is not an absolute URL with a host grants nothing.
func settingBoundNetHost(ctx context.Context, db *sql.DB, pluginID, urlKey string) (string, bool) {
	v, ok := pluginSettingString(ctx, db, pluginID, urlKey)
	if !ok {
		return "", false
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", false
	}
	return normGrantHost(u.Hostname()), true
}

// settingBoundTCPAddr is the normalised (host, port) stored in hostKey and
// portKey. A missing or out-of-range port grants nothing.
func settingBoundTCPAddr(ctx context.Context, db *sql.DB, pluginID, hostKey, portKey string) (string, int, bool) {
	h, ok := pluginSettingString(ctx, db, pluginID, hostKey)
	if !ok {
		return "", 0, false
	}
	ps, ok := pluginSettingString(ctx, db, pluginID, portKey)
	if !ok {
		return "", 0, false
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return normGrantHost(h), port, true
}

// SettingBoundTarget is the address a well-formed setting-bound permission
// unlocks RIGHT NOW, for display next to the permission (ut-docs#2899
// review): the normalised host of the URL in <urlKey> for net:, host:port
// for tcp:. ok is false when the settings are unset or unusable — the
// permission then grants nothing. Only the host (and port) is returned,
// never the rest of the URL (a path or query may carry a token).
func SettingBoundTarget(ctx context.Context, db *sql.DB, pluginID, perm string) (string, bool) {
	keys, bound, err := ParseSettingBoundPermission(perm)
	if !bound || err != nil {
		return "", false
	}
	if strings.HasPrefix(perm, netSettingPrefix) {
		return settingBoundNetHost(ctx, db, pluginID, keys[0])
	}
	h, port, ok := settingBoundTCPAddr(ctx, db, pluginID, keys[0], keys[1])
	if !ok {
		return "", false
	}
	return net.JoinHostPort(h, strconv.Itoa(port)), true
}

// netGrantMatch reports how the plugin's granted permissions cover host:
// exact (net:<host> after normalisation, or a net:@setting whose URL host is
// host) or only the public-only wildcard net:*.
func netGrantMatch(ctx context.Context, db *sql.DB, pluginID, host string) (exact, wildcard bool, err error) {
	perms, err := grantedPermissions(ctx, db, pluginID)
	if err != nil {
		return false, false, err
	}
	want := normGrantHost(host)
	for _, p := range perms {
		switch {
		case p == "net:*":
			wildcard = true
		case strings.HasPrefix(p, "net:@"):
			keys, _, perr := ParseSettingBoundPermission(p)
			if perr != nil {
				continue
			}
			if h, ok := settingBoundNetHost(ctx, db, pluginID, keys[0]); ok && h == want {
				return true, wildcard, nil
			}
		case strings.HasPrefix(p, "net:"):
			if normGrantHost(strings.TrimPrefix(p, "net:")) == want {
				return true, wildcard, nil
			}
		}
	}
	return false, wildcard, nil
}

// tcpGrantMatch is netGrantMatch for tcp_open: addr is host:port (as built
// by tcpAddr); exact means tcp:<host>:<port> or a tcp:@setting naming this
// (host, port); wildcard means tcp:*.
func tcpGrantMatch(ctx context.Context, db *sql.DB, pluginID, addr string) (exact, wildcard bool, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return false, false, nil
	}
	wantPort, err := strconv.Atoi(portStr)
	if err != nil {
		return false, false, nil
	}
	perms, err := grantedPermissions(ctx, db, pluginID)
	if err != nil {
		return false, false, err
	}
	want := normGrantHost(host)
	for _, p := range perms {
		switch {
		case p == "tcp:*":
			wildcard = true
		case strings.HasPrefix(p, "tcp:@"):
			keys, _, perr := ParseSettingBoundPermission(p)
			if perr != nil {
				continue
			}
			if h, port, ok := settingBoundTCPAddr(ctx, db, pluginID, keys[0], keys[1]); ok && h == want && port == wantPort {
				return true, wildcard, nil
			}
		case strings.HasPrefix(p, "tcp:"):
			gh, n, ok := splitTCPGrantAddr(strings.TrimPrefix(p, "tcp:"))
			if ok && n == wantPort && normGrantHost(gh) == want {
				return true, wildcard, nil
			}
		}
	}
	return false, wildcard, nil
}

// Validation-data grants (ut-docs#3226, ADR-0121 amendment 2026-10-02).
//
//	net:validation:<host>   one named host, exact — no wildcard, no @setting
//
// A PAdES-B-LTA seal (ADR-0132 §5) embeds OCSP responses, CRLs and a
// timestamp. Those are served almost always over plain http:// (the data is
// itself signed) and a CRL or EU trusted list can exceed http_request's
// 256 KiB cap. For a host named this way — and only that host —
// http_request allows plain http and reads up to validationResponseCap.
// It is NOT an exact grant at dial time: the host must still resolve to a
// public address (as under net:*), so the relaxation never reaches the LAN,
// loopback or a metadata address. It never widens netGrantMatch either: an
// ordinary net:<host> or net:* grant keeps https-only and the 256 KiB cap.
const netValidationPrefix = "net:validation:"

// ParseValidationPermission reports whether perm is a validation-data grant
// (any net:validation:…) and, if well-formed, its normalised host. A
// net:validation: string with no host, a wildcard, a port, a scheme, a path
// or padding is isValidation=true with an error, so a manifest carrying it
// is refused instead of silently granting nothing (or something broader).
func ParseValidationPermission(perm string) (host string, isValidation bool, err error) {
	if !strings.HasPrefix(perm, netValidationPrefix) {
		return "", false, nil
	}
	raw := strings.TrimPrefix(perm, netValidationPrefix)
	bad := func(why string) (string, bool, error) {
		return "", true, fmt.Errorf("permission %q: %s (use net:validation:<host>, one exact host)", perm, why)
	}
	switch {
	case raw == "":
		return bad("no host")
	case strings.IndexFunc(raw, unicode.IsSpace) >= 0:
		return bad("padded or contains whitespace")
	case strings.HasPrefix(raw, "[") != strings.HasSuffix(raw, "]"):
		return bad("unbalanced brackets")
	case strings.Contains(raw, "*"):
		return bad("wildcards are not allowed")
	case strings.ContainsAny(raw, "/@?#"):
		return bad("a host only — no scheme, path, query or setting reference")
	}
	if strings.HasPrefix(raw, "[") {
		// Brackets (balanced, checked above) only ever wrap an IPv6 literal.
		if a, perr := netip.ParseAddr(raw[1 : len(raw)-1]); perr != nil || !a.Is6() {
			return bad("brackets must wrap an IPv6 address")
		}
	} else if strings.Contains(raw, ":") {
		// Only an IP literal may contain a colon — never host:port.
		if _, perr := netip.ParseAddr(raw); perr != nil {
			return bad("a host only — no port")
		}
	}
	h := normGrantHost(raw)
	if h == "" {
		return bad("no host")
	}
	return h, true, nil
}

// validateValidationPermissions refuses a manifest whose net:validation:
// permission is malformed.
func validateValidationPermissions(m *Manifest) error {
	for _, p := range m.Permissions {
		if _, is, err := ParseValidationPermission(p); is && err != nil {
			return fmt.Errorf("manifest %w", err)
		}
	}
	return nil
}

// validationGrantMatch reports whether the plugin holds a granted, well-formed
// net:validation:<host> for exactly host (after normalisation). Separate from
// netGrantMatch on purpose: it unlocks plain http and the larger response
// cap only, never the exact/LAN half of the egress policy.
func validationGrantMatch(ctx context.Context, db *sql.DB, pluginID, host string) (bool, error) {
	perms, err := grantedPermissions(ctx, db, pluginID)
	if err != nil {
		return false, err
	}
	want := normGrantHost(host)
	if want == "" {
		return false, nil
	}
	for _, p := range perms {
		if h, is, perr := ParseValidationPermission(p); is && perr == nil && h == want {
			return true, nil
		}
	}
	return false, nil
}

// splitTCPGrantAddr splits the <host>:<port> of an exact tcp:<host>:<port>
// grant, the inverse of tcpAddr (net.JoinHostPort, so an IPv6 host is
// bracketed). Shared by tcpGrantMatch and the manifest permission allow-list
// (isKnownPermission) so both read a grant the same way.
func splitTCPGrantAddr(addr string) (host string, port int, ok bool) {
	h, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(ps)
	if err != nil {
		return "", 0, false
	}
	return h, n, true
}
