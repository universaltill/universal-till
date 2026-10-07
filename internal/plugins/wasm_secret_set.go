package plugins

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// secretSetMaxValue caps one secret_set value (ADR-0121 §3).
const secretSetMaxValue = 64 << 10

// hostSecretSet stores a credential the plugin obtained itself (e.g. a TSE
// admin PUK it provisioned) under one of its OWN settings (ADR-0121 §3,
// ut-docs#3159). Needs secret:write; the key must be a setting the plugin's
// installed manifest declares `type: "secret"`, so the value is always
// sealed at rest (ADR-0082) through the same repository seam the settings
// editor uses. The value is a plain string, stored as a JSON string so
// settings_get hands it back unchanged. It is never logged.
//
// Returns 0, or -2 (no secret:write; key not a declared secret setting; no
// installed manifest), -3 (manifest unreadable, seal or DB failure — fails
// closed, never plaintext), -4 (empty key, key over StorageMaxKeyBytes,
// value over secretSetMaxValue, key or value not valid UTF-8 — json.Marshal
// would silently replace invalid bytes — or unreadable guest memory).
func hostSecretSet(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	// Both lengths are bounded before guest memory is read: the key is
	// logged on refusal, so it must never be a multi-MiB guest buffer.
	if valLen > secretSetMaxValue || keyLen > data.StorageMaxKeyBytes {
		return hostErrInvalid
	}
	key, kok := readGuest(m, keyPtr, keyLen)
	val, vok := readGuest(m, valPtr, valLen)
	if !kok || !vok || len(key) == 0 || !utf8.Valid(key) || !utf8.Valid(val) {
		return hostErrInvalid
	}
	keyStr := string(key)
	if err := CheckPermission(ctx, s.db, s.pluginID, "secret:write"); err != nil {
		return hostErrDenied
	}
	man, found, err := InstalledManifest(ctx, s.db, s.pluginID)
	if err != nil {
		logging.L().Errorf("[wasm:%s] secret_set %s: manifest unreadable, refusing (%v)", s.pluginID, keyStr, err)
		return hostErrInternal
	}
	if !found || !man.SettingDeclaredSecret(keyStr) {
		logging.L().Infof("[wasm:%s] secret_set denied: %s is not a declared secret setting", s.pluginID, keyStr)
		return hostErrDenied
	}
	valueJSON, _ := json.Marshal(string(val)) // a string never fails to marshal
	// Same generation bump as the settings editor (ut-docs#1941): cached
	// .ask answers are dropped once the write lands. Safe from inside a
	// guest call — publish releases the bus lock around every handler.
	repo := data.NewPluginRepo(s.db).OnSettingsChanged(func() { SharedBus(s.db).BumpGeneration() })
	if err := repo.UpsertPluginSettingScoped(ctx, s.pluginID, keyStr, string(valueJSON), "global", true); err != nil {
		// The error names the key and the cause, never the value.
		logging.L().Errorf("[wasm:%s] secret_set %s failed: %v", s.pluginID, keyStr, err)
		return hostErrInternal
	}
	return 0
}
