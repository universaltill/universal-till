package data

import (
	"slices"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5066: the off-site backup state is this till's own and a restore
// drops it; a key added without both lists would sync to a replica or survive
// a restore.
func TestOffsiteBackupKeysArePerTillAndDroppedOnRestore(t *testing.T) {
	for _, k := range OffsiteBackupKeys {
		if !perTillSetting(k) {
			t.Errorf("%s is not per-till", k)
		}
		if !slices.Contains(db.RestoreDropKeys, k) {
			t.Errorf("%s is not in db.RestoreDropKeys", k)
		}
	}
	if len(OffsiteBackupKeys) != 16 {
		t.Errorf("key list changed (%d): update the restore drop list too", len(OffsiteBackupKeys))
	}
}
