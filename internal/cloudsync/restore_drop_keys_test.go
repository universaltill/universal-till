package cloudsync

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5069: a backup restore drops the "what this till last pushed"
// hashes, so the restored data is pushed again. A new push hash added here
// without db.RestoreDropKeys would let a restored replacement skip its
// push as "unchanged".
func TestPushHashesAreDroppedOnRestore(t *testing.T) {
	src, err := os.ReadFile("cloudsync.go")
	if err != nil {
		t.Fatal(err)
	}
	found := regexp.MustCompile(`"(cloudsync\.[a-z_.]+_hash)"`).FindAllSubmatch(src, -1)
	if len(found) == 0 {
		t.Fatal("no push hash keys found: the pattern is stale")
	}
	for _, m := range found {
		if k := string(m[1]); !slices.Contains(db.RestoreDropKeys, k) {
			t.Errorf("%s is not in db.RestoreDropKeys", k)
		}
	}
}
