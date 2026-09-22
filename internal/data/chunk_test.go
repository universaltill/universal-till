package data

import (
	"strings"
	"testing"
)

// TestChunkStrings pins ChunkStrings' boundary behavior directly (no DB) —
// the empty case, an exact multiple of the chunk size, a remainder, and a
// chunk size bigger than the whole input.
func TestChunkStrings(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		size int
		want [][]string
	}{
		{"empty", nil, 3, nil},
		{"smaller than one chunk", []string{"a", "b"}, 5, [][]string{{"a", "b"}}},
		{"exact multiple", []string{"a", "b", "c", "d"}, 2, [][]string{{"a", "b"}, {"c", "d"}}},
		{"remainder", []string{"a", "b", "c", "d", "e"}, 2, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}},
		{"size one", []string{"a", "b"}, 1, [][]string{{"a"}, {"b"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkStrings(tc.ids, tc.size)
			if len(got) != len(tc.want) {
				t.Fatalf("ChunkStrings(%v, %d) = %v, want %v", tc.ids, tc.size, got, tc.want)
			}
			for i := range got {
				if strings.Join(got[i], ",") != strings.Join(tc.want[i], ",") {
					t.Fatalf("ChunkStrings(%v, %d)[%d] = %v, want %v", tc.ids, tc.size, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMergeMapInto pins the generic merge helper for both value types
// LoadAllActive/loadShopItems actually use it for.
func TestMergeMapInto(t *testing.T) {
	dst := map[string]bool{"a": true}
	MergeMapInto(dst, map[string]bool{"b": true, "c": false})
	if len(dst) != 3 || !dst["a"] || !dst["b"] || dst["c"] {
		t.Fatalf("unexpected merged bool map: %+v", dst)
	}

	prices := map[string]int64{"x": 100}
	MergeMapInto(prices, map[string]int64{"y": 200})
	if len(prices) != 2 || prices["x"] != 100 || prices["y"] != 200 {
		t.Fatalf("unexpected merged int64 map: %+v", prices)
	}
}
