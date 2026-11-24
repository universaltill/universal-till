package cloudsync

import (
	"errors"
	"math"
)

// set_plugin_secret (ADR-0213 Decision 5, ut-docs#6960): the cloud delivers
// one sealed plugin credential to the main till. This package only decodes
// the envelope and carries the opaque secret_sealed string to the injected
// Hooks.SetPluginSecret; it never opens the seal, names a setting or touches
// plugin storage (the boundary test next to this file). The apply lives
// in internal/pluginsecretapply, wired from internal/pages.

// PluginSecretDirective is a decoded set_plugin_secret directive.
// DirectiveID and Type are part of the seal's AAD.
type PluginSecretDirective struct {
	DirectiveID  string
	Type         string
	PluginID     string
	SettingKey   string
	Ver          int64
	SecretSealed string
}

// ErrPluginSecretRetry is what the hook returns for a failure that is the
// till's state, not the payload's (db busy, manifest unreadable): the
// directive stays pending and the cloud re-serves it at the next check-in.
var ErrPluginSecretRetry = errors.New("write_failed")

// decodePluginSecret reads the payload. A non-empty msg is the failure
// reason code.
func decodePluginSecret(d directive) (PluginSecretDirective, string) {
	p := payload(d.Payload)
	out := PluginSecretDirective{DirectiveID: d.ID, Type: d.Type}
	if id, ok := p.optStr("plugin_id"); ok && id != nil {
		out.PluginID = *id
	}
	if k, ok := p.optStr("setting_key"); ok && k != nil {
		out.SettingKey = *k
	}
	if out.PluginID == "" || out.SettingKey == "" {
		return out, "not_allowed"
	}
	ver, ok := p.optInt("ver")
	if !ok || ver == nil || *ver <= 0 || *ver > math.MaxInt32 {
		return out, "open_failed"
	}
	out.Ver = *ver
	if s, ok := p["secret_sealed"].(string); ok {
		out.SecretSealed = s
	}
	if out.SecretSealed == "" {
		return out, "open_failed"
	}
	return out, ""
}
