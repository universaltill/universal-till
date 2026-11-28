package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// ADR-0169 / ut-docs#4759: the `channel` type and its manifest block. All
// data here is invented.

func channelFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/channel_demo_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// path edits: "channel.auth.inject.header" -> value; nil deletes the key.
func setPath(t *testing.T, m map[string]any, path string, v any) {
	t.Helper()
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("no object at %q in %q", p, path)
		}
		cur = next
	}
	last := parts[len(parts)-1]
	if v == nil {
		delete(cur, last)
		return
	}
	cur[last] = v
}

func parseChannel(t *testing.T, m map[string]any) (*Manifest, error) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return ParseManifest(bytes.NewReader(raw))
}

func TestChannelIsTheTwentyThirdCanonicalType(t *testing.T) {
	if !slices.Contains(CanonicalTypes, "channel") {
		t.Fatal("channel must be a canonical type (ADR-0169 section 3)")
	}
	if len(CanonicalTypes) != 23 {
		t.Fatalf("want 23 canonical types, have %d", len(CanonicalTypes))
	}
	if !isValidCanonicalType("channel") {
		t.Fatal("channel must pass isValidCanonicalType")
	}
}

func TestParseManifest_ChannelContractExample(t *testing.T) {
	m, err := parseChannel(t, channelFixture(t))
	if err != nil {
		t.Fatalf("the contract's own example must parse: %v", err)
	}
	if m.RunsOnTill() || !m.IsCloudHosted() {
		t.Fatalf("hosts %v must be cloud only", m.Hosts)
	}
	if m.Channel == nil || m.Channel.Kind != "webshop" || m.Channel.AccountSuffix != ".demo-shop.example" {
		t.Fatalf("channel block not parsed: %+v", m.Channel)
	}
	if string(m.Channel.Capabilities["stock"]) != `"push"` || !m.Channel.StockLocations {
		t.Fatalf("capabilities not parsed: %+v", m.Channel.Capabilities)
	}
	if v := m.Channel.Webhooks.Verify; v.Header != "X-Demo-Hmac" || v.Encoding != "base64" {
		t.Fatalf("webhook verify not parsed: %+v", v)
	}
	if err := m.RefuseOnTill(); !errors.Is(err, ErrCloudHostedPlugin) {
		t.Fatalf("a till must refuse a cloud-hosted plugin, got %v", err)
	}
}

func TestChannelManifest_RoundTripIsStable(t *testing.T) {
	m, err := parseChannel(t, channelFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(m)
	var back Manifest
	if err := json.Unmarshal(a, &back); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(&back)
	if !bytes.Equal(a, b) {
		t.Fatalf("manifest bytes are not stable across a round trip (the signature covers them):\n%s\n%s", a, b)
	}
}

func TestManifest_HostsDefaultsToTill(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(`{"id":"t.p","name":"T","version":"1.0.0","runtime":"none"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !m.RunsOnTill() || m.IsCloudHosted() || m.RefuseOnTill() != nil {
		t.Fatalf("a manifest without hosts runs on a till: %+v", m.Hosts)
	}
	// A manifest without the new fields marshals without them.
	out, _ := json.Marshal(m)
	for _, f := range []string{`"hosts"`, `"channel"`, `"channel_contract"`} {
		if strings.Contains(string(out), f) {
			t.Fatalf("legacy manifest must not marshal %s (old signatures verify): %s", f, out)
		}
	}
}

func TestParseManifest_ChannelRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, m map[string]any)
		want string
	}{
		{"unknown capability", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.capabilities.teleport", "push") }, "unknown capability"},
		{"bad direction", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.capabilities.items", "sideways") }, "capabilities"},
		{"other net host", func(t *testing.T, m map[string]any) { setPath(t, m, "permissions", []string{"net:evil.example"}) }, "only permission"},
		{"extra permission", func(t *testing.T, m map[string]any) {
			setPath(t, m, "permissions", []string{"net:{account}", "storage"})
		}, "only permission"},
		{"db:own", func(t *testing.T, m map[string]any) { setPath(t, m, "permissions", []string{"db:own"}) }, "only permission"},
		{"secret:write", func(t *testing.T, m map[string]any) { setPath(t, m, "permissions", []string{"secret:write"}) }, "only permission"},
		{"net wildcard", func(t *testing.T, m map[string]any) { setPath(t, m, "permissions", []string{"net:*"}) }, "only permission"},
		{"no permission", func(t *testing.T, m map[string]any) { setPath(t, m, "permissions", nil) }, "only permission"},
		{"hosts till only", func(t *testing.T, m map[string]any) { setPath(t, m, "hosts", []string{"till"}) }, "hosts"},
		{"hosts both", func(t *testing.T, m map[string]any) { setPath(t, m, "hosts", []string{"till", "cloud"}) }, "hosts"},
		{"hosts absent", func(t *testing.T, m map[string]any) { setPath(t, m, "hosts", nil) }, "hosts"},
		{"hosts unknown", func(t *testing.T, m map[string]any) { setPath(t, m, "hosts", []string{"edge"}) }, "hosts"},
		{"hosts duplicate", func(t *testing.T, m map[string]any) { setPath(t, m, "hosts", []string{"cloud", "cloud"}) }, "hosts"},
		{"contract 2", func(t *testing.T, m map[string]any) { setPath(t, m, "channel_contract", 2) }, "channel_contract"},
		{"contract absent", func(t *testing.T, m map[string]any) { setPath(t, m, "channel_contract", nil) }, "channel_contract"},
		{"block absent", func(t *testing.T, m map[string]any) { setPath(t, m, "channel", nil) }, "channel block"},
		{"not wasm", func(t *testing.T, m map[string]any) { setPath(t, m, "runtime", "go") }, "runtime"},
		{"bad kind", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.kind", "bazaar") }, "channel.kind"},
		{"account unanchored", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account", `[a-z]+\.demo-shop\.example`) }, "anchored"},
		{"account invalid regex", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account", `^(unclosed$`) }, "valid pattern"},
		{"account empty", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account", "") }, "channel.account"},
		{"suffix no dot", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account_suffix", "demo-shop.example") }, "account_suffix"},
		{"suffix single label", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account_suffix", ".com") }, "account_suffix"},
		{"suffix upper", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account_suffix", ".Demo-Shop.example") }, "account_suffix"},
		{"suffix public co.uk", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.account_suffix", ".co.uk")
			setPath(t, m, "channel.account", `^[a-z0-9-]+\.co\.uk$`)
		}, "public suffix"},
		{"suffix public com.au", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.account_suffix", ".com.au")
			setPath(t, m, "channel.account", `^[a-z0-9-]+\.com\.au$`)
		}, "public suffix"},
		{"suffix is an address", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.account_suffix", ".10.0.0.1")
			setPath(t, m, "channel.account", `^[a-z0-9-]+\.10\.0\.0\.1$`)
		}, "number"},
		{"account match-all", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account", `^.*$`) }, "accepts"},
		{"account alternation escape", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account", `^a|.*$`) }, "accepts"},
		{"account allows a slash", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.account", `^[a-z0-9/.-]+\.demo-shop\.example$`)
		}, "accepts"},
		{"account allows userinfo", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.account", `^[a-z0-9@.-]+\.demo-shop\.example$`)
		}, "accepts"},
		{"carries entries", func(t *testing.T, m map[string]any) {
			setPath(t, m, "entries", []map[string]any{{"type": "page", "key": "k", "label": "L"}})
		}, "may not declare entries"},
		{"carries settings", func(t *testing.T, m map[string]any) {
			setPath(t, m, "settings", []map[string]any{{"key": "k", "type": "secret"}})
		}, "may not declare settings"},
		{"carries hooks", func(t *testing.T, m map[string]any) {
			setPath(t, m, "hooks", []map[string]any{{"event": "sale.completed", "action": "a"}})
		}, "may not declare hooks"},
		{"carries limits", func(t *testing.T, m map[string]any) { setPath(t, m, "limits", map[string]any{"memory_mb": 4096}) }, "may not declare limits"},
		{"carries db", func(t *testing.T, m map[string]any) { setPath(t, m, "db", map[string]any{"migrations": "migrations"}) }, "may not declare db"},
		{"carries views_used", func(t *testing.T, m map[string]any) { setPath(t, m, "views_used", []string{"sales.by_day.v1"}) }, "may not declare views_used"},
		{"carries wasm_abi", func(t *testing.T, m map[string]any) { setPath(t, m, "wasm_abi", 2) }, "may not declare wasm_abi"},
		{"carries top-level schedules", func(t *testing.T, m map[string]any) {
			setPath(t, m, "schedules", []map[string]any{{"event": "com.universaltill.channel-demo.t", "every_s": 60}})
		}, "may not declare schedules"},
		{"suffix absent", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.account_suffix", nil) }, "account_suffix"},
		{"auth absent", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth", nil) }, "channel.auth"},
		{"grant unknown", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth.grants", []string{"password"}) }, "grants"},
		{"token url other host", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.auth.token_url", "https://evil.example/oauth/token")
		}, "token_url"},
		{"token url http", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.auth.token_url", "http://{account}/oauth/token")
		}, "token_url"},
		{"token url userinfo", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.auth.token_url", "https://{account}@evil.example/x")
		}, "token_url"},
		{"authorize url other host", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.auth.authorize_url", "https://evil.example/a")
		}, "authorize_url"},
		{"authorize url missing for code", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth.authorize_url", nil) }, "authorize_url"},
		{"no scopes", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth.scopes", []string{}) }, "scopes"},
		{"inject header Host", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth.inject.header", "Host") }, "inject.header"},
		{"inject header newline", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth.inject.header", "X-A\r\nX-B") }, "inject.header"},
		{"inject absent", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.auth.inject", nil) }, "inject.header"},
		{"webhook alg", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.webhooks.verify.alg", "md5") }, "alg"},
		{"webhook encoding", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.webhooks.verify.encoding", "rot13") }, "encoding"},
		{"webhook topics without verify", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.webhooks.verify", nil) }, "verify"},
		{"webhook duplicate topic", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.webhooks.topics", []string{"a/b", "a/b"})
		}, "twice"},
		{"schedule too fast", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.schedules", []map[string]any{{"event": "reconcile", "every_s": 5}})
		}, "every_s"},
		{"schedule core namespace", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.schedules", []map[string]any{{"event": "sale.created", "every_s": 60}})
		}, "event"},
		{"negative limit", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.limits.max_variants", -1) }, "limits"},
		{"push_fields unknown", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel.push_fields", []string{"price", "colour"})
		}, "push_fields"},
		{"push_fields duplicate", func(t *testing.T, m map[string]any) { setPath(t, m, "channel.push_fields", []string{"price", "price"}) }, "twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := channelFixture(t)
			c.edit(t, m)
			_, err := parseChannel(t, m)
			if err == nil {
				t.Fatalf("manifest must be refused")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q should mention %q", err, c.want)
			}
		})
	}
}

func TestParseManifest_ChannelPushFields(t *testing.T) {
	all := []string{"name", "description", "sku", "barcodes", "price", "compare_at", "category_path"}
	for _, c := range []struct {
		name string
		set  any
		want []string
	}{
		{"absent", nil, nil},
		{"price only", []string{"price"}, []string{"price"}},
		{"every field", all, all},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := channelFixture(t)
			if c.set != nil {
				setPath(t, m, "channel.push_fields", c.set)
			}
			mf, err := parseChannel(t, m)
			if err != nil {
				t.Fatalf("manifest must be accepted: %v", err)
			}
			if !slices.Equal(mf.Channel.PushFields, c.want) {
				t.Fatalf("push_fields = %v, want %v", mf.Channel.PushFields, c.want)
			}
		})
	}
}

func TestParseManifest_ChannelFieldsOnlyOnChannelType(t *testing.T) {
	// A till plugin may not claim cloud hosting, the channel block or the
	// contract version.
	for _, c := range []struct {
		name string
		edit func(t *testing.T, m map[string]any)
	}{
		{"hosts cloud", func(t *testing.T, m map[string]any) { setPath(t, m, "hosts", []string{"cloud"}) }},
		{"channel block", func(t *testing.T, m map[string]any) {
			setPath(t, m, "channel", map[string]any{"kind": "webshop"})
		}},
		{"channel_contract", func(t *testing.T, m map[string]any) { setPath(t, m, "channel_contract", 1) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]any{"id": "t.p", "name": "T", "version": "1.0.0", "runtime": "none", "canonical_type": "report"}
			c.edit(t, m)
			if _, err := parseChannel(t, m); err == nil {
				t.Fatal("a non-channel plugin must not carry the channel fields")
			}
		})
	}
	// hosts ["till"] is the explicit default and stays valid.
	m := map[string]any{"id": "t.p", "name": "T", "version": "1.0.0", "runtime": "none", "canonical_type": "report", "hosts": []string{"till"}}
	if _, err := parseChannel(t, m); err != nil {
		t.Fatalf("hosts [till] is the default: %v", err)
	}
}

func TestNetAccountPermissionOnlyOnChannel(t *testing.T) {
	m := map[string]any{"id": "t.p", "name": "T", "version": "1.0.0", "runtime": "none",
		"canonical_type": "report", "permissions": []string{"net:{account}"}}
	if _, err := parseChannel(t, m); err == nil {
		t.Fatal("net:{account} is only a channel plugin's permission")
	}
}

func TestVerifyManifest_RefusesTheSameChannelShapes(t *testing.T) {
	// The marketplace install path never goes through ParseManifest, so
	// VerifyManifest must refuse the same shapes.
	m := channelFixture(t)
	setPath(t, m, "channel.capabilities.teleport", "push")
	raw, _ := json.Marshal(m)
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/manifest.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	mv := &ManifestVerifier{allowUnsigned: true}
	_, err := mv.VerifyManifest(dir + "/manifest.json")
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("VerifyManifest should refuse the unknown capability, got %v", err)
	}
	// ... and the untouched example is clean.
	good, _ := json.Marshal(channelFixture(t))
	if err := os.WriteFile(dir+"/manifest.json", good, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := mv.VerifyManifest(dir + "/manifest.json")
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("the contract example must verify cleanly: %v %v", err, res.Errors)
	}
}

func TestParseManifest_ChannelRealShopifyShapedSuffixIsAccepted(t *testing.T) {
	// A platform suffix (one platform, many shops) is the reviewed case.
	m := channelFixture(t)
	setPath(t, m, "channel.account_suffix", ".myshopify.com")
	setPath(t, m, "channel.account", `^[a-z0-9][a-z0-9-]*\.myshopify\.com$`)
	if _, err := parseChannel(t, m); err != nil {
		t.Fatalf("a private platform suffix must be accepted: %v", err)
	}
}

// ut-docs#4907: item_create is an optional capability; only push is read.
func TestParseManifest_ChannelItemCreateCapability(t *testing.T) {
	for _, dir := range []string{"push", "pull", "both"} {
		t.Run(dir, func(t *testing.T) {
			m := channelFixture(t)
			setPath(t, m, "channel.capabilities.item_create", dir)
			mf, err := parseChannel(t, m)
			if err != nil {
				t.Fatalf("item_create %q must be accepted: %v", dir, err)
			}
			if got := string(mf.Channel.Capabilities["item_create"]); got != `"`+dir+`"` {
				t.Fatalf("item_create = %q, want %q", got, dir)
			}
		})
	}
}

// ADR-0215 / ut-docs#6989: a delivery channel manifest (fixed hosts, store_ref,
// non-string capabilities, inject scheme, webhook route and canonical form) is
// signed by ut-cloud over its own canonical bytes. A till that re-marshals it
// through Manifest must keep every one of those fields, or the signature no
// longer verifies. All data is invented.
const deliveryChannelManifest = `{
  "id": "com.universaltill.channel-demo-delivery",
  "name": "Demo delivery channel",
  "version": "0.1.0",
  "canonical_type": "channel",
  "runtime": "wasm",
  "entrypoint": "./bin/plugin.wasm",
  "device_arch": "any",
  "hosts": ["cloud"],
  "channel_contract": 1,
  "permissions": ["net:{account}"],
  "channel": {
    "kind": "delivery",
    "account_model": "fixed",
    "hosts": {"live": {"api": "api.demo-delivery.example", "auth": "auth.demo-delivery.example"},
              "sandbox": {"api": "api-sandbox.demo-delivery.example", "auth": "auth-sandbox.demo-delivery.example"}},
    "store_ref": "^[A-Za-z0-9_-]{1,64}$",
    "auth": {"grants": ["client_credentials"], "token_url": "https://{auth}/oauth2/token",
             "scopes": ["orders"],
             "inject": {"header": "Authorization", "scheme": "Bearer"}},
    "capabilities": {"orders": "pull", "modifiers": true, "accept_window_s": 600, "prep": "order"},
    "webhooks": {"route": "app", "topics": ["order.created"],
                 "verify": {"header": "X-Sig", "alg": "hmac-sha256", "encoding": "hex",
                            "canonical": "{sequence_header} + \" \" + body", "sequence_header": "X-Seq"}},
    "schedules": [{"event": "reconcile", "every_s": 60, "jitter_s": 15}]
  }
}`

// jsonSubset reports the first path at which want has a value that got lacks
// or changes (extra keys in got are allowed, e.g. empty required strings).
func jsonSubset(want, got any, path string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				return path + "." + k
			}
			if p := jsonSubset(wv, gv, path+"."+k); p != "" {
				return p
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return path
		}
		for i := range w {
			if p := jsonSubset(w[i], g[i], path+"[]"); p != "" {
				return p
			}
		}
	default:
		if want != got {
			return path
		}
	}
	return ""
}

func TestDeliveryChannelManifest_RoundTripsWithoutDroppingFields(t *testing.T) {
	var want any
	if err := json.Unmarshal([]byte(deliveryChannelManifest), &want); err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(deliveryChannelManifest), &m); err != nil {
		t.Fatal(err)
	}
	a, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(a, &got); err != nil {
		t.Fatal(err)
	}
	if p := jsonSubset(want, got, "$"); p != "" {
		t.Fatalf("re-marshal dropped or changed %s (the signature covers these bytes):\n%s", p, a)
	}
	// Second pass is byte-identical to the first.
	var back Manifest
	if err := json.Unmarshal(a, &back); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(&back)
	if !bytes.Equal(a, b) {
		t.Fatalf("delivery manifest bytes are not stable:\n%s\n%s", a, b)
	}
	// Field order in the signed bytes matches ut-cloud's CanonicalChannel.
	order := []string{`"kind"`, `"account"`, `"account_suffix"`, `"account_model"`, `"hosts":{"live"`, `"store_ref"`, `"auth":{"grants"`, `"capabilities"`, `"webhooks"`, `"schedules"`}
	last := -1
	for _, f := range order {
		i := strings.Index(string(a)[strings.Index(string(a), `"channel":`):], f)
		if i < 0 || i < last {
			t.Fatalf("channel field %s missing or out of order (%d after %d):\n%s", f, i, last, a)
		}
		last = i
	}
	for _, f := range []string{`"scheme":"Bearer"`, `"route":"app"`, `"canonical":`, `"sequence_header":"X-Seq"`, `"modifiers":true`, `"accept_window_s":600`} {
		if !strings.Contains(string(a), f) {
			t.Fatalf("missing %s in %s", f, a)
		}
	}
}

func TestParseManifest_DeliveryChannelIsAccepted(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(deliveryChannelManifest))
	if err != nil {
		t.Fatalf("a delivery channel manifest must parse: %v", err)
	}
	if m.Channel.Kind != "delivery" || m.Channel.AccountModel != "fixed" || m.Channel.Hosts["live"].API != "api.demo-delivery.example" {
		t.Fatalf("delivery block not parsed: %+v", m.Channel)
	}
	if err := m.RefuseOnTill(); !errors.Is(err, ErrCloudHostedPlugin) {
		t.Fatalf("a till must still refuse a cloud-hosted plugin, got %v", err)
	}
}
