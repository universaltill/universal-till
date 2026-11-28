package pages

import (
	"context"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/restoredsecrets"
)

// restoredSecretsSource adapts the plugin repo to restoredsecrets.Source.
type restoredSecretsSource struct{ repo *data.PluginRepo }

func (s restoredSecretsSource) UnreadableSecrets(ctx context.Context) ([]restoredsecrets.Item, error) {
	refs, err := s.repo.UnreadableSecretSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]restoredsecrets.Item, 0, len(refs))
	for _, r := range refs {
		out = append(out, restoredsecrets.Item{PluginID: r.PluginID, Key: r.Key})
	}
	return out, nil
}

func restoredSecretsService(d *common.Deps) *restoredsecrets.Service {
	dbPath := ""
	if d.Cfg != nil {
		dbPath = d.Cfg.DBPath
	}
	return &restoredsecrets.Service{
		DBPath: dbPath,
		Source: restoredSecretsSource{repo: data.NewPluginRepo(d.Db)},
		Store:  data.NewSettingsRepo(d.Db),
	}
}

// restoredSecretsBanner is the Plugins page banner data (design-B S7): the
// plugin secrets still to enter again after a restore. A failure never takes
// the page down; the banner just does not show.
func restoredSecretsBanner(d *common.Deps, r *http.Request) []restoredsecrets.Item {
	items, err := restoredSecretsService(d).Current(r.Context())
	if err != nil {
		logging.L().Warnf("[plugins] restored secrets list: %v", err)
		return nil
	}
	return items
}

// registerRestoredSecrets wires the banner's dismiss button.
func registerRestoredSecrets(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /plugins/restored-secrets/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "plugin_management") {
			return
		}
		if err := restoredSecretsService(d).Dismiss(r.Context()); err != nil {
			logging.L().Warnf("[plugins] dismiss restored secrets: %v", err)
			http.Error(w, "dismiss failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
