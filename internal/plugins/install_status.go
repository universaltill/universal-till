package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

const installStatusKeyPrefix = "plugins.marketplace.install_status."

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

type InstallStatusStore struct {
	settings *data.SettingsRepo
}

func NewInstallStatusStore(db *sql.DB) *InstallStatusStore {
	return &InstallStatusStore{settings: data.NewSettingsRepo(db)}
}

func (s *InstallStatusStore) Save(ctx context.Context, record InstallStatusRecord) error {
	record.ListingID = strings.TrimSpace(record.ListingID)
	if s == nil || s.settings == nil || record.ListingID == "" {
		return nil
	}
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.settings.Set(ctx, installStatusKey(record.ListingID), string(payload))
}

func (s *InstallStatusStore) Get(ctx context.Context, listingID string) (InstallStatusRecord, bool, error) {
	if s == nil || s.settings == nil {
		return InstallStatusRecord{}, false, nil
	}
	raw, ok, err := s.settings.Get(ctx, installStatusKey(listingID))
	if err != nil || !ok {
		return InstallStatusRecord{}, ok, err
	}
	var record InstallStatusRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return InstallStatusRecord{}, false, err
	}
	return record, true, nil
}

func (s *InstallStatusStore) List(ctx context.Context) (map[string]InstallStatusRecord, error) {
	if s == nil || s.settings == nil {
		return map[string]InstallStatusRecord{}, nil
	}
	all, err := s.settings.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]InstallStatusRecord)
	for key, value := range all {
		if !strings.HasPrefix(key, installStatusKeyPrefix) {
			continue
		}
		var record InstallStatusRecord
		if err := json.Unmarshal([]byte(value), &record); err != nil {
			return nil, err
		}
		result[record.ListingID] = record
	}
	return result, nil
}

func installStatusKey(listingID string) string {
	return installStatusKeyPrefix + strings.TrimSpace(listingID)
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
	default:
		return InstallFailure{MessageKey: "plugins.install.error.retryable", Message: "Install failed. You can retry.", Retryable: true}
	}
}
