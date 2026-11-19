package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#6549: listingCatalogTier reads the cached catalogue only and
// falls back to "" (never a network call, never a panic).
func TestListingCatalogTier(t *testing.T) {
	repo := seededSchedulerCatalogRepo(t, []marketplace.PluginSummary{
		{ID: "id-a", ListingID: "listing-a", TrustTier: "official"},
		{ID: "id-b", ListingID: "", TrustTier: "verified"},
	})
	d := &common.Deps{Cfg: &config.Config{DefaultLocale: schedulerTestCfg.DefaultLocale}, CatalogRepo: repo}

	for id, want := range map[string]string{
		"listing-a": "official", // by ListingID
		"id-a":      "official", // by ID
		"id-b":      "verified",
		"missing":   "",
		"":          "",
	} {
		if got := listingCatalogTier(d, id); got != want {
			t.Errorf("listingCatalogTier(%q) = %q, want %q", id, got, want)
		}
	}

	if got := listingCatalogTier(&common.Deps{Cfg: d.Cfg}, "listing-a"); got != "" {
		t.Errorf("nil CatalogRepo = %q, want empty", got)
	}
	if got := listingCatalogTier(nil, "listing-a"); got != "" {
		t.Errorf("nil deps = %q, want empty", got)
	}
}
