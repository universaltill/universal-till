package plugins

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
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
	// Status is the HTTP status the store download handler answers with; set
	// only by MarketplaceCodeFailure (zero elsewhere).
	Status int
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

// MarketplaceCodeFailure maps a marketplace.APIError code (found with
// errors.As through any "%w" wrapper) to the till's actionable, non-retryable
// message, and the HTTP status the store download handler answers with. It is
// the one table for these codes (ut-docs#3541), shared by ClassifyInstallError
// and the store download handler. The till-auth codes answer 403, never 401,
// so the UI doesn't read it as the operator's own login expiring.
//
// hasOwner says whether the till's store has an owner (the cloud-auth chip's
// storeHasOwner): an unclaimed till whose credential the cloud rejects is told
// to register as a new store, not to pair (ut-docs#5187).
func MarketplaceCodeFailure(err error, hasOwner bool) (InstallFailure, bool) {
	var apiErr *marketplace.APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return InstallFailure{}, false
	}
	switch apiErr.Code {
	case "not_entitled": // ut-docs#673: paid/unapproved listing
		return InstallFailure{MessageKey: "plugins.install.error.not_entitled", Message: "This plugin needs approval before it can be installed.", Status: http.StatusForbidden}, true
	case "listing_not_found": // ut-docs#703: stale catalog / removed listing
		return InstallFailure{MessageKey: "plugins.install.error.not_found", Message: "The selected plugin was not found in the catalog.", Status: http.StatusNotFound}, true
	case "registered_required": // ut-docs#704: anonymous till, registered-tier listing
		return InstallFailure{MessageKey: "plugins.install.error.registered_required", Message: "This plugin requires a claimed store account.", Status: http.StatusForbidden}, true
	case "store_auth_required": // paid plugin from a till that is not paired
		return InstallFailure{MessageKey: "plugins.install.error.pair_required", Message: "Pair this till with your shop to install this plugin.", Status: http.StatusForbidden}, true
	case "device_revoked": // till removed/replaced/moved from the shop (ut-docs#5187: data.reason)
		switch apiErr.Reason {
		case "replaced":
			return InstallFailure{MessageKey: "plugins.install.error.till_replaced", Message: "This till was replaced by another till in your shop.", Status: http.StatusForbidden}, true
		case "moved":
			return InstallFailure{MessageKey: "plugins.install.error.till_moved", Message: "This till was moved to another shop.", Status: http.StatusForbidden}, true
		}
		return InstallFailure{MessageKey: "plugins.install.error.till_removed", Message: "This till was removed from your shop.", Status: http.StatusForbidden}, true
	case "service_unavailable": // ut-cloud writeCountryRefused: country not served
		return InstallFailure{MessageKey: "plugins.install.error.service_unavailable", Message: "Plugin downloads are not available for this shop.", Status: http.StatusForbidden}, true
	case "unauthorized", "token_retired":
		if !hasOwner {
			return InstallFailure{MessageKey: "plugins.install.error.register_new", Message: "Register this till as a new store.", Status: http.StatusForbidden}, true
		}
		return InstallFailure{MessageKey: "plugins.install.error.pair_again", Message: "Pair this till with your shop again.", Status: http.StatusForbidden}, true
	case "device_mismatch":
		return InstallFailure{MessageKey: "plugins.install.error.pair_again", Message: "Pair this till with your shop again.", Status: http.StatusForbidden}, true
	}
	return InstallFailure{}, false
}

// hasOwner is forwarded to MarketplaceCodeFailure.
func ClassifyInstallError(err error, hasOwner bool) InstallFailure {
	// ut-docs#673/#703/#3541: marketplace codes are typed errors (errors.As
	// through any "%w" wrapper) and none can succeed on retry.
	if f, ok := MarketplaceCodeFailure(err, hasOwner); ok {
		return f
	}

	// ADR-0169: a cloud-hosted plugin (type `channel`) can never run on a
	// till, so a retry can never succeed.
	if errors.Is(err, ErrCloudHostedPlugin) {
		return InstallFailure{MessageKey: "plugins.install.error.incompatible", Message: "This plugin is not compatible with this till.", Retryable: false}
	}

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
	// ut-docs#499: a page entry route collision (validatePageEntryRoutes in
	// manifest.go) — same permanent-failure shape as the key case above, a
	// distinct namespace. Scoped to "page entry route" so it never matches
	// the key-collision message above.
	case strings.Contains(msg, "page entry route") && strings.Contains(msg, "is already provided by plugin"):
		return InstallFailure{MessageKey: "plugins.install.error.page_route_conflict", Message: "This plugin's page route conflicts with an existing one.", Retryable: false}
	default:
		return InstallFailure{MessageKey: "plugins.install.error.retryable", Message: "Install failed. You can retry.", Retryable: true}
	}
}
