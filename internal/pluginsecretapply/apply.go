// Package pluginsecretapply applies the cloud's set_plugin_secret directive
// on the main till (ADR-0213 Decision 5, ut-docs#6960): it opens the HPKE
// blob with the till's directive key and writes one allow-listed plugin
// secret setting (plus its non-secret companion and a version counter).
//
// It lives OUTSIDE internal/cloudsync on purpose: cloudsync never touches
// plugin settings (ADR-0082; TestCloudSyncNeverTouchesPluginSettings). It is
// reached only through the hook the till wiring injects into cloudsync.
//
// Every refusal is a short reason code (the error text is the code); the
// value, its hash and its length never reach a log line, an error or a
// result. A DB or manifest failure is ErrRetryable: the directive stays
// pending and the cloud re-serves it at the next check-in.
package pluginsecretapply

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/logging"
)

// Info is the HPKE info string of the plugin-secret seal (ut-docs
// reference/contracts/plugin-secret-v1.json).
const Info = "universaltill/plugin-secret/v1"

// DirectiveType is the cloud directive type this package applies.
const DirectiveType = "set_plugin_secret"

// Reason codes (the whole result message on a failure).
const (
	ReasonNoKeyPair        = "no_till_key"
	ReasonKIDMismatch      = "kid_mismatch"
	ReasonPluginNotInstall = "plugin_not_installed"
	ReasonSettingNotSecret = "setting_not_secret"
	ReasonNotAllowed       = "not_allowed"
	ReasonOpenFailed       = "open_failed"
	ReasonStaleVersion     = "stale_version"
	ReasonWriteFailed      = "write_failed"
	maxValueBytes          = 1024
	maxAuxBytes            = 128
	appliedMessage         = "applied"
)

// ErrRetryable marks a failure that leaves the directive pending.
var ErrRetryable = errors.New("retryable")

type reasonError struct {
	code      string
	retryable bool
}

func (e *reasonError) Error() string { return e.code }
func (e *reasonError) Is(target error) bool {
	return target == ErrRetryable && e.retryable
}

func fail(code string) error  { return &reasonError{code: code} }
func retry(code string) error { return &reasonError{code: code, retryable: true} }

// target is one allow-listed plugin: the secret key, its plain companions
// and the plain setting holding the last applied version. Compile-time
// table, never configuration (ADR-0213 Decision 5).
type target struct {
	secretKey string
	plainKeys []string
	verKey    string
}

var allowed = map[string]target{
	"sumup": { // core-neutral:allow ADR-0213 sealed key delivery, allow-list of one pair
		secretKey: "sumup_api_key",                 // core-neutral:allow ADR-0213
		plainKeys: []string{"sumup_merchant_code"}, // core-neutral:allow ADR-0213
		verKey:    "sumup_key_version",             // core-neutral:allow ADR-0213
	},
}

// IsAllowed reports whether (plugin, key) is the allow-listed secret pair.
func IsAllowed(pluginID, settingKey string) bool {
	t, ok := allowed[pluginID]
	return ok && t.secretKey == settingKey
}

// SecretKeyFor returns the allow-listed secret setting of a plugin: the
// plugin's settings page offers the one-time-code entry only for these.
func SecretKeyFor(pluginID string) (string, bool) {
	t, ok := allowed[pluginID]
	return t.secretKey, ok
}

// Directive is the decoded set_plugin_secret directive.
type Directive struct {
	DirectiveID  string
	Type         string
	PluginID     string
	SettingKey   string
	Ver          int64
	SecretSealed string
}

// Write is one setting write.
type Write struct {
	Key, Scope, ValueJSON string
	DeclaredSecret        bool
}

// Settings is the plugin-settings seam (production: data.PluginRepo).
type Settings interface {
	Get(ctx context.Context, pluginID, key string) (valueJSON string, found bool, err error)
	// Save writes every item in one transaction, sealing declared secrets.
	Save(ctx context.Context, pluginID string, writes []Write) error
}

// Applier applies set_plugin_secret directives.
type Applier struct {
	Keys     *directivekey.Store
	Settings Settings
	// StoreID is the store's external id (the AAD's first field).
	StoreID func() string
	// DeclaresSecret reports whether the plugin is installed and its
	// manifest declares key as type "secret". A non-nil error means the
	// manifest could not be read (retryable).
	DeclaresSecret func(ctx context.Context, pluginID, key string) (declared, installed bool, err error)
}

type plaintext struct {
	V     int               `json:"v"`
	Ver   int64             `json:"ver"`
	Value string            `json:"value"`
	Aux   map[string]string `json:"aux"`
}

func (p plaintext) clear() bool { return p.Value == "" }

func printable(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func parsePlaintext(raw []byte) (plaintext, error) {
	var p plaintext
	if err := json.Unmarshal(raw, &p); err != nil || p.V != 1 {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	if !printable(p.Value, maxValueBytes) {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	for k, v := range p.Aux {
		if !printable(v, maxAuxBytes) {
			return plaintext{}, fail(ReasonOpenFailed)
		}
		ok := false
		for _, a := range allowed {
			for _, pk := range a.plainKeys {
				ok = ok || pk == k
			}
		}
		if !ok {
			return plaintext{}, fail(ReasonNotAllowed)
		}
	}
	return p, nil
}

// Apply applies one directive. It returns the result message on success
// and an error whose text is a reason code on failure; ErrRetryable errors
// must leave the directive pending.
func (a *Applier) Apply(ctx context.Context, d Directive) (string, error) {
	msg, err := a.apply(ctx, d)
	if err != nil {
		var re *reasonError
		code := "failed"
		if errors.As(err, &re) {
			code = re.code
		}
		// The reason code only: never the directive's value or blob.
		logging.L().Warnf("plugin secret directive %s refused: %s", d.DirectiveID, code)
	}
	return msg, err
}

func (a *Applier) apply(ctx context.Context, d Directive) (string, error) {
	tgt, ok := allowed[d.PluginID]
	if d.Type != DirectiveType || !ok || tgt.secretKey != d.SettingKey {
		return "", fail(ReasonNotAllowed)
	}
	declared, installed, err := a.DeclaresSecret(ctx, d.PluginID, d.SettingKey)
	if err != nil {
		return "", retry(ReasonWriteFailed)
	}
	if !installed {
		return "", fail(ReasonPluginNotInstall)
	}
	if !declared {
		return "", fail(ReasonSettingNotSecret)
	}
	kid, haveKey := a.Keys.CurrentKID()
	if !haveKey {
		return "", fail(ReasonNoKeyPair)
	}
	parts := strings.Split(d.SecretSealed, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", fail(ReasonOpenFailed)
	}
	if parts[1] != kid {
		return "", fail(ReasonKIDMismatch)
	}
	aad := strings.Join([]string{a.StoreID(), d.DirectiveID, d.Type, d.PluginID, d.SettingKey}, "\n")
	raw, err := a.Keys.OpenSealed(d.SecretSealed, Info, aad)
	if err != nil {
		return "", fail(ReasonOpenFailed)
	}
	pt, err := parsePlaintext(raw)
	if err != nil {
		return "", err
	}
	if pt.Ver != d.Ver || pt.Ver <= 0 {
		return "", fail(ReasonOpenFailed)
	}
	stored, found, err := a.Settings.Get(ctx, d.PluginID, tgt.verKey)
	if err != nil {
		return "", retry(ReasonWriteFailed)
	}
	if found {
		if cur, perr := strconv.ParseInt(strings.TrimSpace(stored), 10, 64); perr == nil && pt.Ver <= cur {
			return "", fail(ReasonStaleVersion)
		}
	}
	valJSON, _ := json.Marshal(pt.Value)
	writes := []Write{{Key: tgt.secretKey, Scope: "global", ValueJSON: string(valJSON), DeclaredSecret: true}}
	for _, pk := range tgt.plainKeys {
		v := pt.Aux[pk] // absent aux code on a clear (or set) writes blank
		b, _ := json.Marshal(v)
		writes = append(writes, Write{Key: pk, Scope: "global", ValueJSON: string(b)})
	}
	writes = append(writes, Write{Key: tgt.verKey, Scope: "global", ValueJSON: strconv.FormatInt(pt.Ver, 10)})
	if err := a.Settings.Save(ctx, d.PluginID, writes); err != nil {
		return "", retry(ReasonWriteFailed)
	}
	return appliedMessage, nil
}
