package catalogpull

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6240: a replica holds icons the cloud never received and no marks
// for them (migration 103 marks nothing on a replica). Promoting it opens
// the marks, so the first pull keeps the icons and the next push sends them.

// replicaWithUnsentIcons is the state migration 103 leaves on a replica: a
// linked item and category with local icons, bases "" and no marks.
func replicaWithUnsentIcons(t *testing.T) (*pushHarness, string) {
	t.Helper()
	h := newPushHarness(t)
	id := h.seedCatAndItem()
	h.exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://main.local')`)
	h.setIcon("items", id, pendingItemIcon)
	h.setIcon("categories", cCat, pendingCatIcon)
	if h.marks() != 0 {
		t.Fatalf("marks = %d, a replica's triggers must not mark", h.marks())
	}
	return h, id
}

func TestPromotedReplicaKeepsAndUploadsUnsentIcon_6240(t *testing.T) {
	h, id := replicaWithUnsentIcons(t)
	if err := data.NewSettingsRepo(h.db).PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.marks() != 2 {
		t.Fatalf("marks = %d after promotion, want one per icon", h.marks())
	}
	h.myEdit("item", id, map[string]any{"price_minor": 350})
	h.myEdit("category", cCat, map[string]any{"name": "Hot drinks"})
	h.mustRun()
	if it, _ := h.item(id); it.Price != 350 {
		t.Fatalf("price = %d, the cloud's edit must still apply", it.Price)
	}
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q, want kept", got)
	}
	if got := h.icon("categories", cCat); got != pendingCatIcon {
		t.Fatalf("category icon = %q, want kept", got)
	}

	if p := h.push(); p.Accepted != 2 {
		t.Fatalf("push = %+v, want both accepted", p)
	}
	want := map[string]string{"item": pendingItemIcon, "category": pendingCatIcon}
	seen := 0
	for _, ch := range h.sentChanges() {
		w, ok := want[ch.Kind]
		if !ok {
			continue
		}
		seen++
		if !reflect.DeepEqual(ch.ChangedFields, []string{"icon"}) {
			t.Fatalf("%s changed_fields = %v", ch.Kind, ch.ChangedFields)
		}
		if got := norm(ch.Before["icon"]); got != `""` {
			t.Fatalf("%s before icon = %s", ch.Kind, got)
		}
		if got := norm(ch.After["icon"]); got != `"`+w+`"` {
			t.Fatalf("%s after icon = %s", ch.Kind, got)
		}
	}
	if seen != 2 {
		t.Fatalf("sent %d icon changes, want 2", seen)
	}
	h.mustRun()
	if got := h.icon("items", id); got != pendingItemIcon {
		t.Fatalf("item icon = %q after the echo", got)
	}
}

// The control: without the promotion marks the pull clears the icon. Pins
// the bug so the test above cannot pass for the wrong reason.
func TestReplicaPromotedWithoutMarksLosesIcon_6240(t *testing.T) {
	h, id := replicaWithUnsentIcons(t)
	h.exec(`DELETE FROM settings WHERE key = 'sync.primary_url'`)
	h.myEdit("item", id, map[string]any{"price_minor": 350})
	h.mustRun()
	if got := h.icon("items", id); got != "" {
		t.Fatalf("item icon = %q, the unmarked icon is expected to be cleared", got)
	}
}

// R5: each promoted field is an uploadable, no longer till-owned, field.
func TestPromotedUploadFieldsAreUploadFields_6240(t *testing.T) {
	for _, e := range data.PromotedUploadFieldKeys {
		kind, key := e[0], e[1]
		if !contains(uploadFields[kind], key) {
			t.Errorf("%s.%s is promoted but not in uploadFields", kind, key)
		}
		if contains(tillOwnedFields[kind], key) {
			t.Errorf("%s.%s is promoted but still in tillOwnedFields", kind, key)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
