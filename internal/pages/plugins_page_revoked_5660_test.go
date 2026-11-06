package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5660: /plugins shows why a plugin was turned off by a revocation.

type revokedItem struct {
	ID             string `json:"id"`
	Enabled        bool   `json:"enabled"`
	Revoked        bool   `json:"revoked"`
	RevokedVersion string `json:"revokedVersion"`
	RevokedReason  string `json:"revokedReason"`
	RevokedNotice  string `json:"revokedNotice"`
	RevokedAt      string `json:"revokedAt"`
}

func getPluginsPage5660(t *testing.T, d *common.Deps) (string, map[string]revokedItem) {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginsPage(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins = %d", rec.Code)
	}
	const open = `<script id="plugins-data" type="application/json">`
	start := strings.Index(body, open)
	if start < 0 {
		t.Fatal("plugins-data missing")
	}
	rest := body[start+len(open):]
	rest = rest[:strings.Index(rest, "</script>")]
	var payload struct {
		Items []revokedItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(rest), &payload); err != nil {
		t.Fatal(err)
	}
	m := map[string]revokedItem{}
	for _, it := range payload.Items {
		m[it.ID] = it
	}
	return body, m
}

func seedRevoked5660(t *testing.T, d *common.Deps, id, version, reason string, audit bool) {
	t.Helper()
	seedTestPlugin(t, d.Db, id, "Revoked Plugin", version)
	repo := data.NewPluginRepo(d.Db)
	ctx := t.Context()
	if err := repo.SetPluginState(ctx, id, version, data.PluginStateRevoked, false); err != nil {
		t.Fatal(err)
	}
	if audit {
		if err := repo.InsertAuditRaw(ctx, nil, "disable_revoked", "plugin", id,
			map[string]any{"version": version, "reason": reason}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPluginsPage_RevokedPluginShowsChipAndNotice_5660(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedRevoked5660(t, d, "com.x.rev", "1.4.2", "contains malware", true)
	seedTestPlugin(t, d.Db, "com.x.fine", "Fine", "1.0.0")

	body, items := getPluginsPage5660(t, d)
	it := items["com.x.rev"]
	if !it.Revoked || it.RevokedVersion != "1.4.2" || it.RevokedReason != "contains malware" || it.RevokedAt == "" {
		t.Fatalf("item = %+v", it)
	}
	for _, want := range []string{"1.4.2", "contains malware", "Plugin Store", "Reason given:"} {
		if !strings.Contains(it.RevokedNotice, want) {
			t.Errorf("notice %q missing %q", it.RevokedNotice, want)
		}
	}
	if items["com.x.fine"].Revoked {
		t.Error("unrevoked plugin must not carry the notice")
	}
	for _, want := range []string{`x-show="p.revoked"`, ">Withdrawn", "notice-block-warn", `p.revoked ? 'btn secondary' : 'btn'`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestPluginsPage_RevokedEmptyReasonOmitsReasonClause_5660(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedRevoked5660(t, d, "com.x.legacy", "2.0.0", "", false) // legacy row, no audit
	_, items := getPluginsPage5660(t, d)
	it := items["com.x.legacy"]
	if !it.Revoked || it.RevokedAt != "" {
		t.Fatalf("item = %+v", it)
	}
	if strings.Contains(it.RevokedNotice, "Reason given") || !strings.Contains(it.RevokedNotice, "2.0.0") {
		t.Fatalf("notice = %q", it.RevokedNotice)
	}
}

func TestPluginsPage_RevokedReasonScriptIsEscaped_5660(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedRevoked5660(t, d, "com.x.xss", "1.0.0", `<script>alert(1)</script>`, true)
	body, items := getPluginsPage5660(t, d)
	if strings.Contains(body, "<script>alert") {
		t.Fatal("raw <script> from the reason reached the page")
	}
	if !strings.Contains(items["com.x.xss"].RevokedReason, "<script>alert(1)</script>") {
		t.Fatalf("reason must survive as text, got %q", items["com.x.xss"].RevokedReason)
	}
	if strings.Contains(body, "x-html") {
		t.Fatal("page must never use x-html")
	}
}

func TestPluginsPage_ReenabledRevokedKeepsNotice_5660(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedRevoked5660(t, d, "com.x.back", "1.0.0", "r", true)
	if err := data.NewPluginRepo(d.Db).SetPluginActive(t.Context(), nil, "com.x.back", true); err != nil {
		t.Fatal(err)
	}
	_, items := getPluginsPage5660(t, d)
	if it := items["com.x.back"]; !it.Enabled || !it.Revoked {
		t.Fatalf("item = %+v", it)
	}
}

func TestPluginsPage_RevokedNoticeIsolatesVersionAndReasonForBidi_5660(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	seedRevoked5660(t, d, "com.x.bidi", "1.0.0", "because", true)
	_, items := getPluginsPage5660(t, d)
	n := items["com.x.bidi"].RevokedNotice
	if !strings.Contains(n, "⁨1.0.0⁩") || !strings.Contains(n, "⁨because⁩") {
		t.Fatalf("notice not FSI/PDI wrapped: %q", n)
	}
}
