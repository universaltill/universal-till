package enroll

import (
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
)

// TestMain points the data root at a throwaway directory: pairing and
// registering create the till's approval key under paths.Data("secrets")
// (ut-docs#5127), which must never land in the package directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ut-enroll-data-")
	if err != nil {
		panic("TestMain: data temp dir: " + err.Error())
	}
	paths.Init(dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
