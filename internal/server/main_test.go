package server

import (
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
)

// TestMain points the data dir at a temp dir: Start creates the LAN TLS key
// under paths.Data("tls") (ut-docs#2736), and the default "./data" would put
// a private key in the source tree.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "server-test-data-")
	if err != nil {
		panic(err)
	}
	paths.Init(dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
