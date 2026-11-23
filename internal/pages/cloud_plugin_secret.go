package pages

import (
	"context"
	"errors"

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

func (s pluginSecretSettings) Get(ctx context.Context, pluginID, key string) (string, bool, error) {
	return s.repo().GetPluginSetting(ctx, pluginID, key)
}

func (s pluginSecretSettings) Save(ctx context.Context, pluginID string, writes []pluginsecretapply.Write) error {
	batch := make([]data.PluginSettingWrite, len(writes))
	for i, w := range writes {
		batch[i] = data.PluginSettingWrite{Key: w.Key, Scope: w.Scope, ValueJSON: w.ValueJSON, DeclaredSecret: w.DeclaredSecret}
	}
	_, err := s.repo().SavePluginSettingsBatch(ctx, pluginID, batch)
	return err
}

func newPluginSecretApplier(d *common.Deps, keys *directivekey.Store) *pluginsecretapply.Applier {
	return &pluginsecretapply.Applier{
		Keys:     keys,
		Settings: pluginSecretSettings{d: d},
		StoreID:  func() string { return enroll.Effective(d.Cfg).Marketplace.StoreID },
		DeclaresSecret: func(ctx context.Context, pluginID, key string) (bool, bool, error) {
			man, found, err := plugins.InstalledManifest(ctx, d.Db, pluginID)
			if err != nil || !found {
				return false, false, err
			}
			return man.SettingDeclaredSecret(key), true, nil
		},
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
	return msg, err
}
