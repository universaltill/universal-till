package catalogpull

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6243: a till that held icons while in till mode (cloud holds "")
// enters cloud mode: the transaction caching the mode marks them, so the
// push uploads them and the pull never clears them.
func TestModeEntryUploadsIconAndPullKeepsIt_6243(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.exec(`UPDATE settings SET value = 'till' WHERE key = 'entitlement.catalog_master'`)
	h.setIcon("items", id, pendingItemIcon) // till mode: no trigger mark
	if h.marks() != 0 {
		t.Fatalf("marks = %d, till mode must not mark", h.marks())
	}

	n, err := data.NewSettingsRepo(h.db).SetManyMarkingCatalogModeEntry(context.Background(),
		map[string]string{"entitlement.catalog_master": "cloud"})
	if err != nil || n != 1 {
		t.Fatalf("mode entry marks = %d, %v; want 1", n, err)
	}

	if p := h.push(); p.Accepted != 1 {
		t.Fatalf("push = %+v, want the icon accepted", p)
	}
	seen := 0
	for _, ch := range h.sentChanges() {
		if ch.Kind != "item" {
			continue
		}
		seen++
		if !reflect.DeepEqual(ch.ChangedFields, []string{"icon"}) {
			t.Fatalf("changed_fields = %v", ch.ChangedFields)
		}
		if got := norm(ch.Before["icon"]); got != `""` {
			t.Fatalf("before icon = %s", got)
		}
		if got := norm(ch.After["icon"]); got != `"`+pendingItemIcon+`"` {
			t.Fatalf("after icon = %s", got)
		}
	}
	if seen != 1 {
		t.Fatalf("sent %d item changes, want 1", seen)
	}
	h.mustRun()
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q after the pull, want kept", got)
	}
}
