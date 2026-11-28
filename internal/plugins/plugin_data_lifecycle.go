package plugins

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// Plugin data lifecycle (ut-docs#3157 P5). Uninstalling a plugin touches the
// code tree only; its data (data/plugin-data/<id>) is moved aside by the data
// manager and kept for the soft-delete window and, when the manifest declares
// retention.keep_years, for that many years with deletion refused meanwhile.

// retentionFromTree reads the retention declaration from the manifests left
// under pluginBaseDir/<id> (live version dirs and versions/ snapshots). The
// longest keep_years wins, so an older snapshot can never shorten a legal
// retention period; unreadable manifests are skipped. id must be validated.
func retentionFromTree(pluginBaseDir, id string) (years int, reasonKey string) {
	root := filepath.Join(pluginBaseDir, id)
	var paths []string
	for _, pat := range []string{"manifest.json", "*/manifest.json", "versions/*/manifest.json"} {
		if m, err := filepath.Glob(filepath.Join(root, pat)); err == nil {
			paths = append(paths, m...)
		}
	}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var mf struct {
			Retention *ManifestRetention `json:"retention"`
		}
		err = json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&mf)
		f.Close()
		if err != nil || mf.Retention == nil || mf.Retention.KeepYears <= years {
			continue
		}
		years, reasonKey = mf.Retention.KeepYears, mf.Retention.ReasonKey
	}
	return years, reasonKey
}

// DeletePluginDataNow is the operator's explicit "delete data now" for an
// uninstalled plugin's retained data. It is refused with plugindb.ErrRetained
// while the manifest's keep_years period runs. The id is validated like every
// other join.
func DeletePluginDataNow(ctx context.Context, id string) error {
	if err := validatePluginID(id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return DataManager().DeleteNow(id)
}

// PrunePluginData purges soft-deleted plugin data whose soft-delete window and
// retention period have both passed. The daily housekeeping job calls it.
func PrunePluginData() ([]string, error) {
	return DataManager().Prune()
}
