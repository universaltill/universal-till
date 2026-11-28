package plugins

import (
	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/paths"
)

// SetDataManagerForTest installs m as the shared manager until restore runs.
func SetDataManagerForTest(m *plugindb.Manager) (restore func()) {
	dataMgrMu.Lock()
	prev, prevRoot := dataMgr, dataMgrRoot
	dataMgr, dataMgrRoot = m, paths.Data("plugin-data")
	dataMgrMu.Unlock()
	return func() {
		dataMgrMu.Lock()
		dataMgr, dataMgrRoot = prev, prevRoot
		dataMgrMu.Unlock()
	}
}
