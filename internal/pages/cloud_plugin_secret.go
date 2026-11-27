package pages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginsecretapply"
)

// The main till's apply of the cloud's set_plugin_secret directive
// (ADR-0213 Decision 5, ut-docs#6960). cloudsync only decodes the envelope;
// everything about the sealed value lives in internal/pluginsecretapply,
// reached through this injected hook. The value is never logged and the
// result message is a bare reason code.

// pluginSecretSettings adapts the plugin repository to the apply seam.
type pluginSecretSettings struct {
	d *common.Deps
}

func (s pluginSecretSettings) repo() *data.PluginRepo {
	return data.NewPluginRepo(s.d.Db).OnSettingsChanged(func() { plugins.SharedBus(s.d.Db).BumpGeneration() })
}

// LocalVer is the version of the global row as the conflict rule sees it.
func (s pluginSecretSettings) LocalVer(ctx context.Context, pluginID, key string) (int64, error) {
	ver, _, _, err := s.repo().PluginSettingVer(ctx, pluginID, key)
	return ver, err
}

// Save writes the batch in one transaction, every row stored with the
// cloud's ver and origin cloud.
func (s pluginSecretSettings) Save(ctx context.Context, pluginID string, writes []pluginsecretapply.Write, ver int64) error {
	meta := &data.PluginSettingMeta{Ver: ver, Origin: data.PluginSettingOriginCloud}
	batch := make([]data.PluginSettingWrite, len(writes))
	for i, w := range writes {
		batch[i] = data.PluginSettingWrite{Key: w.Key, Scope: w.Scope, ValueJSON: w.ValueJSON, DeclaredSecret: w.DeclaredSecret, Meta: meta}
	}
	_, err := s.repo().SavePluginSettingsBatch(ctx, pluginID, batch)
	return err
}

// SaveChecked is Save with the version rule re-run inside the write
// transaction (pluginsecretapply.CheckedSaver): a till edit between the
// apply's check and the write refuses the directive instead of being
// overwritten.
func (s pluginSecretSettings) SaveChecked(ctx context.Context, pluginID string, writes []pluginsecretapply.Write, ver int64,
	check func(localVer func(key string) (int64, error)) error) error {
	meta := &data.PluginSettingMeta{Ver: ver, Origin: data.PluginSettingOriginCloud}
	batch := make([]data.PluginSettingWrite, len(writes))
	for i, w := range writes {
		batch[i] = data.PluginSettingWrite{Key: w.Key, Scope: w.Scope, ValueJSON: w.ValueJSON, DeclaredSecret: w.DeclaredSecret, Meta: meta}
	}
	_, err := s.repo().SavePluginSettingsBatchChecked(ctx, pluginID, batch, check)
	return err
}

// verifiedManifestSeam is the production manifest seam (ADR-0214 D3): the
// installed manifest, re-verified against the marketplace key at apply time
// (plugins.VerifiedManifestSettings). The key is the effective config's
// (a release binary's compiled key wins, ADR-0158); no network is touched.
func verifiedManifestSeam(d *common.Deps) pluginsecretapply.Manifest {
	return pluginsecretapply.ManifestFunc(func(ctx context.Context, pluginID string) (pluginsecretapply.Declared, error) {
		mv, err := plugins.NewManifestVerifier(enroll.Effective(d.Cfg).Marketplace.PublicKey)
		if err != nil {
			return pluginsecretapply.Declared{}, fmt.Errorf("%w: %v", plugins.ErrManifestUnverified, err)
		}
		man, installed, err := plugins.VerifiedManifestSettings(ctx, d.Db, mv, pluginID)
		if err != nil || !installed {
			return pluginsecretapply.Declared{}, err
		}
		return pluginsecretapply.DeclaredFrom(man), nil
	})
}

func newPluginSecretApplier(d *common.Deps, keys *directivekey.Store) *pluginsecretapply.Applier {
	return &pluginsecretapply.Applier{
		Keys:     keys,
		Settings: pluginSecretSettings{d: d},
		Manifest: verifiedManifestSeam(d),
		StoreID:  func() string { return enroll.Effective(d.Cfg).Marketplace.StoreID },
	}
}

func cloudSetPluginSecret(ctx context.Context, d *common.Deps, keys *directivekey.Store, in cloudsync.PluginSecretDirective) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", errors.New(pluginsecretapply.ReasonNotAllowed)
	}
	msg, err := newPluginSecretApplier(d, keys).Apply(ctx, pluginsecretapply.Directive{
		DirectiveID: in.DirectiveID, Type: in.Type, PluginID: in.PluginID,
		SettingKey: in.SettingKey, Ver: in.Ver, SecretSealed: in.SecretSealed,
	})
	if errors.Is(err, pluginsecretapply.ErrRetryable) {
		return "", cloudsync.ErrPluginSecretRetry
	}
	if err == nil {
		// Who changed what, never the value: plugin and key only.
		_ = data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, "system", "plugin", in.PluginID, "plugin_setting_cloud_applied",
			map[string]any{"via": "cloud", "directive_id": in.DirectiveID, "key": in.SettingKey, "ver": in.Ver},
			time.Now().UTC().Format(time.RFC3339), "")
	}
	return msg, err
}
