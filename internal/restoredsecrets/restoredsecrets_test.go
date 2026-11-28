package restoredsecrets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSource struct{ items []Item }

func (f *fakeSource) UnreadableSecrets(context.Context) ([]Item, error) {
	return append([]Item(nil), f.items...), nil
}

type fakeStore struct{ m map[string]string }

func (f *fakeStore) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := f.m[k]
	return v, ok, nil
}
func (f *fakeStore) Set(_ context.Context, k, v string) error { f.m[k] = v; return nil }

func newSvc(t *testing.T, items ...Item) (*Service, *fakeSource, *fakeStore, string) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "till.db")
	src := &fakeSource{items: items}
	st := &fakeStore{m: map[string]string{}}
	return &Service{DBPath: db, Source: src, Store: st}, src, st, db
}

func TestNoMarkerNoScan(t *testing.T) {
	s, _, st, _ := newSvc(t, Item{"com.example.pay", "api_key"})
	got, err := s.Current(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("no restore happened: want no items, got %v err %v", got, err)
	}
	if _, ok := st.m[SettingKey]; ok {
		t.Fatal("nothing must be written without a restore")
	}
}

func TestMarkerTriggersScanListsPluginAndKeyOnly(t *testing.T) {
	s, _, st, db := newSvc(t, Item{"com.example.pay", "api_key"})
	if err := MarkPending(db); err != nil {
		t.Fatal(err)
	}
	if !Pending(db) {
		t.Fatal("marker must be pending after MarkPending")
	}
	got, err := s.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Item{"com.example.pay", "api_key"}) {
		t.Fatalf("want com.example.pay/api_key, got %v", got)
	}
	if Pending(db) {
		t.Fatal("marker must be consumed by the scan")
	}
	var stored []Item
	if err := json.Unmarshal([]byte(st.m[SettingKey]), &stored); err != nil || len(stored) != 1 {
		t.Fatalf("stored list must be plain JSON of (plugin,key): %q %v", st.m[SettingKey], err)
	}
	if strings.Contains(st.m[SettingKey], "enc:") {
		t.Fatal("no ciphertext in the stored list")
	}
}

func TestReEnteredItemDisappears(t *testing.T) {
	s, src, _, db := newSvc(t, Item{"a", "k1"}, Item{"a", "k2"})
	_ = MarkPending(db)
	if got, _ := s.Current(context.Background()); len(got) != 2 {
		t.Fatalf("want 2, got %v", got)
	}
	src.items = []Item{{"a", "k2"}} // k1 re-entered: it opens now
	got, _ := s.Current(context.Background())
	if len(got) != 1 || got[0].Key != "k2" {
		t.Fatalf("re-entered k1 must drop off the list, got %v", got)
	}
	src.items = nil
	if got, _ := s.Current(context.Background()); len(got) != 0 {
		t.Fatalf("list must empty, got %v", got)
	}
}

func TestDismissHidesUntilNextRestore(t *testing.T) {
	s, _, _, db := newSvc(t, Item{"a", "k1"})
	_ = MarkPending(db)
	_, _ = s.Current(context.Background())
	if err := s.Dismiss(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Current(context.Background()); len(got) != 0 {
		t.Fatalf("dismissed: want none, got %v", got)
	}
	_ = MarkPending(db)
	if got, _ := s.Current(context.Background()); len(got) != 1 {
		t.Fatalf("a new restore lists again, got %v", got)
	}
}

func TestScanNeverCreatesTheKeyFile(t *testing.T) {
	s, _, _, db := newSvc(t, Item{"a", "k1"})
	_ = MarkPending(db)
	_, _ = s.Current(context.Background())
	var found []string
	_ = filepath.Walk(filepath.Dir(db), func(p string, _ os.FileInfo, _ error) error {
		if strings.Contains(p, "plugin_settings_key") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("restored-secrets handling must never write a key file: %v", found)
	}
}
