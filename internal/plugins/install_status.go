package plugins

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

type InstallLifecycleState string

const (
	InstallStateRequested   InstallLifecycleState = "requested"
	InstallStateDownloading InstallLifecycleState = "downloading"
	InstallStateInstalling  InstallLifecycleState = "installing"
	InstallStateActive      InstallLifecycleState = "active"
	InstallStateFailed      InstallLifecycleState = "failed"
)

type InstallStatusRecord struct {
	ListingID      string                `json:"listing_id"`
	PluginID       string                `json:"plugin_id,omitempty"`
	PluginName     string                `json:"plugin_name,omitempty"`
	TargetVersion  string                `json:"target_version,omitempty"`
	CurrentVersion string                `json:"current_version,omitempty"`
	State          InstallLifecycleState `json:"state"`
	MessageKey     string                `json:"message_key,omitempty"`
	Retryable      bool                  `json:"retryable,omitempty"`
	UpdatedAt      string                `json:"updated_at"`
}

type InstallFailure struct {
	MessageKey string
	Message    string
	Retryable  bool
}

// InstallStatusStore keeps the marketplace installer's bookkeeping — one
// record per listing — in the plugin_install_status table (migration 007
// moved them out of the settings KV store).
type InstallStatusStore struct {
	repo *data.InstallStatusRepo
}

func NewInstallStatusStore(db *sql.DB) *InstallStatusStore {
	return &InstallStatusStore{repo: data.NewInstallStatusRepo(db)}
}

func recordFromRow(row data.InstallStatusRow) InstallStatusRecord {
	return InstallStatusRecord{
		ListingID:      row.ListingID,
		PluginID:       row.PluginID,
		PluginName:     row.PluginName,
		TargetVersion:  row.TargetVersion,
		CurrentVersion: row.CurrentVersion,
		State:          InstallLifecycleState(row.State),
		MessageKey:     row.MessageKey,
		Retryable:      row.Retryable,
		UpdatedAt:      row.UpdatedAt,
	}
}

func (s *InstallStatusStore) Save(ctx context.Context, record InstallStatusRecord) error {
	record.ListingID = strings.TrimSpace(record.ListingID)
	if s == nil || s.repo == nil || record.ListingID == "" {
		return nil
	}
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.repo.Upsert(ctx, data.InstallStatusRow{
		ListingID:      record.ListingID,
		PluginID:       record.PluginID,
		PluginName:     record.PluginName,
		TargetVersion:  record.TargetVersion,
		CurrentVersion: record.CurrentVersion,
		State:          string(record.State),
		MessageKey:     record.MessageKey,
		Retryable:      record.Retryable,
		UpdatedAt:      record.UpdatedAt,
	})
}

func (s *InstallStatusStore) Get(ctx context.Context, listingID string) (InstallStatusRecord, bool, error) {
	if s == nil || s.repo == nil {
		return InstallStatusRecord{}, false, nil
	}
	row, ok, err := s.repo.Get(ctx, strings.TrimSpace(listingID))
	if err != nil || !ok {
		return InstallStatusRecord{}, ok, err
	}
	return recordFromRow(row), true, nil
}

func (s *InstallStatusStore) List(ctx context.Context) (map[string]InstallStatusRecord, error) {
	if s == nil || s.repo == nil {
		return map[string]InstallStatusRecord{}, nil
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]InstallStatusRecord, len(rows))
	for _, row := range rows {
		result[row.ListingID] = recordFromRow(row)
	}
	return result, nil
}

// ClearForPlugin removes install-status records that refer to the given plugin
// ID (records are keyed by listing ID, so match on the stored PluginID). Called
// on uninstall so the plugins page doesn't keep showing a stale "active" state.
func (s *InstallStatusStore) ClearForPlugin(ctx context.Context, pluginID string) error {
	pluginID = strings.TrimSpace(pluginID)
	if s == nil || s.repo == nil || pluginID == "" {
		return nil
	}
	return s.repo.DeleteForPlugin(ctx, pluginID)
}

func ClassifyInstallError(err error) InstallFailure {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case msg == "":
		return InstallFailure{MessageKey: "plugins.install.error.retryable", Message: "Install failed. You can retry.", Retryable: true}
	case strings.Contains(msg, "incompatible architecture"):
		return InstallFailure{MessageKey: "plugins.install.error.incompatible", Message: "This plugin is not compatible with this till.", Retryable: false}
	case strings.Contains(msg, "public key not configured"), strings.Contains(msg, "not configured"):
		return InstallFailure{MessageKey: "plugins.install.error.configuration", Message: "Marketplace installation is not configured.", Retryable: false}
	case strings.Contains(msg, "signature mismatch"), strings.Contains(msg, "artifact hash"), strings.Contains(msg, "verify manifest"), strings.Contains(msg, "manifest validation"), strings.Contains(msg, "executable not found"), strings.Contains(msg, "incomplete"):
		return InstallFailure{MessageKey: "plugins.install.error.invalid_package", Message: "The plugin package failed verification.", Retryable: false}
	// ut-docs#169: a payment entry key/label collision (with a built-in
	// tender, another installed plugin, or an uninstalled plugin's retained
	// tender row — see validatePaymentEntryKeys in manifest.go) is a
	// permanent failure: retrying with the same manifest always fails the
	// same way until the plugin author picks a different key/label. Scoped
	// to the true collision messages ("collides with"/"belongs to
	// plugin"/"is already provided by plugin"/"is already used by plugin")
	// — deliberately excludes the separate within-manifest-duplicate and
	// malformed-key validation errors, which stay on the generic retryable
	// default below (unrelated bug class, not this ticket's scope).
	case (strings.Contains(msg, "payment entry key") || strings.Contains(msg, "payment entry label")) &&
		(strings.Contains(msg, "collides with") || strings.Contains(msg, "belongs to plugin") ||
			strings.Contains(msg, "is already provided by plugin") || strings.Contains(msg, "is already used by plugin")):
		return InstallFailure{MessageKey: "plugins.install.error.payment_conflict", Message: "This plugin's payment key or label conflicts with an existing one.", Retryable: false}
	// ut-docs#472: a page entry key collision (validatePageEntryKeys in
	// manifest.go) is the same permanent-failure shape as the payment case
	// above — retrying the same manifest against the same till always
	// fails identically until the plugin author picks a different key.
	// Scoped to the true collision message ("is already provided by
	// plugin") so it doesn't also swallow the separate malformed-key
	// validation errors, which stay on the generic retryable default.
	case strings.Contains(msg, "page entry key") && strings.Contains(msg, "is already provided by plugin"):
		return InstallFailure{MessageKey: "plugins.install.error.page_conflict", Message: "This plugin's page key conflicts with an existing one.", Retryable: false}
	default:
		return InstallFailure{MessageKey: "plugins.install.error.retryable", Message: "Install failed. You can retry.", Retryable: true}
	}
}
