package help_test

import (
	"testing"

	"github.com/universaltill/universal-till/internal/help"
)

func TestGetOwnLanguageTopic(t *testing.T) {
	idx, err := help.LoadTopics(fixtureFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	top, translated := idx.Get("en", "sell")
	if top == nil || top.Lang != "en" || !translated {
		t.Fatalf("Get(en, sell) = %+v, %v; want en topic, translated=true", top, translated)
	}
	top, translated = idx.Get("fa", "sell")
	if top == nil || top.Lang != "fa" || !translated {
		t.Fatalf("Get(fa, sell) = %+v, %v; want fa topic, translated=true", top, translated)
	}
}

func TestGetFallsBackToEnglishUntranslated(t *testing.T) {
	idx, err := help.LoadTopics(fixtureFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	// fa has no quick-start topic — English serves, flagged untranslated.
	top, translated := idx.Get("fa", "quick-start")
	if top == nil || top.Lang != "en" {
		t.Fatalf("Get(fa, quick-start) = %+v; want en fallback topic", top)
	}
	if translated {
		t.Fatalf("Get(fa, quick-start) translated = true; want false (en fallback)")
	}
}

func TestGetUnknownTopicIsNil(t *testing.T) {
	idx, err := help.LoadTopics(fixtureFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	if top, _ := idx.Get("en", "no-such-topic"); top != nil {
		t.Fatalf("Get(en, no-such-topic) = %+v; want nil", top)
	}
	if top, _ := idx.Get("fa", "no-such-topic"); top != nil {
		t.Fatalf("Get(fa, no-such-topic) = %+v; want nil", top)
	}
}

func TestSectionsFallBackToEnglish(t *testing.T) {
	idx, err := help.LoadTopics(fixtureFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	// "tr" has no topics at all — the tree falls back to the full en tree.
	secs := idx.Sections("tr")
	if len(secs) != 2 {
		t.Fatalf("Sections(tr) = %d sections, want the 2 en sections", len(secs))
	}
	// fa has its own (single-topic) tree, so it is served as-is.
	secs = idx.Sections("fa")
	if len(secs) != 1 || secs[0].Topics[0].Lang != "fa" {
		t.Fatalf("Sections(fa) = %+v, want the fa tree", secs)
	}
}
