package plugins

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ADR-0169 (ut-docs#4187, card ut-docs#4759): the `channel` plugin type and
// the manifest fields that go with it. A channel plugin is the adapter of a
// sales channel (a web shop or marketplace). It is a signed WASM plugin that
// runs in the ut-cloud channel host and NEVER on a till: it declares
// `hosts: ["cloud"]`, a till lists it and refuses to run or install it.
//
// All fields are omitempty and sit at the end of Manifest, so a manifest
// without them marshals byte-identically to before and old signatures keep
// verifying. ut-cloud's internal/signing.CanonicalManifest must mirror every
// one (same position, same tags), and the nested types too. The contract is
// ut-docs reference/contracts/sales-channel-v1.md.

// Hosts values (ADR-0169 section 3). A manifest without `hosts` runs on a
// till, as every plugin did before this field existed.
const (
	HostTill  = "till"
	HostCloud = "cloud"
)

// ChannelType is the canonical type of a sales-channel adapter.
const ChannelType = "channel"

// ChannelContractVersion is the only `channel_contract` this till can list.
// A new version is a new contract (sales-channel-v2), never an edit of v1.
const ChannelContractVersion = 1

// ChannelAccountPermission is the one permission a channel plugin may hold:
// the host binds {account} to the connection's verified account.
const ChannelAccountPermission = "net:{account}"

// ErrCloudHostedPlugin is returned when a till is asked to install or run a
// plugin whose `hosts` does not include "till" (ADR-0169 section 3).
var ErrCloudHostedPlugin = errors.New("plugin runs in the cloud, not on a till")

// Channel capability entities and directions (contract section 1).
var (
	channelCapabilityEntities = []string{
		"items", "variants", "categories", "images", "stock", "orders",
		"fulfilment", "refunds", "cancel", "customers", "compare_at", "item_create",
	}
	channelCapabilityDirections = []string{"push", "pull", "both"}
	channelKinds                = []string{"webshop", "marketplace"}
	channelAuthGrants           = []string{"code", "client_credentials"}
)

const (
	maxChannelAccountPatternLen = 256
	maxChannelTopics            = 64
	maxChannelScopes            = 64
	maxChannelCapabilityCount   = 32
)

var (
	// channelSuffixRe: a leading dot and at least two lower-case DNS labels,
	// so ".myshopify.com" passes while ".com" (which would let a credential
	// go to any .com host) and "myshopify.com" (no dot, matches
	// "evilmyshopify.com") do not.
	channelSuffixRe = regexp.MustCompile(`^(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?){2,}$`)
	// channelHeaderRe: an HTTP field-name token.
	channelHeaderRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`)
	channelScopeRe  = regexp.MustCompile(`^[a-z0-9_:.\-]{1,100}$`)
	channelTopicRe  = regexp.MustCompile(`^[a-z0-9_./\-]{1,100}$`)
	// channelBareEventRe: a bare schedule event name. A channel plugin runs
	// in the cloud host, where its schedule fires a `channel.tick` for the
	// connection, so there is no shared event bus to collide on and the
	// ADR-0121 own-id prefix is not required (a namespaced name is also fine).
	channelBareEventRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// forbiddenChannelHeaders cannot carry the credential: they frame the
// request or are controlled by the host.
var forbiddenChannelHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true,
	"connection": true, "upgrade": true, "te": true, "trailer": true,
	"proxy-authorization": true, "proxy-authenticate": true, "expect": true,
	"content-type": true, "content-encoding": true, "cookie": true, "set-cookie": true,
	"forwarded": true, "origin": true, "max-forwards": true, "keep-alive": true,
	"x-forwarded-for": true, "x-forwarded-host": true, "x-forwarded-proto": true, "x-real-ip": true,
}

// RunsOnTill reports whether this plugin may run on a till. A manifest
// without `hosts` runs on a till (ADR-0169 section 3).
func (m *Manifest) RunsOnTill() bool {
	return len(m.Hosts) == 0 || slices.Contains(m.Hosts, HostTill)
}

// IsCloudHosted reports whether the plugin runs in the ut-cloud channel host.
func (m *Manifest) IsCloudHosted() bool { return slices.Contains(m.Hosts, HostCloud) }

// RefuseOnTill returns ErrCloudHostedPlugin (wrapped with the plugin id)
// when the plugin does not run on a till. Every install and load path calls
// it; a nil manifest is not refused here (the caller has other checks).
func (m *Manifest) RefuseOnTill() error {
	if m == nil || m.RunsOnTill() {
		return nil
	}
	return fmt.Errorf("%w: %s declares hosts %v (ADR-0169)", ErrCloudHostedPlugin, m.ID, m.Hosts)
}

// validateChannelFields refuses a manifest whose ADR-0169 fields are
// malformed. Called by ParseManifest and ManifestVerifier.VerifyManifest.
func validateChannelFields(m *Manifest) error {
	if err := validateHosts(m); err != nil {
		return err
	}
	if m.CanonicalType != ChannelType {
		switch {
		case m.Channel != nil:
			return fmt.Errorf("manifest channel block is only allowed on a plugin of canonical_type %q", ChannelType)
		case m.ChannelContract != 0:
			return fmt.Errorf("manifest channel_contract is only allowed on a plugin of canonical_type %q", ChannelType)
		case m.IsCloudHosted():
			return fmt.Errorf("manifest hosts %q is only allowed on a plugin of canonical_type %q (ADR-0121 D2, ADR-0169)", HostCloud, ChannelType)
		}
		return nil
	}

	// A channel plugin: cloud-hosted, contract 1, one block, one permission.
	if len(m.Hosts) != 1 || m.Hosts[0] != HostCloud {
		return fmt.Errorf("a channel plugin must declare hosts [%q] (got %v)", HostCloud, m.Hosts)
	}
	if m.ChannelContract != ChannelContractVersion {
		return fmt.Errorf("manifest channel_contract %d is not served; this till knows channel_contract %d", m.ChannelContract, ChannelContractVersion)
	}
	if m.Channel == nil {
		return errors.New("a channel plugin must declare a channel block")
	}
	if err := refuseTillPluginFields(m); err != nil {
		return err
	}
	if m.Runtime != "wasm" {
		return fmt.Errorf("a channel plugin must use runtime %q (got %q)", "wasm", m.Runtime)
	}
	if len(m.Permissions) != 1 || m.Permissions[0] != ChannelAccountPermission {
		return fmt.Errorf("a channel plugin's only permission is %q (got %v)", ChannelAccountPermission, m.Permissions)
	}
	return validateChannelBlock(m.ID, m.Channel)
}

// refuseTillPluginFields: a channel plugin runs in the cloud host, which
// reads only the channel block. Every other capability surface of a till
// plugin (entries, settings, hooks, a database, limits that a host might
// honour, schedules outside the block, ...) is refused, so the manifest
// cannot ask for more than the one permission it is allowed.
func refuseTillPluginFields(m *Manifest) error {
	var field string
	switch {
	case len(m.Entries) > 0:
		field = "entries"
	case len(m.Settings) > 0:
		field = "settings"
	case len(m.Hooks) > 0:
		field = "hooks"
	case len(m.Provides) > 0:
		field = "provides"
	case len(m.Markets) > 0:
		field = "markets"
	case m.WasmABI != 0:
		field = "wasm_abi"
	case m.Limits != nil:
		field = "limits"
	case len(m.Schedules) > 0:
		field = "schedules (use channel.schedules)"
	case m.DB != nil:
		field = "db"
	case m.Retention != nil:
		field = "retention"
	case len(m.ViewsUsed) > 0:
		field = "views_used"
	case len(m.SettingsTabs) > 0 || len(m.SettingsTests) > 0 || m.SettingsSetup != nil:
		field = "settings_tabs, settings_tests and settings_setup"
	}
	if field != "" {
		return fmt.Errorf("a channel plugin may not declare %s: it runs in the cloud host and reads only the channel block", field)
	}
	return nil
}

// adversarialAccountPrefixes are strings no real shop account contains. The
// account pattern must not match any of them in front of the suffix: a
// pattern like ^.*$ would let "evil.example/x"+suffix pass as a host and
// steer the token exchange elsewhere.
var adversarialAccountPrefixes = []string{
	"evil.example/x", "evil.example/", "a@b", "a:b", "a?b", "a#b", "a b", "a\\b", "a/b",
	"", ".", "a_b", "a\n", "a\x00b", "[::1]", "127.0.0.1",
}

func validateHosts(m *Manifest) error {
	if m.Hosts == nil {
		return nil
	}
	if len(m.Hosts) == 0 {
		return errors.New("manifest hosts must not be empty (omit it for a till plugin)")
	}
	seen := map[string]bool{}
	for _, h := range m.Hosts {
		if h != HostTill && h != HostCloud {
			return fmt.Errorf("manifest hosts %q is not %q or %q", h, HostTill, HostCloud)
		}
		if seen[h] {
			return fmt.Errorf("manifest hosts lists %q twice", h)
		}
		seen[h] = true
	}
	return nil
}

func validateChannelBlock(pluginID string, c *ManifestChannel) error {
	if !slices.Contains(channelKinds, c.Kind) {
		return fmt.Errorf("manifest channel.kind %q is not one of %s", c.Kind, strings.Join(channelKinds, "|"))
	}
	if err := validateChannelAccount(c.Account, c.AccountSuffix); err != nil {
		return err
	}
	if err := validateChannelAuth(c.Auth); err != nil {
		return err
	}
	if len(c.Capabilities) > maxChannelCapabilityCount {
		return fmt.Errorf("manifest channel.capabilities lists %d entries (at most %d)", len(c.Capabilities), maxChannelCapabilityCount)
	}
	keys := make([]string, 0, len(c.Capabilities))
	for k := range c.Capabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(channelCapabilityEntities, k) {
			return fmt.Errorf("manifest channel.capabilities has unknown capability %q (allowed: %s)", k, strings.Join(channelCapabilityEntities, ", "))
		}
		if v := c.Capabilities[k]; !slices.Contains(channelCapabilityDirections, v) {
			return fmt.Errorf("manifest channel.capabilities[%q] is %q (allowed: %s, or omit the capability)", k, v, strings.Join(channelCapabilityDirections, "|"))
		}
	}
	if l := c.Limits; l != nil && (l.MaxOptions < 0 || l.MaxVariants < 0) {
		return errors.New("manifest channel.limits must not be negative")
	}
	if err := validateChannelPushFields(c.PushFields); err != nil {
		return err
	}
	if err := validateChannelWebhooks(c.Webhooks); err != nil {
		return err
	}
	for i, s := range c.Schedules {
		if !channelBareEventRe.MatchString(s.Event) {
			if err := checkPluginEventName(pluginID, s.Event); err != nil {
				return fmt.Errorf("manifest channel.schedules[%d].event %q must be a lower-case name like \"reconcile\" or <plugin-id>.<name>", i, s.Event)
			}
		}
		if s.EveryS < minScheduleEveryS {
			return fmt.Errorf("manifest channel.schedules[%d].every_s must be at least %d (got %d)", i, minScheduleEveryS, s.EveryS)
		}
		if s.JitterS < 0 || s.JitterS > s.EveryS {
			return fmt.Errorf("manifest channel.schedules[%d].jitter_s must be between 0 and every_s (got %d)", i, s.JitterS)
		}
	}
	return nil
}

// channelPushFields are the values channel.push_fields may list (ut-docs#4911).
var channelPushFields = []string{"name", "description", "sku", "barcodes", "price", "compare_at", "category_path"}

// validateChannelPushFields: every value known, none twice. Empty = every field.
func validateChannelPushFields(fields []string) error {
	seen := map[string]bool{}
	for _, f := range fields {
		if !slices.Contains(channelPushFields, f) {
			return fmt.Errorf("manifest channel.push_fields has unknown field %q (allowed: %s)", f, strings.Join(channelPushFields, ", "))
		}
		if seen[f] {
			return fmt.Errorf("manifest channel.push_fields lists %q twice", f)
		}
		seen[f] = true
	}
	return nil
}

// validateChannelAccount checks the account pattern and the fixed suffix.
// The host requires BOTH; the suffix, not the plugin's regex alone, bounds
// where a credential can be sent (ADR-0169 section 3).
func validateChannelAccount(pattern, suffix string) error {
	if pattern == "" || len(pattern) > maxChannelAccountPatternLen {
		return fmt.Errorf("manifest channel.account must be a pattern of 1 to %d characters", maxChannelAccountPatternLen)
	}
	if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") {
		return errors.New("manifest channel.account must be anchored (start with ^, end with $)")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("manifest channel.account is not a valid pattern: %v", err)
	}
	if !channelSuffixRe.MatchString(suffix) {
		return fmt.Errorf("manifest channel.account_suffix %q must be a dot followed by at least two lower-case DNS labels, like .myshopify.com", suffix)
	}
	domain := strings.TrimPrefix(suffix, ".")
	labels := strings.Split(domain, ".")
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
		return fmt.Errorf("manifest channel.account_suffix %q ends in a number: an address, not a domain", suffix)
	}
	// A suffix that is itself an ICANN public suffix (.co.uk, .com.au) puts
	// every registrant under it in scope, so it bounds nothing. A private
	// suffix such as .myshopify.com (one platform, many shops) is what the
	// marketplace reviews and approves.
	if ps, icann := publicsuffix.PublicSuffix(domain); icann && ps == domain {
		return fmt.Errorf("manifest channel.account_suffix %q is a public suffix: it would allow any registrant under it", suffix)
	}
	for _, pre := range adversarialAccountPrefixes {
		if re.MatchString(pre + suffix) {
			return fmt.Errorf("manifest channel.account pattern accepts %q in front of the suffix: it must match only a plain host name label", pre+suffix)
		}
	}
	return nil
}

func validateChannelAuth(a *ManifestChannelAuth) error {
	if a == nil {
		return errors.New("manifest channel.auth is required")
	}
	if len(a.Grants) == 0 {
		return errors.New("manifest channel.auth.grants must list code, client_credentials or both")
	}
	seen := map[string]bool{}
	for _, g := range a.Grants {
		if !slices.Contains(channelAuthGrants, g) {
			return fmt.Errorf("manifest channel.auth.grants %q is not one of %s", g, strings.Join(channelAuthGrants, "|"))
		}
		if seen[g] {
			return fmt.Errorf("manifest channel.auth.grants lists %q twice", g)
		}
		seen[g] = true
	}
	if seen["code"] || a.AuthorizeURL != "" {
		if err := checkChannelTemplateURL("authorize_url", a.AuthorizeURL); err != nil {
			return err
		}
	}
	if err := checkChannelTemplateURL("token_url", a.TokenURL); err != nil {
		return err
	}
	if len(a.Scopes) == 0 || len(a.Scopes) > maxChannelScopes {
		return fmt.Errorf("manifest channel.auth.scopes must list 1 to %d scopes", maxChannelScopes)
	}
	for _, s := range a.Scopes {
		if !channelScopeRe.MatchString(s) {
			return fmt.Errorf("manifest channel.auth.scopes %q is not a valid scope", s)
		}
	}
	if a.Inject == nil || a.Inject.Header == "" {
		return errors.New("manifest channel.auth.inject.header is required")
	}
	if !channelHeaderRe.MatchString(a.Inject.Header) || forbiddenChannelHeaders[strings.ToLower(a.Inject.Header)] {
		return fmt.Errorf("manifest channel.auth.inject.header %q cannot carry a credential", a.Inject.Header)
	}
	return nil
}

// checkChannelTemplateURL requires https://{account}/<path>: the only host
// an authorize or token URL may have is the connection's own verified
// account, so the plugin cannot send an OAuth code or secret elsewhere.
func checkChannelTemplateURL(field, raw string) error {
	const prefix = "https://{account}/"
	if !strings.HasPrefix(raw, prefix) {
		return fmt.Errorf("manifest channel.auth.%s %q must start with %s", field, raw, prefix)
	}
	u, err := url.Parse(strings.Replace(raw, "{account}", "account.invalid", 1))
	if err != nil || u.Host != "account.invalid" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Path, "{}") {
		return fmt.Errorf("manifest channel.auth.%s %q must be https://{account}/<path> with no credentials, query or other template", field, raw)
	}
	return nil
}

func validateChannelWebhooks(w *ManifestChannelWebhook) error {
	if w == nil {
		return nil
	}
	if len(w.Topics) > maxChannelTopics {
		return fmt.Errorf("manifest channel.webhooks.topics lists %d topics (at most %d)", len(w.Topics), maxChannelTopics)
	}
	seen := map[string]bool{}
	for _, t := range w.Topics {
		if !channelTopicRe.MatchString(t) {
			return fmt.Errorf("manifest channel.webhooks.topics %q is not a valid topic", t)
		}
		if seen[t] {
			return fmt.Errorf("manifest channel.webhooks.topics lists %q twice", t)
		}
		seen[t] = true
	}
	v := w.Verify
	if v == nil {
		if len(w.Topics) > 0 {
			return errors.New("manifest channel.webhooks.verify is required when topics are listed (the host verifies every webhook)")
		}
		return nil
	}
	if !channelHeaderRe.MatchString(v.Header) || forbiddenChannelHeaders[strings.ToLower(v.Header)] {
		return fmt.Errorf("manifest channel.webhooks.verify.header %q is not a usable header name", v.Header)
	}
	if v.Alg != "hmac-sha256" {
		return fmt.Errorf("manifest channel.webhooks.verify.alg %q is not supported (hmac-sha256)", v.Alg)
	}
	if v.Encoding != "base64" && v.Encoding != "hex" {
		return fmt.Errorf("manifest channel.webhooks.verify.encoding %q is not base64 or hex", v.Encoding)
	}
	return nil
}
