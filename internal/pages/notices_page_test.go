package pages

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudnotice"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ADR-0176 (ut-docs#5366): bell, inbox, toast and critical strip. Local data
// only; the flag and notices.view gate everything server-side.

var (
	nMgr  = &auth.User{ID: "mgr-1", Role: "manager"}
	nCash = &auth.User{ID: "cash-1", Role: "cashier"}
)

type noticesRig struct {
	mux  *http.ServeMux
	db   *sql.DB
	repo *data.CloudNoticeRepo
	now  time.Time
}

func newNoticesRig(t *testing.T, on bool) *noticesRig {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state, Menu: []common.MenuItem{},
		Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerNoticesPages(mux, dp)
	rig := &noticesRig{mux: mux, db: db, repo: data.NewCloudNoticeRepo(db), now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	orig := noticesNow
	noticesNow = func() time.Time { return rig.now }
	t.Cleanup(func() { noticesNow = orig })
	if on {
		if err := data.NewSettingsRepo(db).Set(context.Background(), cloudnotice.SettingEnabled, "true"); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func (r *noticesRig) seed(t *testing.T, ns ...data.CloudNotice) {
	t.Helper()
	if err := r.repo.Mirror(context.Background(), ns, r.now); err != nil {
		t.Fatal(err)
	}
}

func (r *noticesRig) n(id, sev string) data.CloudNotice {
	return data.CloudNotice{ID: id, Rev: 1, SenderKind: "platform", SenderName: "Universal Till", Severity: sev,
		Lang: "en-gb", Title: "Title " + id, Body: "Body **" + id + "**",
		StartsAt: r.now.Add(-time.Hour), ExpiresAt: r.now.Add(48 * time.Hour), CreatedAt: r.now.Add(-time.Hour)}
}

func (r *noticesRig) do(method, path string, u *auth.User, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func TestNotices_FlagOffIsInvisible(t *testing.T) {
	r := newNoticesRig(t, false)
	r.seed(t, r.n("c", "critical"), r.n("i", "important"))
	for _, p := range []string{"/notices/bell", "/notices/strip", "/notices/toast"} {
		if rec := r.do("GET", p, nMgr, nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "" {
			t.Errorf("%s with the flag off = %d %q, want an empty 200", p, rec.Code, rec.Body.String())
		}
	}
	if rec := r.do("GET", "/notices", nMgr, nil); rec.Code != 404 {
		t.Errorf("GET /notices with the flag off = %d, want 404", rec.Code)
	}
	if rec := r.do("POST", "/notices/c/read", nMgr, nil); rec.Code != 404 {
		t.Errorf("POST read with the flag off = %d, want 404", rec.Code)
	}
}

func TestNotices_BellCountsUnreadAndCapsAtNine(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("a", "info"), r.n("b", "info"))
	rec := r.do("GET", "/notices/bell", nMgr, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="notices-bell"`) || !strings.Contains(body, "Notices, 2 unread") {
		t.Fatalf("bell = %s", body)
	}
	if strings.Contains(body, "crit") {
		t.Fatalf("no critical unread: the badge must not take the critical shape: %s", body)
	}
	var many []data.CloudNotice
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		many = append(many, r.n(id, "info"))
	}
	r.seed(t, many...)
	if body := r.do("GET", "/notices/bell", nMgr, nil).Body.String(); !strings.Contains(body, "9&#43;") {
		t.Fatalf("11 unread must show 9+: %s", body)
	}
	r.seed(t, r.n("z", "critical"))
	if body := r.do("GET", "/notices/bell", nMgr, nil).Body.String(); !strings.Contains(body, "notices-badge crit") {
		t.Fatalf("an unread critical changes the badge shape: %s", body)
	}
}

func TestNotices_CashierHasNoBellNoInboxNoToast_ServerSide(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("a", "info"), r.n("m", "important"))
	for _, p := range []string{"/notices/bell", "/notices/toast"} {
		if rec := r.do("GET", p, nCash, nil); strings.TrimSpace(rec.Body.String()) != "" {
			t.Errorf("cashier %s must be empty, got %q", p, rec.Body.String())
		}
	}
	if rec := r.do("GET", "/notices", nCash, nil); rec.Code != 403 {
		t.Errorf("cashier GET /notices = %d, want 403", rec.Code)
	}
	for _, p := range []string{"/notices/a/read", "/notices/a/dismiss", "/notices/a/ack"} {
		if rec := r.do("POST", p, nCash, nil); rec.Code != 403 {
			t.Errorf("cashier POST %s = %d, want 403", p, rec.Code)
		}
	}
	if n, _, _ := r.repo.UnreadCount(context.Background(), r.now); n != 2 {
		t.Fatalf("a refused write must change nothing, unread = %d", n)
	}
}

func TestNotices_CashierSeesOnlyTheCriticalStrip_ReadOnly(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("c", "critical"))
	body := r.do("GET", "/notices/strip", nCash, nil).Body.String()
	if !strings.Contains(body, "Title c") || !strings.Contains(body, "A manager has been told. You can keep selling.") {
		t.Fatalf("cashier strip = %s", body)
	}
	for _, bad := range []string{"notices-strip-ack", "notices-strip-open", "<button", "href="} {
		if strings.Contains(body, bad) {
			t.Errorf("a cashier's strip is read-only (no %q): %s", bad, body)
		}
	}
	// A partner "critical" can never exist; if one is held it never strips.
	pc := r.n("p", "critical")
	pc.SenderKind = "partner"
	r.seed(t, pc)
	if body := r.do("GET", "/notices/strip", nCash, nil).Body.String(); strings.Contains(body, "Title p") && !strings.Contains(body, "Title c") {
		t.Fatalf("partner critical stripped: %s", body)
	}
}

func TestNotices_ManagerAcknowledgesTheStrip(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("c", "critical"))
	body := r.do("GET", "/notices/strip", nMgr, nil).Body.String()
	if !strings.Contains(body, `data-testid="notices-strip-ack"`) || !strings.Contains(body, "Acknowledge") {
		t.Fatalf("manager strip = %s", body)
	}
	rec := r.do("POST", "/notices/c/ack?from=strip", nMgr, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("ack from strip = %d %q, want the now-empty strip", rec.Code, rec.Body.String())
	}
	if body := r.do("GET", "/notices/strip", nCash, nil).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("acknowledged: the strip is gone for the cashier too: %s", body)
	}
	n, _, _ := r.repo.Get(context.Background(), "c")
	if n.AckedAt == nil || n.ReadAt != nil {
		t.Fatalf("ack sets acked_at only (stays unread until opened): %+v", n)
	}
}

func TestNotices_AckRefusedForNonCritical(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("i", "info"))
	if rec := r.do("POST", "/notices/i/ack", nMgr, nil); rec.Code == 200 {
		t.Fatalf("only a critical notice can be acknowledged, got %d", rec.Code)
	}
}

func TestNotices_ToastOncePerIdRevAndOnlyImportant(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("i", "info"), r.n("m", "important"))
	body := r.do("GET", "/notices/toast", nMgr, nil).Body.String()
	if !strings.Contains(body, "pos-notice important") || !strings.Contains(body, "Title m") || !strings.Contains(body, `data-dismiss-ms="10000"`) {
		t.Fatalf("toast = %s", body)
	}
	if strings.Contains(body, "Title i") {
		t.Fatalf("info never pops up: %s", body)
	}
	if body := r.do("GET", "/notices/toast", nMgr, nil).Body.String(); strings.TrimSpace(body) != "" {
		t.Fatalf("never twice for the same id+rev: %s", body)
	}
	// A new revision is new content and may toast once more.
	m2 := r.n("m", "important")
	m2.Rev = 2
	r.seed(t, m2)
	if body := r.do("GET", "/notices/toast", nMgr, nil).Body.String(); !strings.Contains(body, "Title m") {
		t.Fatalf("a new rev toasts again: %s", body)
	}
}

func TestNotices_InboxOpenMarksReadDismissKeepsInAll(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("a", "info"))
	page := r.do("GET", "/notices", nMgr, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Title a") || !strings.Contains(page.Body.String(), "Unread") {
		t.Fatalf("inbox = %d %s", page.Code, page.Body.String())
	}
	open := r.do("POST", "/notices/a/read?tab=unread", nMgr, nil)
	if !strings.Contains(open.Body.String(), "<strong>a</strong>") || open.Header().Get("HX-Trigger") != "notices-changed" {
		t.Fatalf("opening shows the rendered body and tells the bell: %s / %v", open.Body.String(), open.Header())
	}
	if n, _, _ := r.repo.UnreadCount(context.Background(), r.now); n != 0 {
		t.Fatalf("opened = read, unread = %d", n)
	}
	r.do("POST", "/notices/a/dismiss?tab=all", nMgr, nil)
	all := r.do("GET", "/notices?tab=all", nMgr, nil).Body.String()
	unread := r.do("GET", "/notices?tab=unread", nMgr, nil).Body.String()
	if !strings.Contains(all, "Title a") || strings.Contains(unread, "Title a") {
		t.Fatalf("dismissed stays in All, leaves Unread")
	}
}

func TestNotices_CriticalIsNeverDismissedOnlyAcknowledged(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("c", "critical"))
	r.do("POST", "/notices/c/dismiss", nMgr, nil)
	n, _, _ := r.repo.Get(context.Background(), "c")
	if n.DismissedAt != nil {
		t.Fatal("a critical notice must not be dismissible")
	}
	open := r.do("POST", "/notices/c/read", nMgr, nil).Body.String()
	if !strings.Contains(open, "notice-ack") || strings.Contains(open, "notice-dismiss") {
		t.Fatalf("critical rows carry Acknowledge, not Dismiss: %s", open)
	}
}

func TestNotices_RendersEscapedAndOnlySafeLinks(t *testing.T) {
	r := newNoticesRig(t, true)
	x := r.n("x", "info")
	x.Title = `<script>alert(1)</script>`
	x.Body = "[bad](javascript:alert(1)) <img src=x onerror=alert(1)> **ok**"
	x.LinkJSON = `{"kind":"https","target":"javascript:alert(1)"}`
	p := r.n("p", "info")
	p.SenderKind, p.SenderName = "partner", `Acme <b>Partners</b>`
	p.LinkJSON = `{"kind":"https","target":"https://universaltill.com/news","label":"More"}`
	r.seed(t, x, p)
	list := r.do("GET", "/notices?tab=all", nMgr, nil).Body.String()
	if strings.Contains(list, "<script>alert") || strings.Contains(list, "<b>Partners</b>") {
		t.Fatalf("titles and sender names are escaped: %s", list)
	}
	if !strings.Contains(list, "From Acme &lt;b&gt;Partners&lt;/b&gt;") || !strings.Contains(list, "notice-sender partner") {
		t.Fatalf("partner sender chip is server-rendered and distinct: %s", list)
	}
	open := r.do("POST", "/notices/x/read", nMgr, nil).Body.String()
	// The text may show the author's characters, escaped; it may never carry
	// live markup: no element, no href/src attribute built from them.
	for _, bad := range []string{"<img", "<script", `href="javascript`} {
		if strings.Contains(open, bad) {
			t.Errorf("live unsafe markup %q in an opened notice: %s", bad, open)
		}
	}
	if strings.Contains(open, "<a ") {
		t.Fatalf("an unsafe link must render no anchor: %s", open)
	}
	if !strings.Contains(open, "<strong>ok</strong>") {
		t.Fatalf("safe markdown still renders: %s", open)
	}
	pop := r.do("POST", "/notices/p/read", nMgr, nil).Body.String()
	if !strings.Contains(pop, `href="https://universaltill.com/news"`) || !strings.Contains(pop, `rel="noopener noreferrer"`) || !strings.Contains(pop, `target="_blank"`) {
		t.Fatalf("safe https link: %s", pop)
	}
}

func TestNotices_SettingSwitchAndErasure(t *testing.T) {
	r := newNoticesRig(t, true)
	r.seed(t, r.n("a", "info"))
	if rec := r.do("POST", "/api/settings/notices", nCash, url.Values{"enabled": {"on"}}); rec.Code != 403 {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	if rec := r.do("POST", "/api/settings/notices", nMgr, url.Values{}); rec.Code != 204 {
		t.Fatalf("manager off = %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := r.repo.Pairs(context.Background()); len(p) != 0 {
		t.Fatal("turning it off erases the held notices")
	}
	if v, _, _ := data.NewSettingsRepo(r.db).Get(context.Background(), cloudnotice.SettingEnabled); v != "false" {
		t.Fatalf("enabled = %q", v)
	}
	if rec := r.do("POST", "/api/settings/notices", nMgr, url.Values{"enabled": {"on"}}); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if v, _, _ := data.NewSettingsRepo(r.db).Get(context.Background(), cloudnotice.SettingEnabled); v != "true" {
		t.Fatalf("enabled = %q", v)
	}
}
