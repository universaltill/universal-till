package plugins

import (
	"context"
	"testing"
)

// CanonicalTypes and the plugin_entries.type CHECK constraint are documented
// as mirrors of each other (manifest_verifier.go, ADR-0002: "code + CHECK +
// docs together"), but nothing proved it at the DB level — `language` was
// added to CanonicalTypes without ever reaching the CHECK, so a language
// pack declaring an entry of its own type failed at persist time with a raw
// constraint error. This installs one entry of every canonical type through
// the real migrated schema, so the two can no longer drift apart silently.
func TestPersistManifest_EveryCanonicalEntryTypeInserts(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	for i, typ := range CanonicalTypes {
		t.Run(typ, func(t *testing.T) {
			m := &Manifest{
				ID:      "com.example.type-" + typ,
				Name:    "Type " + typ,
				Version: "1.0.0",
				Runtime: "none",
				Entries: []ManifestEntry{{
					Type:  typ,
					Key:   "k" + typ,
					Label: "Label " + typ,
					Route: "/plugin/type-" + typ,
					// payment entries must be lowercase and distinct; every
					// other type ignores these.
					SortOrder: i,
				}},
			}
			if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
				t.Fatalf("entry type %q is in CanonicalTypes but the schema refuses it: %v", typ, err)
			}
		})
	}
}
