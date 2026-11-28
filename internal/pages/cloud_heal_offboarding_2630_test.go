package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/credheal"
)

// ut-docs#2630 offboarding: a deleted shop names its undo date when the cloud
// sends one, tolerates its absence, and a purged shop says it is final.
func TestHealChip_StoreDeletedNamesRestorableUntil(t *testing.T) {
	now := time.Date(2031, 12, 1, 9, 0, 0, 0, time.UTC)
	orig := healChipClock
	healChipClock = func() time.Time { return now }
	t.Cleanup(func() { healChipClock = orig })

	until := time.Date(2031, 12, 20, 12, 0, 0, 0, time.UTC)
	v, ok := healChip("en", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonStoreDeleted, Detail: until.Format(time.RFC3339)}, true)
	if !ok {
		t.Fatal("no chip")
	}
	if !strings.Contains(v.Sentence, "can restore the shop on my.universaltill.com until ") || !strings.Contains(v.Sentence, "20/12/2031") {
		t.Errorf("sentence = %q, want the undo date 20/12/2031", v.Sentence)
	}
	if v.Href != healHrefThis || v.ButtonHref != healHrefData {
		t.Errorf("chip href %q / banner button href %q", v.Href, v.ButtonHref)
	}
	if v.Button != "Download your data" || v.Button2 != "Check again" || v.Button2Href != "" {
		t.Errorf("buttons = %q / %q (%q)", v.Button, v.Button2, v.Button2Href)
	}

	// No date from an older cloud: the old sentence stays.
	v, _ = healChip("en", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonStoreDeleted}, true)
	if !strings.Contains(v.Sentence, "reconnects by itself within a day") {
		t.Errorf("no-date sentence = %q", v.Sentence)
	}
}

func TestHealChip_StorePurgedIsFinalAndOffersDataAndNewShop(t *testing.T) {
	v, ok := healChip("en", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonStorePurged}, true)
	if !ok {
		t.Fatal("no chip")
	}
	if v.Short != "Shop permanently deleted" {
		t.Errorf("short = %q", v.Short)
	}
	if !strings.Contains(v.Sentence, "can no longer be restored") || !strings.Contains(v.Sentence, "sales on this till stay here") {
		t.Errorf("sentence = %q", v.Sentence)
	}
	if v.Button != "Download your data" || v.Button2 != "Register as a new shop" || v.Button2Href != healHrefRegister {
		t.Errorf("buttons = %q / %q -> %q", v.Button, v.Button2, v.Button2Href)
	}
}
