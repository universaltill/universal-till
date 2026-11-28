package plugins

import (
	"sync"

	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/paths"
)

// The process-wide owner of every plugin's own SQLite file (ADR-0121 §4,
// ut-docs#3157). Like tcpConns / importFiles it must outlive the per-event
// module instance, and the installer, the pages and the runtime share it
// without a common owner object.
var (
	dataMgrMu   sync.Mutex
	dataMgr     *plugindb.Manager
	dataMgrRoot string
)

// DataManager returns the shared plugin-data manager, rooted at
// paths.Data("plugin-data"). It is rebuilt (the old one closed) when that root
// changes, which only happens in tests that point paths at a temp dir.
func DataManager() *plugindb.Manager {
	root := paths.Data("plugin-data")
	dataMgrMu.Lock()
	defer dataMgrMu.Unlock()
	if dataMgr == nil || dataMgrRoot != root {
		if dataMgr != nil {
			dataMgr.CloseAll()
		}
		dataMgr = plugindb.NewManager(root, nil)
		dataMgrRoot = root
	}
	return dataMgr
}

// closePluginData force-closes plugin id's open database (unload, disable,
// reload). The file stays; the next call reopens it.
func closePluginData(id string) {
	dataMgrMu.Lock()
	m := dataMgr
	dataMgrMu.Unlock()
	if m != nil {
		_ = m.Close(id)
	}
}

// closeAllPluginData closes every open plugin database (runtime shutdown).
func closeAllPluginData() {
	dataMgrMu.Lock()
	m := dataMgr
	dataMgrMu.Unlock()
	if m != nil {
		m.CloseAll()
	}
}
