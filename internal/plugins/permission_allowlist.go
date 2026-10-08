package plugins

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Manifest permission allow-list (ut-docs#3328, ADR-0121 §2). Until this,
// any string in Manifest.Permissions was persisted and shown to an operator
// for granting, even one no code ever checks. isKnownPermission accepts
// every shape core grants today plus the ADR-0121 §2 names, and nothing
// else. Adding a permission to core means adding it here.

// exactPermissions are the permissions matched as whole strings.
var exactPermissions = map[string]bool{
	// Checked by core today.
	"storage":                 true, // wasm_hostfns.go storage_get/storage_set
	"events:receive":          true, // ipc.go event delivery
	"payments:reconciliation": true, // ipc.go reconciliation payloads
	"tcp:*":                   true, // wasm_tcp.go public-only wildcard
	"devices:printer":         true, // data.PluginRepo.HasActivePrinterCapability

	// ADR-0121 §2. Runtime enforcement lands with the §3 build cards;
	// http:lan and http:stream are enforced (ut-docs#3156, wasm_egress.go
	// admitHTTPHop, wasm_httpstream.go).
	"http:lan":        true,
	"http:stream":     true,
	"db:own":          true,
	"blob:own":        true,
	"schedule":        true,
	"cloud:directive": true,
	"secret:write":    true,
	"ui:page":         true,

	// ADR-0140 (ut-docs#3862): ★ review-gated, read-only device identity —
	// wasm_device_info.go device_id_get / device_local_ips_get /
	// device_timezone_get. Bare: no wildcard or parameter form.
	"device-info": true,

	// Grandfathered: dotted names published plugin repos declare
	// (ut-plugin-language-* ui.locale, ut-plugin-theme*/themes ui.theme,
	// ut-plugin-faq ui.page, ut-plugin-integration-ai ai.configure,
	// ut-plugin-payment-* pos.tender). Core checks none of them, but refusing
	// them would stop those plugins installing and make an installed one's
	// settings page refuse writes (InstalledManifest parses the manifest).
	// New plugins use the names above instead.
	"ui.locale":    true,
	"ui.theme":     true,
	"ui.page":      true,
	"ai.configure": true,
	"pos.tender":   true,
}

var (
	// <entity>:read / <entity>:write (ut-docs#228, #2899): entity names are
	// plugin-defined (sales, inventory, items, tax_codes, fiscal_register_de,
	// the dynamic e+":write" in internal/pages/import_dispatch.go), so this
	// is a shape, not an enum.
	entityPermissionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*:(read|write)$`)
	// Grandfathered legacy quota form, predates bare "storage" and is checked
	// nowhere — still declared by plugins/tax-tr and the signed marketplace
	// fixture; new plugins use storage / db:own / blob:own instead.
	legacyStorageQuotaRe = regexp.MustCompile(`^storage\.local\.\d+[KMG]B$`)
	// view:<class> (ADR-0121 §5): the permission names a class of core read
	// views — view:sales, view:inventory, view:audit — not one view
	// (sales.by_day.v1 goes in views_used).
	viewPermissionRe = regexp.MustCompile(`^view:[a-z][a-z0-9_]*$`)
	// grantHostnameRe: a DNS name after normGrantHost (IDNA-mapped,
	// lower-cased) — LDH labels, '_' tolerated for LAN hosts, no wildcard,
	// scheme, path or port. An IP literal is accepted by netip instead.
	grantHostnameRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)
)

// validGrantHost reports whether h can be the host of an exact net:/tcp:
// grant: an IP literal (bracketed IPv6 included) or a DNS name. net:* and
// tcp:* are handled by their callers. A host netGrantMatch/tcpGrantMatch
// could never match (a wildcard pattern, a URL, host:port, a path) is
// refused at parse so the badge an operator consents to is never one that
// grants nothing (independent review, ut-docs#3328).
func validGrantHost(h string) bool {
	n := normGrantHost(h)
	if n == "" {
		return false
	}
	if _, err := netip.ParseAddr(n); err == nil {
		return true
	}
	return grantHostnameRe.MatchString(n)
}

// isKnownPermission reports whether perm is a permission shape core
// recognises.
func isKnownPermission(perm string) bool {
	// net:@… / tcp:@… are setting-bound: valid iff well-formed.
	if _, bound, err := ParseSettingBoundPermission(perm); bound {
		return err == nil
	}
	// net:validation:<host> (ut-docs#3226, ADR-0121 amendment): a narrower
	// grant shape than plain net:<host>, checked before the generic net:
	// case below since both share the "net:" prefix.
	if _, isValidation, err := ParseValidationPermission(perm); isValidation {
		return err == nil
	}
	if exactPermissions[perm] {
		return true
	}
	switch {
	case strings.HasPrefix(perm, "net:"):
		// net:<host> exact grant, or net:* (public-only wildcard).
		host := strings.TrimPrefix(perm, "net:")
		return host == "*" || validGrantHost(host)
	case strings.HasPrefix(perm, "tcp:"):
		// tcp:<host>:<port>, read exactly as tcpGrantMatch reads it.
		host, port, ok := splitTCPGrantAddr(strings.TrimPrefix(perm, "tcp:"))
		return ok && validGrantHost(host) && port > 0 && port <= 65535
	case strings.HasPrefix(perm, "view:"):
		return viewPermissionRe.MatchString(perm)
	case strings.HasPrefix(perm, "ui:slot:"):
		// ui:slot:<slot> (ADR-0121 §7): the slot is one of the core content
		// slots, the same closed set an entry's `slot` field is checked
		// against (validateABI3Fields).
		return isContentSlot(strings.TrimPrefix(perm, "ui:slot:"))
	}
	return entityPermissionRe.MatchString(perm) || legacyStorageQuotaRe.MatchString(perm)
}

// validatePermissions refuses a manifest declaring a permission core does
// not recognise. Called by ParseManifest and, for the marketplace install
// path that never goes through ParseManifest, ManifestVerifier.VerifyManifest.
func validatePermissions(m *Manifest) error {
	for _, p := range m.Permissions {
		if !isKnownPermission(p) {
			return fmt.Errorf("manifest permission %q is not a recognized permission shape", p)
		}
	}
	return nil
}
