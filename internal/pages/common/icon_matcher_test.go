package common

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

type countingSettings struct {
	val   string
	err   error
	calls int
}

func (c *countingSettings) Get(_ context.Context, _ string) (string, bool, error) {
	c.calls++
	return c.val, c.err == nil && c.val != "", c.err
}

func wireTR(t *testing.T) {
	t.Helper()
	restore := httpx.SnapshotStateForTests()
	t.Cleanup(restore)
	i, err := config.NewI18nFS(fstest.MapFS{
		"en.json": {Data: []byte(`{"catalog.builtin_icon.tea":"Tea"}`)},
		"tr.json": {Data: []byte(`{"catalog.builtin_icon.tea":"Çay","catalog.builtin_icon.cake":"Pasta"}`)},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i, "tr")
}

func TestShopIconMatcher_UsesDefaultLanguageAndReadsSettingsOnce(t *testing.T) {
	wireTR(t)
	s := &countingSettings{}
	m := ShopIconMatcher(context.Background(), s)
	if s.calls != 1 {
		t.Fatalf("settings reads = %d, want 1", s.calls)
	}
	for i := 0; i < 50; i++ {
		if got := m.Icon("Çay", ""); got != "tea" {
			t.Fatalf("Çay = %q, want tea", got)
		}
	}
	if got := m.Icon("Pasta", ""); got != "cake" {
		t.Errorf("tr shop Pasta = %q, want cake", got)
	}
	if s.calls != 1 {
		t.Errorf("settings reads after matching = %d, want still 1", s.calls)
	}
}

func TestShopIconMatcher_SettingsErrorFailsOpen(t *testing.T) {
	wireTR(t)
	m := ShopIconMatcher(context.Background(), &countingSettings{err: errors.New("db down")})
	if got := m.Icon("Çay", ""); got != "tea" {
		t.Errorf("Çay = %q, want tea (default language still used)", got)
	}
}

func TestShopPickerIconGroups_ReadsSettingsOnce(t *testing.T) {
	wireTR(t)
	s := &countingSettings{}
	g := ShopPickerIconGroups(context.Background(), s)
	if s.calls != 1 {
		t.Fatalf("settings reads = %d, want 1", s.calls)
	}
	if !pickerHas(g, "tea", "cay") {
		t.Error("tea lacks cay")
	}
}

func TestShopPickerIconGroups_SettingsErrorFailsOpen(t *testing.T) {
	wireTR(t)
	g := ShopPickerIconGroups(context.Background(), &countingSettings{err: errors.New("db down")})
	if !pickerHas(g, "tea", "cay") {
		t.Error("default-language labels missing after settings error")
	}
}

func pickerHas(g []catimport.BuiltinIconGroup, key, word string) bool {
	for _, grp := range g {
		for _, ic := range grp.Icons {
			if ic.Key == key {
				for _, w := range strings.Fields(ic.Keywords) {
					if w == word {
						return true
					}
				}
			}
		}
	}
	return false
}
