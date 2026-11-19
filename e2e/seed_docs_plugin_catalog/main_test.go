package main

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// The seed is only worth anything if the file it writes is the one the till
// reads: this loads it back through the real CatalogRepository.
func TestSeed_SnapshotIsTheFileTheTillReads(t *testing.T) {
	dir := t.TempDir()
	locale, arch := marketplace.TillCatalogKey("")
	if err := writeSnapshot(dir, buildSnapshot(locale, arch)); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	repo, err := marketplace.NewCatalogRepository(nil, dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	snap, _, err := repo.Get(locale, arch)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if snap == nil || len(snap.Plugins) != 6 {
		t.Fatalf("want 6 plugins, got %+v", snap)
	}
	want := listings()
	for i, p := range snap.Plugins {
		w := want[i]
		if p.TrustTier != w.TrustTier || p.PaidListing != w.PaidListing ||
			p.CanonicalType != w.CanonicalType || strings.Join(p.Permissions, ",") != strings.Join(w.Permissions, ",") {
			t.Errorf("listing %d (%s) did not round-trip: got %+v want %+v", i, w.Name, p, w)
		}
	}
}

func TestSeed_ListingsAreChipsAndTiersTheShotsNeed(t *testing.T) {
	canon := map[string]bool{}
	for _, c := range plugins.CanonicalTypes {
		canon[c] = true
	}
	types := map[string]bool{}
	ids := map[string]bool{}
	tiers := map[string]bool{}
	paid := 0
	validation := false
	for _, p := range listings() {
		if !canon[p.CanonicalType] {
			t.Errorf("%s: type %q is not a canonical type", p.Name, p.CanonicalType)
		}
		if types[p.CanonicalType] {
			t.Errorf("%s: type %q repeated; the shot needs 6 distinct category chips", p.Name, p.CanonicalType)
		}
		types[p.CanonicalType] = true
		for _, id := range []string{p.ID, p.ListingID} {
			if strings.HasPrefix(id, "com.universaltill.") {
				t.Errorf("%s: id %q would be matched as official/installed", p.Name, id)
			}
		}
		if p.ID != p.ListingID {
			t.Errorf("%s: id and listing_id differ", p.Name)
		}
		if ids[p.ID] {
			t.Errorf("duplicate id %q", p.ID)
		}
		ids[p.ID] = true
		tiers[p.TrustTier] = true
		if p.PaidListing {
			paid++
		}
		for _, perm := range p.Permissions {
			if host, isV, err := plugins.ParseValidationPermission(perm); isV {
				if err != nil || host == "" {
					t.Errorf("%s: bad validation permission %q: %v", p.Name, perm, err)
				}
				validation = true
			}
		}
	}
	for _, tier := range []string{"official", "verified", "unverified"} {
		if !tiers[tier] {
			t.Errorf("no %s listing", tier)
		}
	}
	if paid != 1 {
		t.Errorf("want exactly 1 paid listing, got %d", paid)
	}
	if !validation {
		t.Error("no net:validation: permission listing")
	}
	if len(types) != 6 {
		t.Errorf("want 6 distinct types, got %d", len(types))
	}
}

func TestSnapshotFileMatchesTheTill(t *testing.T) {
	// snapshotFile repeats marketplace's unexported snapshotFileName; seed the
	// real repository and check the file it reads is the one we named.
	dir := t.TempDir()
	locale, arch := marketplace.TillCatalogKey("")
	if err := writeSnapshot(dir, buildSnapshot(locale, arch)); err != nil {
		t.Fatal(err)
	}
	repo, _ := marketplace.NewCatalogRepository(nil, dir)
	if snap, _, err := repo.Get(locale, arch); err != nil || snap == nil {
		t.Fatalf("till does not find %s: %v", snapshotFile(locale, arch), err)
	}
}
