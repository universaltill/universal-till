package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ErrManifestUnverified: the installed manifest.json exists but does not
// verify against the marketplace key (edited, unsigned, wrong key, package
// changed) or no verifier is available. Terminal, never "retry later".
var ErrManifestUnverified = errors.New("installed plugin manifest is not verified")

// VerifiedManifestSettings is the production manifest seam of the cloud's
// plugin-settings apply (ADR-0214 Decision 3, internal/pluginsecretapply):
// it returns the manifest of the plugin's active version only after
// ManifestVerifier.VerifyManifest (signature AND package digest) accepts it
// again now, so a manifest edited on disk after install declares nothing.
//
//   - installed=false, err=nil: no active version, or no manifest.json (the
//     plugin is not installed as far as this till can tell);
//   - err wrapping ErrManifestUnverified: present but refused (terminal);
//   - any other error: the file could not be read (the apply retries).
func VerifiedManifestSettings(ctx context.Context, db *sql.DB, mv *ManifestVerifier, pluginID string) (*Manifest, bool, error) {
	version, ok, err := data.NewPluginRepo(db).GetActivePluginVersion(ctx, pluginID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	base := filepath.Clean(paths.Plugins())
	dir := filepath.Join(base, pluginID, version)
	if dir == base || !filepath.HasPrefix(dir, base+string(os.PathSeparator)) {
		return nil, false, fmt.Errorf("%w: plugin %s v%s resolves outside the plugin dir", ErrManifestUnverified, pluginID, version)
	}
	path := filepath.Join(dir, "manifest.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("stat manifest for %s v%s: %w", pluginID, version, err)
	}
	if mv == nil {
		return nil, false, fmt.Errorf("%w: no verifier", ErrManifestUnverified)
	}
	res, err := mv.VerifyManifest(path)
	if err != nil || res == nil || res.Manifest == nil {
		return nil, false, fmt.Errorf("%w: %v", ErrManifestUnverified, err)
	}
	if res.Manifest.ID != pluginID {
		return nil, false, fmt.Errorf("%w: manifest id does not match", ErrManifestUnverified)
	}
	return res.Manifest, true, nil
}
