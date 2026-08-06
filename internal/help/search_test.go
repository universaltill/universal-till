package help_test

import (
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/help"
)

// searchFS builds a topic set where exactly one topic matches the query
// "printing" at each rank tier, so the expected order is unambiguous.
func searchFS() fstest.MapFS {
	return fstest.MapFS{
		// rank 3: body substring only
		"en/kitchen.md": {Data: mdTopic("kitchen", "Kitchen orders", "Selling", 1,
			[]string{"food"}, "Kitchen printing goes to a separate device.\n")},
		// rank 0: exact title match
		"en/printing.md": {Data: mdTopic("printing", "Printing", "Selling", 2,
			[]string{"thermal"}, "Prints receipts on a thermal printer.\n")},
		// rank 2: keyword match only
		"en/designer.md": {Data: mdTopic("designer", "Receipt designer", "Setup", 3,
			[]string{"printing", "logo"}, "Customise the logo and header.\n")},
		// rank 1: title substring
		"en/receipts.md": {Data: mdTopic("receipts", "Receipts & printing", "Selling", 4,
			[]string{"paper"}, "Receipts for customers.\n")},
		// no match at all
		"en/backups.md": {Data: mdTopic("backups", "Backups", "Setup", 5,
			[]string{"snapshot"}, "Snapshots of your data.\n")},
	}
}

func TestSearchRanksExactTitleThenSubstringThenKeywordThenBody(t *testing.T) {
	idx, err := help.LoadTopics(searchFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	got := idx.Search("en", "Printing")
	if len(got) != 4 {
		ids := make([]string, 0, len(got))
		for _, m := range got {
			ids = append(ids, m.ID)
		}
		t.Fatalf("Search = %v, want 4 matches", ids)
	}
	want := []string{"printing", "receipts", "designer", "kitchen"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("rank %d = %s, want %s", i, got[i].ID, id)
		}
	}
}

func TestSearchIsCaseInsensitive(t *testing.T) {
	idx, err := help.LoadTopics(searchFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	if got := idx.Search("en", "THERMAL"); len(got) != 1 || got[0].ID != "printing" {
		t.Fatalf("Search(THERMAL) = %+v, want [printing]", got)
	}
}

func TestSearchEmptyQueryReturnsNothing(t *testing.T) {
	idx, err := help.LoadTopics(searchFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	if got := idx.Search("en", ""); len(got) != 0 {
		t.Fatalf("Search(\"\") = %d results, want 0", len(got))
	}
	if got := idx.Search("en", "   "); len(got) != 0 {
		t.Fatalf("Search(blank) = %d results, want 0", len(got))
	}
}

func TestSearchFallsBackToEnglishForEmptyLanguage(t *testing.T) {
	idx, err := help.LoadTopics(searchFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	// "fa" has no topics in this fixture — search the en set instead.
	if got := idx.Search("fa", "printing"); len(got) != 4 {
		t.Fatalf("Search(fa) = %d results, want 4 en-fallback results", len(got))
	}
}

func TestSearchCapsResults(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := 0; i < 30; i++ {
		id := "topic-" + itoa(i)
		fsys["en/"+id+".md"] = &fstest.MapFile{Data: mdTopic(id, "Topic "+itoa(i), "S", i,
			nil, "everything mentions widgets here\n")}
	}
	idx, err := help.LoadTopics(fsys)
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	if got := idx.Search("en", "widgets"); len(got) > 20 {
		t.Fatalf("Search returned %d results, want capped at 20", len(got))
	}
}
