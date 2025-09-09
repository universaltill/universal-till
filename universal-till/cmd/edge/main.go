package main

import (
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/common"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

func pluginDir(id string) string { return filepath.Join("data", "plugins", id) }

func pluginDownloaded(id string) bool {
	st, err := os.Stat(filepath.Join(pluginDir(id), "index.html"))
	return err == nil && !st.IsDir()
}

	mux.HandleFunc("/api/plugins/state", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" { w.WriteHeader(http.StatusBadRequest); return }
		cur := settings.GetAll()
		installed := cur.InstalledPlugins != nil && cur.InstalledPlugins[id]
		downloaded := pluginDownloaded(id)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"installed": installed, "downloaded": downloaded})
	})

	// download bundle to local folder
	mux.HandleFunc("/api/plugins/download", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		bundleURL := strings.TrimSpace(r.Form.Get("bundleUrl"))
		if id == "" || bundleURL == "" { w.WriteHeader(http.StatusBadRequest); return }
		dir := pluginDir(id)
		_ = os.MkdirAll(dir, 0o755)
		// local copy or remote fetch
		if strings.HasPrefix(bundleURL, "/") {
			src := bundleURL
			if strings.HasPrefix(src, "/public/") { src = filepath.Join("web", src[1:]) }
			in, err := os.Open(src)
			if err == nil {
				defer in.Close()
				out, err := os.Create(filepath.Join(dir, "index.html"))
				if err == nil { io.Copy(out, in); out.Close() }
			}
		} else if strings.HasPrefix(bundleURL, "http://") || strings.HasPrefix(bundleURL, "https://") {
			resp, err := http.Get(bundleURL)
			if err == nil {
				defer resp.Body.Close()
				out, err := os.Create(filepath.Join(dir, "index.html"))
				if err == nil { io.Copy(out, resp.Body); out.Close() }
			}
		}
		w.Header().Set("Location", "/plugins")
		w.WriteHeader(http.StatusSeeOther)
	})

	// install: require downloaded, then register
	mux.HandleFunc("/api/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		route := strings.TrimSpace(r.Form.Get("route"))
		label := strings.TrimSpace(r.Form.Get("label"))
		if id == "" { w.WriteHeader(http.StatusBadRequest); return }
		if !pluginDownloaded(id) { w.WriteHeader(http.StatusBadRequest); return }
		cur := settings.GetAll()
		if cur.InstalledPlugins == nil { cur.InstalledPlugins = map[string]bool{} }
		cur.InstalledPlugins[id] = true
		if cur.PluginRecords == nil { cur.PluginRecords = map[string]common.PluginRecord{} }
		if route == "" { route = "/plug/" + id }
		if label == "" { label = id }
		cur.PluginRecords[id] = common.PluginRecord{Route: route, Label: label, Path: pluginDir(id)}
		_ = settings.SetAll(cur)
		w.Header().Set("Location", "/plugins")
		w.WriteHeader(http.StatusSeeOther)
	})

	// uninstall: deregister (keep files)
	mux.HandleFunc("/api/plugins/uninstall", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" { w.WriteHeader(http.StatusBadRequest); return }
		cur := settings.GetAll()
		if cur.InstalledPlugins != nil { delete(cur.InstalledPlugins, id) }
		if cur.PluginRecords != nil { delete(cur.PluginRecords, id) }
		_ = settings.SetAll(cur)
		w.Header().Set("Location", "/plugins")
		w.WriteHeader(http.StatusSeeOther)
	})

	// delete: remove files (and unregister if present)
	mux.HandleFunc("/api/plugins/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" { w.WriteHeader(http.StatusBadRequest); return }
		cur := settings.GetAll()
		if cur.InstalledPlugins != nil { delete(cur.InstalledPlugins, id) }
		if cur.PluginRecords != nil { delete(cur.PluginRecords, id) }
		_ = settings.SetAll(cur)
		_ = os.RemoveAll(pluginDir(id))
		w.Header().Set("Location", "/plugins")
		w.WriteHeader(http.StatusSeeOther)
	})
