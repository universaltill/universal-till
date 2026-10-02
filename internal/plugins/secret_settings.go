package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/secrets"
)

// SettingTypeSecret (ADR-0082, ut-docs#1739): the setting holds a
// credential — masked on the settings page, sealed at rest by
// internal/data's PluginRepo.
const SettingTypeSecret = "secret"

// SettingTypeEndpoint (ADR-0121 §2, ut-docs#3328): the setting holds an
// operator-entered http(s)://host[:port][/path] — the address a plugin's
// http:lan / net:@setting grant reaches. ValidEndpointURL is enforced where
// an operator saves the value (POST /api/plugins/{id}/settings); a
// manifest's own default_value is not validated at parse time, the same as
// every other setting type.
const SettingTypeEndpoint = "endpoint"

// isValidSettingType is ParseManifest's enum check for ManifestSetting.Type.
func isValidSettingType(t string) bool {
	return t == "" || t == SettingTypeSecret || t == SettingTypeEndpoint
}

// ValidEndpointURL reports whether v is http(s)://host[:port][/path]: an
// absolute http or https URL with a host, an optional port in 1..65535 and
// an optional path — no userinfo, query or fragment, no surrounding space.
// Anything else (ftp:, javascript:, a bare host, free text) is refused.
func ValidEndpointURL(v string) bool {
	if v == "" || v != strings.TrimSpace(v) {
		return false
	}
	u, err := url.Parse(v)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(v, "#") {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	h := u.Hostname()
	if h == "" {
		return false
	}
	// url.Parse splits the port at the LAST colon, so "erp.lan:8080:80"
	// parses as host "erp.lan:8080" port "80" and would be accepted — only a
	// bracketed IPv6 literal may carry a colon inside the host (independent
	// review, ut-docs#3328).
	if strings.Contains(h, ":") {
		if _, err := netip.ParseAddr(h); err != nil {
			return false
		}
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return false
	}
	return true
}

// SettingDeclaredEndpoint reports whether this manifest declares key with
// `type: "endpoint"`. Nil-safe, like SettingDeclaredSecret.
func (m *Manifest) SettingDeclaredEndpoint(key string) bool {
	if m == nil {
		return false
	}
	for _, s := range m.Settings {
		if s.Key == key {
			return s.Type == SettingTypeEndpoint
		}
	}
	return false
}

// SettingDeclaredSecret reports whether this manifest declares key with
// `type: "secret"`. Nil-safe (a plugin whose manifest could not be read
// simply declares nothing — the key-name heuristic still applies).
func (m *Manifest) SettingDeclaredSecret(key string) bool {
	if m == nil {
		return false
	}
	for _, s := range m.Settings {
		if s.Key == key {
			return s.Type == SettingTypeSecret
		}
	}
	return false
}

// IsSecretSettingKey is the key-name credential heuristic — the rule the
// settings page masks on and internal/data seals on. It lives in
// internal/secrets (which both this package and internal/data can import —
// this package already imports internal/data, so it could not be defined
// here without a cycle); this is the plugin-side name for the same
// function, kept so callers that reason about manifests find it next to
// SettingTypeSecret.
func IsSecretSettingKey(key string) bool { return secrets.IsSecretSettingKey(key) }

// InstalledManifest parses the manifest of the plugin's currently active
// version from its on-disk install tree, paths.Plugins(id, version,
// "manifest.json") — the same layout Exporter/RollbackManager read. The
// Manager keeps no manifest in memory and nothing persists the settings'
// declared types (plugin_settings has no column for it, by design — ADR-0082
// changes no schema), so this is how the settings page learns a key was
// declared `type: "secret"`. ok=false with a nil error means either the
// plugin has no active version, or an active version is recorded but its
// manifest.json is absent — both treated as "nothing to declare" rather
// than an error. That second case is a deliberate, narrower-than-ideal
// scope decision (ut-docs#1739 review, "ABSENT manifest.json" finding):
// this codebase's own test suite (internal/pages' seedForPages, 63+ call
// sites) — and plausibly some real registration paths too — routinely
// creates an active-version DB row with no corresponding on-disk
// manifest.json, so treating that combination as a hard error broke a huge
// swath of unrelated tests reliant on that lightweight fixture. Only a
// manifest.json that EXISTS but cannot be opened or parsed, or a
// traversal-guard trip, or a DB error, is surfaced as an error. A caller
// gating a WRITE on manifest resolution (secretSettingCheck) therefore
// still fails closed against a corrupt/unreadable manifest, but not
// against a plugin that was never fully installed with files on disk in
// the first place — see that finding's own comment for the residual gap
// this leaves (a plugin with an active version, no manifest.json, and a
// secret declared under a key the heuristic doesn't catch is
// indistinguishable from "declares nothing"). Filed as a follow-up rather
// than force a wider test-fixture rework under this card's scope.
func InstalledManifest(ctx context.Context, db *sql.DB, pluginID string) (*Manifest, bool, error) {
	version, ok, err := data.NewPluginRepo(db).GetActivePluginVersion(ctx, pluginID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	// Same traversal guard as Exporter.Export: id/version come from the DB
	// here, not a request, but the resolved path must still stay under the
	// plugin base dir.
	base := filepath.Clean(paths.Plugins())
	dir := filepath.Join(base, pluginID, version)
	if dir == base || !filepath.HasPrefix(dir, base+string(os.PathSeparator)) {
		return nil, false, fmt.Errorf("plugin %s v%s resolves outside the plugin dir", pluginID, version)
	}
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open manifest for %s v%s: %w", pluginID, version, err)
	}
	defer f.Close()
	m, err := ParseManifest(f)
	if err != nil {
		return nil, false, fmt.Errorf("parse manifest for %s v%s: %w", pluginID, version, err)
	}
	return m, true, nil
}
