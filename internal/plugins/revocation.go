package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// RevocationEntry represents one revoked plugin, decoded exactly as
// ut-cloud's GET /v1/revocations actually serves it (ut-docs#2380).
// ut-cloud's HTTP gateway is grpc-gateway's default protojson marshaler
// with no UseProtoNames override (confirmed repo-wide: no such option set
// anywhere in ut-cloud), so wire keys are the proto message's
// json_name/camelCase — never the till-side snake_case a naive
// encoding/json struct tag assumes. ut-cloud's Revocation message
// (pkg/contracts/cloud/v1/cloud.pb.go) carries exactly plugin_id (wire
// name: pluginId), version, action and reason — no developer_id or
// revoked_at field exists on the wire at all. Those two used to be
// declared here and always silently decoded to their zero value; more
// importantly, plugin_id/pluginId being the ONLY mismatched tag meant
// every entry's PluginID decoded as "", so GetPlugin(ctx, "", version)
// never found a match and revocation enforcement was a complete no-op —
// see the ticket for the full trace.
//
// ID kinds (ut-docs#5653): PluginID is the marketplace LISTING UUID, not
// the plugin manifest id the till's plugins table is keyed by. ManifestID
// is the manifest id (the listing slug); older ut-cloud builds omit it.
// processRevocation therefore resolves the target through ManifestID, then
// the plugin_install_status row for the listing, then PluginID directly.
// Contract: ut-docs reference/contracts/plugin-revocation-feed.md.
type RevocationEntry struct {
	PluginID   string `json:"pluginId"`
	ManifestID string `json:"manifestId,omitempty"`
	Version    string `json:"version,omitempty"` // Empty means all versions
	// Action is "disable" or "delete" on the wire. Decoded for
	// completeness (it's part of the real contract) but not yet acted on
	// differently — processRevocation treats every entry as a disable,
	// same as before this fix. Differentiating "delete" (a real uninstall,
	// internal/plugins.UninstallPlugin) from "disable" is a deliberate
	// follow-up, not folded into this security fix.
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// RevocationFeed contains the list of revoked plugins from ut-cloud.
// LatestVersion mirrors GetRevocationsResponse.LatestVersion — an int64
// on the proto side, which protojson encodes as a JSON STRING, not a
// number; kept as a string rather than mis-decoding it (marketplace.Client's
// own copy of this field had the same int64 mismatch until ut-docs#2386
// fixed it to match). SyncRevocations parses it and persists it as the
// since_version cursor (revocationCursorKey, ut-docs#5659); the advance
// rules are in the contract page.
type RevocationFeed struct {
	Revocations   []RevocationEntry `json:"revocations"`
	LatestVersion string            `json:"latestVersion,omitempty"`
}

const (
	// maxRevocationFeedBytes caps the feed body the till will read. An entry
	// is ~250 B plus a free-text reason, so 4 MiB is ~15k revocations, far
	// above any realistic full feed. Documented in the contract page.
	maxRevocationFeedBytes int64 = 4 << 20
	// revocationCursorKey holds the last applied latestVersion (decimal
	// int64, unix nanos on the cloud side). Per-till (data.PerTillSettingPrefixes).
	revocationCursorKey = "plugins.revocation_cursor"
	// revocationFullAtKey holds the unix seconds of the last complete full
	// (since_version omitted) pass.
	revocationFullAtKey = "plugins.revocation_full_at"
	// revocationFullRefreshEvery bounds how long a revocation the cursor
	// would never re-deliver (file import, manual re-enable, cloud rollback)
	// can stay unnoticed: a full feed is fetched at least this often.
	revocationFullRefreshEvery = 24 * time.Hour
)

// RevocationChecker handles plugin revocation synchronization
type RevocationChecker struct {
	db             *sql.DB
	marketplaceURL string
	httpClient     *http.Client
	supervisor     *Supervisor
	// onRevoked runs once after a sync that disabled at least one plugin,
	// so loaded WASM plugins are unloaded without a restart (the app wires
	// Deps.ReloadPlugins here). Optional.
	onRevoked func(context.Context) error
	// now and maxBody are test seams; nil/zero mean time.Now and
	// maxRevocationFeedBytes (see clock and limit).
	now     func() time.Time
	maxBody int64
}

func (rc *RevocationChecker) clock() time.Time {
	if rc.now != nil {
		return rc.now()
	}
	return time.Now()
}

func (rc *RevocationChecker) limit() int64 {
	if rc.maxBody > 0 {
		return rc.maxBody
	}
	return maxRevocationFeedBytes
}

// SetOnRevoked registers the hook SyncRevocations calls after genuine
// disables. A hook error is logged and never fails the sync.
func (rc *RevocationChecker) SetOnRevoked(fn func(context.Context) error) {
	rc.onRevoked = fn
}

// NewRevocationChecker creates a new revocation checker
func NewRevocationChecker(db *sql.DB, marketplaceURL string, supervisor *Supervisor) *RevocationChecker {
	return &RevocationChecker{
		db:             db,
		marketplaceURL: marketplaceURL,
		httpClient:     netaccess.NewClient(30 * time.Second),
		supervisor:     supervisor,
	}
}

// loadCursorState reads the persisted cursor and last full-pass time. Any
// missing, unparsable or negative value, or a read error, yields the zero
// value: that means "fetch the full feed", the safe direction.
func (rc *RevocationChecker) loadCursorState(ctx context.Context) (int64, time.Time) {
	if rc.db == nil { // no store (a bare checker): always a full pass
		return 0, time.Time{}
	}
	repo := data.NewSettingsRepo(rc.db)
	var cursor int64
	var fullAt time.Time
	v, ok, err := repo.Get(ctx, revocationCursorKey)
	if err != nil {
		logging.L().Warnf("[RevocationChecker] Reading cursor failed, fetching full feed: %v", err)
	} else if ok {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			cursor = n
		}
	}
	v, ok, err = repo.Get(ctx, revocationFullAtKey)
	if err != nil {
		logging.L().Warnf("[RevocationChecker] Reading full-pass time failed, fetching full feed: %v", err)
	} else if ok {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			fullAt = time.Unix(n, 0)
		}
	}
	return cursor, fullAt
}

// revocationURL builds the feed URL; since_version is omitted when since
// is 0 so a full fetch also works against a server that ignores it.
func revocationURL(base string, since int64) (string, error) {
	u, err := url.Parse(fmt.Sprintf("%s/v1/revocations", base))
	if err != nil {
		return "", err
	}
	q := url.Values{}
	if since > 0 {
		q.Set("since_version", strconv.FormatInt(since, 10))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// readBoundedFeed reads at most limit bytes of the feed and fails, before
// anything is parsed, when the body is larger.
func readBoundedFeed(r io.Reader, contentLength, limit int64) ([]byte, error) {
	if contentLength > limit {
		return nil, fmt.Errorf("revocation feed exceeds %d bytes", limit)
	}
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read revocation feed: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("revocation feed exceeds %d bytes", limit)
	}
	return b, nil
}

func parseLatestVersion(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil && v > 0
}

// advanceRevocationState persists the new cursor (never regressing) and,
// for a completed full pass, its time. Persist errors are logged only: the
// next poll simply re-fetches.
func (rc *RevocationChecker) advanceRevocationState(ctx context.Context, feed RevocationFeed, cursor int64, full bool) {
	if rc.db == nil {
		return
	}
	log := logging.L()
	repo := data.NewSettingsRepo(rc.db)
	latest, ok := parseLatestVersion(feed.LatestVersion)
	switch {
	case !ok:
		log.Warnf("[RevocationChecker] Ignoring unusable latestVersion %q; cursor unchanged", feed.LatestVersion)
	case latest > cursor:
		if err := repo.Set(ctx, revocationCursorKey, strconv.FormatInt(latest, 10)); err != nil {
			log.Warnf("[RevocationChecker] Saving cursor failed: %v", err)
		}
	}
	if full {
		if err := repo.Set(ctx, revocationFullAtKey, strconv.FormatInt(rc.clock().Unix(), 10)); err != nil {
			log.Warnf("[RevocationChecker] Saving full-pass time failed: %v", err)
		}
	}
}

// SyncRevocations fetches revocation feed and disables revoked plugins (T030).
// It sends the persisted cursor as since_version, except for a full pass
// (no cursor, or the last full pass is 24 h old or in the future).
func (rc *RevocationChecker) SyncRevocations(ctx context.Context) (int, error) {
	log := logging.L()

	cursor, fullAt := rc.loadCursorState(ctx)
	now := rc.clock()
	full := cursor == 0 || fullAt.IsZero() || fullAt.After(now) || now.Sub(fullAt) >= revocationFullRefreshEvery
	since := cursor
	if full {
		since = 0
	}

	endpoint, err := revocationURL(rc.marketplaceURL, since)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch revocations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("marketplace returned status %d", resp.StatusCode)
	}

	body, err := readBoundedFeed(resp.Body, resp.ContentLength, rc.limit())
	if err != nil {
		return 0, err
	}
	var feed RevocationFeed
	if err := json.Unmarshal(body, &feed); err != nil {
		return 0, fmt.Errorf("failed to parse revocation feed: %w", err)
	}

	log.Infof("[RevocationChecker] Fetched %d revocation entries", len(feed.Revocations))

	// Process each revocation. revokedCount counts genuine disables only —
	// processRevocation's (bool, error) split is what makes that possible:
	// "not installed"/"already disabled" are legitimate no-ops (nil error,
	// disabled=false), not failures, but the caller's own "disabled %d
	// revoked plugins" log line (internal/server/server.go) should not
	// count them as if they were. Before this fix every entry with a nil
	// error incremented the count regardless — which, combined with the
	// PluginID decode bug above, meant this log line kept reporting
	// plausible-looking numbers while enforcement was silently a no-op.
	revokedCount := 0
	entryErrs := 0
	for _, entry := range feed.Revocations {
		disabled, err := rc.processRevocation(ctx, entry)
		if err != nil {
			log.Warnf("[RevocationChecker] Failed to process revocation for %s: %v", entry.PluginID, err)
			entryErrs++
			continue
		}
		if disabled {
			revokedCount++
		}
	}

	if revokedCount > 0 && rc.onRevoked != nil {
		if err := rc.onRevoked(ctx); err != nil {
			log.Warnf("[RevocationChecker] Reload after revocation failed: %v", err)
		}
	}

	// Advance only after every entry was handled: a failed entry is retried
	// from the same cursor next poll (processRevocation is idempotent).
	if entryErrs == 0 {
		rc.advanceRevocationState(ctx, feed, cursor, full)
	}

	return revokedCount, nil
}

// resolveTarget maps a feed entry to the installed plugin's manifest id.
// Candidates in order: the entry's ManifestID; the manifest id recorded in
// plugin_install_status for the entry's listing UUID; the raw PluginID.
// The first candidate with a plugins row matching entry.Version wins.
// Returns ("", "", nil) when nothing is installed under any of them.
func (rc *RevocationChecker) resolveTarget(ctx context.Context, entry RevocationEntry) (string, string, error) {
	repo := data.NewPluginRepo(rc.db)
	type cand struct{ id, by string }
	var cands []cand
	if entry.ManifestID != "" {
		cands = append(cands, cand{entry.ManifestID, "manifest_id"})
	}
	if entry.PluginID != "" {
		row, ok, err := data.NewInstallStatusRepo(rc.db).Get(ctx, entry.PluginID)
		if err != nil {
			return "", "", fmt.Errorf("failed to query install status: %w", err)
		}
		if ok && row.PluginID != "" {
			cands = append(cands, cand{row.PluginID, "listing_id"})
		}
		cands = append(cands, cand{entry.PluginID, "plugin_id"})
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		_, found, err := repo.GetPlugin(ctx, c.id, entry.Version)
		if err != nil {
			return "", "", fmt.Errorf("failed to query plugin: %w", err)
		}
		if found {
			return c.id, c.by, nil
		}
	}
	return "", "", nil
}

// processRevocation disables a specific revoked plugin. The returned bool
// reports whether a plugin was actually found, active and disabled — see
// SyncRevocations' own comment on why that's distinct from err == nil.
func (rc *RevocationChecker) processRevocation(ctx context.Context, entry RevocationEntry) (bool, error) {
	log := logging.L()
	repo := data.NewPluginRepo(rc.db)

	pluginID, matchedBy, err := rc.resolveTarget(ctx, entry)
	if err != nil {
		return false, err
	}
	if pluginID == "" {
		// Not installed under any id the entry can name: silent no-op.
		log.Debugf("[RevocationChecker] No installed plugin for revocation %s (manifest %q)", entry.PluginID, entry.ManifestID)
		return false, nil
	}
	pluginRow, found, err := repo.GetPlugin(ctx, pluginID, entry.Version)
	if err != nil {
		return false, fmt.Errorf("failed to query plugin: %w", err)
	}
	if !found {
		return false, nil
	}

	if !pluginRow.IsActive {
		// Already disabled
		return false, nil
	}

	// T031a: Check if plugin is currently running critical operations
	// For now, stop immediately - production should check for active hooks
	if rc.supervisor != nil {
		if err := rc.supervisor.StopPlugin(ctx, pluginID); err != nil {
			log.Warnf("[RevocationChecker] Failed to stop plugin %s: %v", pluginID, err)
		}
	}

	// Disable the plugin in database
	if err := repo.SetPluginState(ctx, pluginID, pluginRow.Version, "revoked", false); err != nil {
		return false, fmt.Errorf("failed to disable plugin: %w", err)
	}

	// Add audit log entry. No developer_id: the real feed never carries
	// one (see RevocationEntry's own comment) — recording an always-empty
	// field would be worse than not recording it at all.
	// "requested_action", not "action": the row's own action COLUMN is
	// already the literal string "disable_revoked" (this call's own first
	// arg) — a bare "action" key in the details map next to that reads as
	// if it were describing the same thing, when it's actually the feed
	// entry's requested action ("disable"|"delete"), which this handler
	// currently treats identically either way (see RevocationEntry.Action).
	_ = repo.InsertAuditRaw(ctx, nil, "disable_revoked", "plugin", pluginID, map[string]any{
		"reason":           entry.Reason,
		"version":          pluginRow.Version,
		"listing_id":       entry.PluginID,
		"matched_by":       matchedBy,
		"requested_action": entry.Action,
		"actor":            "system:revocation",
	}, time.Now())

	log.Infof("[RevocationChecker] Disabled revoked plugin: %s v%s (reason: %s)", pluginID, pluginRow.Version, entry.Reason)
	return true, nil
}
