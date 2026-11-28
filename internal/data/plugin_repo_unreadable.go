package data

import (
	"context"

	"github.com/universaltill/universal-till/internal/secrets"
)

// UnreadableSecretRef names a stored plugin secret this till cannot open
// (plugin id and key only: never a value, never ciphertext).
type UnreadableSecretRef struct {
	PluginID string
	Key      string
}

// UnreadableSecretSettings lists every sealed plugin_settings row that does
// not open under this till's key (ADR-0082: such a row reads as "not
// configured"). After a backup restore the sealing key file is absent by
// design, so this is the list the owner must enter again (ut-docs S7).
// It never logs or returns a value.
func (r *PluginRepo) UnreadableSecretSettings(ctx context.Context) ([]UnreadableSecretRef, error) {
	rows, err := r.executor(nil).QueryContext(ctx, `
SELECT plugin_id, key, value_json FROM plugin_settings
WHERE substr(value_json, 1, ?) = ? ORDER BY plugin_id, key`, len(secrets.Prefix), secrets.Prefix)
	if err != nil {
		return nil, pluginObs.wrap("list_unreadable", err)
	}
	defer rows.Close()
	var out []UnreadableSecretRef
	for rows.Next() {
		var ref UnreadableSecretRef
		var stored string
		if err := rows.Scan(&ref.PluginID, &ref.Key, &stored); err != nil {
			return nil, pluginObs.wrap("scan_unreadable", err)
		}
		if _, err := secrets.OpenWithDefault(ctx, stored); err != nil {
			out = append(out, ref)
		}
	}
	return out, rows.Err()
}
