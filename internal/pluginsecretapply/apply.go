// Package pluginsecretapply applies the cloud's set_plugin_secret directive
// on the main till (ADR-0213 Decision 5, ADR-0214 Decision 3 and its
// amendment, ut-docs#6960): it opens the HPKE blob with the till's directive
// key and writes ONE setting the plugin's installed, signature-verified
// manifest declares (plus declared non-secret companions) with a per-field
// version. The wire is v2 (kind plain|secret, op set|clear, ver, base_ver,
// aux, restore); the v1 plaintext still applies as kind secret.
//
// What may be written is decided by the manifest, never by this package: the
// compiled SumUp allow-list is gone. The seam is Manifest (production:
// plugins.VerifiedManifestSettings, wired in internal/pages); a failure to
// read it is retryable, a manifest that does not verify is refused.
//
// It lives OUTSIDE internal/cloudsync on purpose: cloudsync never touches
// plugin settings (ADR-0082; TestCloudSyncNeverTouchesPluginSettings). It is
// reached only through the hook the till wiring injects into cloudsync.
//
// Every refusal is a short reason code (the error text is the code); the
// value, its hash and its length never reach a log line, an error or a
// result. A DB or manifest-read failure is ErrRetryable: the directive stays
// pending and the cloud re-serves it at the next check-in.
package pluginsecretapply

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Info is the HPKE info string of the plugin-secret seal (ut-docs
// reference/contracts/plugin-secret-v1.json).
const Info = "universaltill/plugin-secret/v1"

// DirectiveType is the cloud directive type this package applies.
const DirectiveType = "set_plugin_secret"

// Reason codes (the whole result message on a failure).
const (
	ReasonNoKeyPair           = "no_till_key"
	ReasonKIDMismatch         = "kid_mismatch"
	ReasonPluginNotInstall    = "plugin_not_installed"
	ReasonSettingNotDeclared  = "setting_not_declared"
	ReasonSettingKindMismatch = "setting_kind_mismatch"
	ReasonInvalidValue        = "invalid_value"
	ReasonNotAllowed          = "not_allowed"
	ReasonOpenFailed          = "open_failed"
	ReasonConflict            = "conflict"
	ReasonStaleVersion        = "stale_version"
	ReasonWriteFailed         = "write_failed"
	maxValueBytes             = 1024
	maxAuxBytes               = 1024
	maxAux                    = 8
	maxSafeInt                = 1 << 53
	appliedMessage            = "applied"
	originCloud               = "cloud"
)

// Plaintext kinds.
const (
	KindPlain  = "plain"
	KindSecret = "secret"
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

// Decl is one setting as the installed manifest declares it (the subset the
// apply validates a value against).
type Decl struct {
	Type       string
	Scope      string
	Options    []string
	OptionsAsk bool
	MaxLen     int
	Min, Max   *int64
	Required   bool
}

// Declared is the installed, verified manifest's settings of one plugin.
type Declared struct {
	Installed bool
	Settings  map[string]Decl
}

// Manifest is the manifest seam. A non-nil error means the manifest could
// not be read (retryable) unless it wraps plugins.ErrManifestUnverified
// (refused, terminal).
type Manifest interface {
	Declared(ctx context.Context, pluginID string) (Declared, error)
}

// ManifestFunc adapts a function to Manifest.
type ManifestFunc func(ctx context.Context, pluginID string) (Declared, error)

// Declared implements Manifest.
func (f ManifestFunc) Declared(ctx context.Context, pluginID string) (Declared, error) {
	return f(ctx, pluginID)
}

// DeclaredFrom converts a parsed manifest.
func DeclaredFrom(m *plugins.Manifest) Declared {
	out := Declared{Installed: m != nil, Settings: map[string]Decl{}}
	if m == nil {
		return out
	}
	for _, s := range m.Settings {
		d := Decl{Type: s.Type, Scope: s.Scope, OptionsAsk: s.OptionsAsk, MaxLen: s.MaxLen, Min: s.Min, Max: s.Max, Required: s.Required}
		if d.Scope == "" {
			d.Scope = "global"
		}
		for _, o := range s.Options {
			d.Options = append(d.Options, o.Value)
		}
		out.Settings[s.Key] = d
	}
	return out
}

// SecretKeysFor returns the plugin's declared global `type: "secret"`
// settings, sorted: the settings page offers the one-time-code entry only
// when the manifest declares one. An unreadable manifest declares none.
func SecretKeysFor(ctx context.Context, m Manifest, pluginID string) []string {
	if m == nil {
		return nil
	}
	d, err := m.Declared(ctx, pluginID)
	if err != nil || !d.Installed {
		return nil
	}
	var keys []string
	for k, s := range d.Settings {
		if s.Type == plugins.SettingTypeSecret && s.Scope == "global" && !reserved(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// reserved: names the host owns (version/origin bookkeeping, internal).
func reserved(key string) bool {
	return key == "" || key == "ver" || key == "origin" || key == "at" ||
		strings.HasPrefix(key, "_") || strings.HasSuffix(key, "_key_version")
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
	// LocalVer is the version of the global row of key as the conflict rule
	// sees it: 0 when there is no row or only an unedited manifest default.
	LocalVer(ctx context.Context, pluginID, key string) (int64, error)
	// Save writes every item in ONE transaction, sealing declared secrets,
	// each stored with ver and origin cloud.
	Save(ctx context.Context, pluginID string, writes []Write, ver int64) error
}

// CheckedSaver is the optional atomic form of Settings.Save: check runs inside
// the write transaction, reading each key's version through localVer, and a
// non-nil result aborts the write. Production (data.PluginRepo) implements it,
// closing the window between the version check and the write.
type CheckedSaver interface {
	SaveChecked(ctx context.Context, pluginID string, writes []Write, ver int64,
		check func(localVer func(key string) (int64, error)) error) error
}

// Applier applies set_plugin_secret directives.
type Applier struct {
	Keys     *directivekey.Store
	Settings Settings
	Manifest Manifest
	// StoreID is the store's external id (the AAD's first field).
	StoreID func() string
}

type plaintext struct {
	V       int               `json:"v"`
	Kind    string            `json:"kind"`
	Op      string            `json:"op"`
	Ver     int64             `json:"ver"`
	BaseVer *int64            `json:"base_ver"`
	Value   string            `json:"value"`
	Aux     map[string]string `json:"aux"`
	Restore bool              `json:"restore"`
}

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
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	var p plaintext
	switch head.V {
	case 1: // legacy: a secret delivery, no base_ver
		var v1 struct {
			V     int               `json:"v"`
			Ver   int64             `json:"ver"`
			Value string            `json:"value"`
			Aux   map[string]string `json:"aux"`
		}
		if err := json.Unmarshal(raw, &v1); err != nil {
			return plaintext{}, fail(ReasonOpenFailed)
		}
		p = plaintext{V: 1, Kind: KindSecret, Op: "set", Ver: v1.Ver, Value: v1.Value, Aux: v1.Aux}
	case 2:
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil || dec.More() {
			return plaintext{}, fail(ReasonOpenFailed)
		}
		if p.BaseVer == nil || *p.BaseVer < 0 {
			return plaintext{}, fail(ReasonOpenFailed)
		}
		if p.Kind != KindPlain && p.Kind != KindSecret {
			return plaintext{}, fail(ReasonNotAllowed)
		}
		if p.Op != "set" && p.Op != "clear" {
			return plaintext{}, fail(ReasonOpenFailed)
		}
		if p.Op == "clear" && p.Value != "" {
			return plaintext{}, fail(ReasonOpenFailed)
		}
	default:
		return plaintext{}, fail(ReasonOpenFailed)
	}
	if !printable(p.Value, maxValueBytes) || len(p.Aux) > maxAux {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	for _, v := range p.Aux {
		if !printable(v, maxAuxBytes) {
			return plaintext{}, fail(ReasonOpenFailed)
		}
	}
	return p, nil
}

// encode validates value against decl and returns the stored value_json
// shape (text/secret/endpoint/choice: a JSON string; bool: a literal;
// number/percent/money: an integer literal). list and map are never remote
// writable.
func encode(decl Decl, value string, clear bool) (string, error) {
	str := func() (string, error) {
		b, _ := json.Marshal(value)
		return string(b), nil
	}
	if clear || value == "" {
		if decl.Required {
			return "", fail(ReasonInvalidValue)
		}
		switch decl.Type {
		case plugins.SettingTypeBool, plugins.SettingTypeNumber, plugins.SettingTypePercent, plugins.SettingTypeMoney:
			return "", fail(ReasonInvalidValue) // no blank for a typed scalar
		}
		return `""`, nil
	}
	switch decl.Type {
	case "", plugins.SettingTypeText:
		if decl.MaxLen > 0 && utf8.RuneCountInString(value) > decl.MaxLen {
			return "", fail(ReasonInvalidValue)
		}
		return str()
	case plugins.SettingTypeSecret:
		return str()
	case plugins.SettingTypeEndpoint:
		if !plugins.ValidEndpointURL(value) {
			return "", fail(ReasonInvalidValue)
		}
		return str()
	case plugins.SettingTypeChoice:
		for _, o := range decl.Options {
			if o == value {
				return str()
			}
		}
		if decl.OptionsAsk && plugins.AskedChoiceValueOK(value) {
			return str()
		}
		return "", fail(ReasonInvalidValue)
	case plugins.SettingTypeBool:
		if value != "true" && value != "false" {
			return "", fail(ReasonInvalidValue)
		}
		return value, nil
	case plugins.SettingTypeNumber, plugins.SettingTypePercent, plugins.SettingTypeMoney:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != value || n > maxSafeInt || n < -maxSafeInt {
			return "", fail(ReasonInvalidValue)
		}
		if decl.Type == plugins.SettingTypePercent && decl.Min == nil && decl.Max == nil && (n < 0 || n > 10000) {
			return "", fail(ReasonInvalidValue) // basis points
		}
		if (decl.Min != nil && n < *decl.Min) || (decl.Max != nil && n > *decl.Max) {
			return "", fail(ReasonInvalidValue)
		}
		return value, nil
	default: // list, map and anything future: not remote writable
		return "", fail(ReasonNotAllowed)
	}
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

func (a *Applier) open(d Directive) (plaintext, error) {
	kid, haveKey := a.Keys.CurrentKID()
	if !haveKey {
		return plaintext{}, fail(ReasonNoKeyPair)
	}
	parts := strings.Split(d.SecretSealed, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	if parts[1] != kid {
		return plaintext{}, fail(ReasonKIDMismatch)
	}
	aad := strings.Join([]string{a.StoreID(), d.DirectiveID, d.Type, d.PluginID, d.SettingKey}, "\n")
	raw, err := a.Keys.OpenSealed(d.SecretSealed, Info, aad)
	if err != nil {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	pt, err := parsePlaintext(raw)
	if err != nil {
		return plaintext{}, err
	}
	if pt.Ver != d.Ver || pt.Ver <= 0 {
		return plaintext{}, fail(ReasonOpenFailed)
	}
	return pt, nil
}

func (a *Applier) apply(ctx context.Context, d Directive) (string, error) {
	if d.Type != DirectiveType || d.PluginID == "" || reserved(d.SettingKey) {
		return "", fail(ReasonNotAllowed)
	}
	pt, err := a.open(d)
	if err != nil {
		return "", err
	}

	decls, err := a.Manifest.Declared(ctx, d.PluginID)
	if err != nil {
		if errors.Is(err, plugins.ErrManifestUnverified) {
			return "", fail(ReasonNotAllowed)
		}
		return "", retry(ReasonWriteFailed)
	}
	if !decls.Installed {
		if pt.Restore {
			return "", retry(ReasonPluginNotInstall) // deferred until the restore installs it
		}
		return "", fail(ReasonPluginNotInstall)
	}
	decl, ok := decls.Settings[d.SettingKey]
	if !ok {
		return "", fail(ReasonSettingNotDeclared)
	}
	if decl.Scope != "global" {
		return "", fail(ReasonNotAllowed)
	}
	isSecret := decl.Type == plugins.SettingTypeSecret
	if isSecret != (pt.Kind == KindSecret) {
		return "", fail(ReasonSettingKindMismatch)
	}
	valJSON, err := encode(decl, pt.Value, pt.Op == "clear")
	if err != nil {
		return "", err
	}

	// aux: other declared, non-secret, global settings of this plugin,
	// validated like any plain value and written in the same transaction.
	auxKeys := make([]string, 0, len(pt.Aux))
	for k := range pt.Aux {
		auxKeys = append(auxKeys, k)
	}
	sort.Strings(auxKeys)
	writes := []Write{{Key: d.SettingKey, Scope: "global", ValueJSON: valJSON, DeclaredSecret: isSecret}}
	for _, k := range auxKeys {
		ad, ok := decls.Settings[k]
		if k == d.SettingKey || reserved(k) || !ok || ad.Scope != "global" || ad.Type == plugins.SettingTypeSecret {
			return "", fail(ReasonNotAllowed)
		}
		av, err := encode(ad, pt.Aux[k], false)
		if err != nil {
			return "", err
		}
		writes = append(writes, Write{Key: k, Scope: "global", ValueJSON: av})
	}

	// Version rule (design B 4.3): a base_ver older than the till's own
	// version means the till changed the value after the cloud looked, so
	// nothing is written; else the new ver must be newer than the till's.
	// Checked first for the cheap refusal, and again inside the write
	// transaction when the store supports it (CheckedSaver), so a till edit
	// between the check and the write is never overwritten.
	check := func(localVer func(key string) (int64, error)) error {
		local, err := localVer(d.SettingKey)
		if err != nil {
			return retry(ReasonWriteFailed)
		}
		if pt.BaseVer != nil && *pt.BaseVer < local {
			return fail(ReasonConflict)
		}
		if pt.Ver <= local {
			return fail(ReasonStaleVersion)
		}
		for _, k := range auxKeys {
			lv, err := localVer(k)
			if err != nil {
				return retry(ReasonWriteFailed)
			}
			if pt.BaseVer != nil && *pt.BaseVer < lv {
				return fail(ReasonConflict) // never overwrite a companion edited on the till
			}
		}
		return nil
	}
	if err := check(func(k string) (int64, error) { return a.Settings.LocalVer(ctx, d.PluginID, k) }); err != nil {
		return "", err
	}
	if cs, ok := a.Settings.(CheckedSaver); ok {
		err := cs.SaveChecked(ctx, d.PluginID, writes, pt.Ver, check)
		var re *reasonError
		if errors.As(err, &re) {
			return "", err // the in-transaction check refused: a reason code
		}
		if err != nil {
			return "", retry(ReasonWriteFailed)
		}
		return appliedMessage, nil
	}
	if err := a.Settings.Save(ctx, d.PluginID, writes, pt.Ver); err != nil {
		return "", retry(ReasonWriteFailed)
	}
	return appliedMessage, nil
}
