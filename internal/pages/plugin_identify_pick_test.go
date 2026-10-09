package pages

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/itemimages"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pos"
)

// The catalog.identify seam's pick (ADR-0121 amendment 2026-10-09 R2a,
// ut-docs#4006): a finished job's photo waits in a per-plugin
// pending-confirm slot; a tapped suggestion posts {_job, sku, qty} to
// /api/pos/identify/plugin/pick, which adds the line exactly as a scan
// does and only then, off the request, stores the photo as the item's
// newest ai_ref.

const pickRoute = "/api/pos/identify/plugin/pick"

// newPickHarness is the identify harness plus a sale engine that resolves
// TEA-1 to itm-tea, the scan and pick routes, and an isolated data dir.
func newPickHarness(t *testing.T) *identifyHarness {
	t.Helper()
	h := newIdentifyHarness(t)
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })
	origSlots := identifySlots
	identifySlots = newIdentifySlotRegistry()
	t.Cleanup(func() { identifySlots.clearAll(); identifySlots = origSlots })
	if _, err := h.d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('itm-tea','TEA-1','Tea',250,1)`); err != nil {
		t.Fatal(err)
	}
	h.d.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, stubResolver{
		"TEA-1": {SKU: "TEA-1", Name: "Tea", Qty: 1, PriceCents: 250, ItemID: "itm-tea", TaxRateBP: 2000},
	})
	registerPOSAPI(h.mux, h.d)
	// Registered last, so it runs first: a learning step still in flight
	// finishes before paths and the slots are restored.
	t.Cleanup(h.d.WaitForAsyncWork)
	h.answer(`{"document":{"version":1,"components":[{"type":"suggestions","items":[
		{"label":{"literal":"Tea"},"effect":{"add_to_basket":{"sku":"TEA-1","qty":2}}}]}]}}`)
	return h
}

// clearAll drops every slot and its file.
func (r *identifySlotRegistry) clearAll() {
	r.mu.Lock()
	slots := r.slots
	r.slots = map[string]*identifySlot{}
	r.mu.Unlock()
	for _, s := range slots {
		discardIdentifySlot(s)
	}
}

// realJPEG is a decodable photo (jpegBytes is only a sniffable header).
func realJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// identifyWith runs one capture of photo to its handed-out result.
func (h *identifyHarness) identifyWith(photo []byte) (string, string) {
	h.t.Helper()
	rec := h.post(photo)
	m := identifyPollRe.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		h.t.Fatalf("identify start = %d:\n%s", rec.Code, rec.Body.String())
	}
	return m[1], h.pollUntilDone(m[1]).Body.String()
}

func (h *identifyHarness) pick(job, sku string, qty string) *httptest.ResponseRecorder {
	h.t.Helper()
	form := url.Values{"_job": {job}, "sku": {sku}, "qty": {qty}}
	return h.do(http.MethodPost, pickRoute, form, true)
}

func aiRefs(t *testing.T, itemID string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(itemimages.AIRefDir(itemID), "*.png"))
	return m
}

// waitPick polls cond for up to 2 s: the learning step runs off the request.
func waitPick(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func basketQty(h *identifyHarness, sku string) float64 {
	b := h.d.Engine.Basket()
	for _, l := range b.Lines {
		if l.SKU == sku {
			return l.Qty
		}
	}
	return 0
}

func TestPluginIdentify_PickAddsLineThenStoresPhoto_4006(t *testing.T) {
	h := newPickHarness(t)
	job, body := h.identifyWith(realJPEG(t))
	for _, want := range []string{
		`hx-post="` + pickRoute + `"`,
		`hx-vals='{&#34;_job&#34;:&#34;` + job + `&#34;,&#34;qty&#34;:2,&#34;sku&#34;:&#34;TEA-1&#34;}'`,
		`hx-target="#basket"`, `hx-sync="#basket:replace"`, `data-identify-pick`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("result missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, `/api/pos/scan`) {
		t.Errorf("a suggestion still posts to /api/pos/scan:\n%s", body)
	}
	// The job is over, but its photo waits in the slot for a pick.
	if n := len(stagedUploads(t)); n != 1 {
		t.Fatalf("pending-confirm photo files = %d, want 1", n)
	}

	rec := h.pick(job, "TEA-1", "2")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="basket"`) {
		t.Fatalf("pick = %d, want the basket:\n%s", rec.Code, rec.Body.String())
	}
	if q := basketQty(h, "TEA-1"); q != 2 {
		t.Fatalf("basket qty = %v, want 2 (added exactly as a scan)", q)
	}
	waitPick(t, "the ai_ref on disk", func() bool { return len(aiRefs(t, "itm-tea")) == 1 })
	raw, err := os.ReadFile(aiRefs(t, "itm-tea")[0])
	if err != nil {
		t.Fatal(err)
	}
	if img, format, err := image.Decode(bytes.NewReader(raw)); err != nil || format != "png" || img.Bounds().Dx() != 8 {
		t.Fatalf("stored ai_ref: format %q, err %v — want the photo re-encoded as PNG", format, err)
	}
	waitPick(t, "the audit row", func() bool {
		var n int
		_ = h.d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='ai_identify_confirmed' AND entity_id='itm-tea'`).Scan(&n)
		return n == 1
	})
	assertNoStaged(t) // the slot's file is gone

	// The slot is taken once: the same button again adds the line, stores nothing.
	if rec := h.pick(job, "TEA-1", "1"); rec.Code != http.StatusOK {
		t.Fatalf("second pick = %d", rec.Code)
	}
	if q := basketQty(h, "TEA-1"); q != 3 {
		t.Fatalf("basket qty = %v, want 3", q)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(aiRefs(t, "itm-tea")); n != 1 {
		t.Fatalf("ai_ref files = %d after a second pick of the same job, want 1", n)
	}
}

func TestPluginIdentify_PickUnknownJobAddsLineOnly_4006(t *testing.T) {
	h := newPickHarness(t)
	job, _ := h.identifyWith(realJPEG(t))
	for _, stale := range []string{strings.Repeat("ab", 16), "", "not-hex", job + "x"} {
		if rec := h.pick(stale, "TEA-1", "1"); rec.Code != http.StatusOK {
			t.Fatalf("pick(%q) = %d", stale, rec.Code)
		}
	}
	if q := basketQty(h, "TEA-1"); q != 4 {
		t.Fatalf("basket qty = %v, want 4: an unknown _job still adds the line", q)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(aiRefs(t, "itm-tea")); n != 0 {
		t.Fatalf("an unknown _job stored %d photos", n)
	}
	if n := len(stagedUploads(t)); n != 1 {
		t.Fatalf("an unknown _job touched the real slot: %d files", n)
	}
}

func TestPluginIdentify_FailedJobKeepsNoSlot_4006(t *testing.T) {
	h := newPickHarness(t)
	h.answer(`{"redirect":"/plugin/views"}`) // refused at this seam: the job fails
	job, body := h.identifyWith(realJPEG(t))
	if !strings.Contains(body, "plugin-view-notice") {
		t.Fatalf("want the error notice:\n%s", body)
	}
	assertNoStaged(t)
	h.pick(job, "TEA-1", "1")
	time.Sleep(50 * time.Millisecond)
	if n := len(aiRefs(t, "itm-tea")); n != 0 {
		t.Fatalf("a failed job's pick stored %d photos", n)
	}
}

func TestPluginIdentify_NewCaptureReplacesSlot_4006(t *testing.T) {
	h := newPickHarness(t)
	old, _ := h.identifyWith(realJPEG(t))
	first := stagedUploads(t)
	if len(first) != 1 {
		t.Fatalf("slot files = %v", first)
	}
	newer, _ := h.identifyWith(realJPEG(t))
	now := stagedUploads(t)
	if len(now) != 1 || now[0] == first[0] {
		t.Fatalf("the newer capture must replace the slot and delete the old file: before %v, after %v", first, now)
	}
	// The old button adds the line but can never attach the newer photo.
	h.pick(old, "TEA-1", "1")
	time.Sleep(50 * time.Millisecond)
	if n := len(aiRefs(t, "itm-tea")); n != 0 {
		t.Fatalf("an old job's pick stored the newer photo")
	}
	h.pick(newer, "TEA-1", "1")
	waitPick(t, "the newer job's ai_ref", func() bool { return len(aiRefs(t, "itm-tea")) == 1 })
}

func TestPluginIdentify_SlotExpires_4006(t *testing.T) {
	h := newPickHarness(t)
	orig := identifySlotTTL
	identifySlotTTL = 50 * time.Millisecond
	t.Cleanup(func() { identifySlotTTL = orig })
	job, _ := h.identifyWith(realJPEG(t))
	assertNoStaged(t) // deleted when the slot expires, 50 ms after hand-out
	h.pick(job, "TEA-1", "1")
	if q := basketQty(h, "TEA-1"); q != 1 {
		t.Fatalf("basket qty = %v after an expired pick, want 1", q)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(aiRefs(t, "itm-tea")); n != 0 {
		t.Fatalf("an expired slot stored %d photos", n)
	}
}

// The sale never waits on, or fails with, the learning step.
func TestPluginIdentify_SlowStoreNeverDelaysTheSale_4006(t *testing.T) {
	h := newPickHarness(t)
	release := make(chan struct{})
	called := make(chan struct{}, 1)
	orig := identifyStoreRef
	identifyStoreRef = func(_ context.Context, _ string, _ image.Image) error {
		called <- struct{}{}
		<-release
		return os.ErrPermission
	}
	t.Cleanup(func() { identifyStoreRef = orig })
	defer close(release)

	job, _ := h.identifyWith(realJPEG(t))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- h.pick(job, "TEA-1", "1") }()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="basket"`) {
			t.Fatalf("pick = %d:\n%s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pick waited on the photo store")
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("the store step never ran")
	}
	// The learning step is tracked async work: shutdown (and a test's
	// cleanup) waits for it rather than leaving it to write after paths
	// changed under it.
	drained := make(chan struct{})
	go func() { h.d.WaitForAsyncWork(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("the learning step is not tracked on Deps.AsyncWork")
	case <-time.After(100 * time.Millisecond):
	}
}

// WebP or an undecodable photo is stored nowhere, and its file still goes.
func TestPluginIdentify_UndecodablePhotoStoresNothing_4006(t *testing.T) {
	h := newPickHarness(t)
	job, _ := h.identifyWith(jpegBytes(2048)) // a JPEG header, no image
	h.pick(job, "TEA-1", "1")
	assertNoStaged(t)
	if n := len(aiRefs(t, "itm-tea")); n != 0 {
		t.Fatalf("an undecodable photo stored %d files", n)
	}
	if q := basketQty(h, "TEA-1"); q != 1 {
		t.Fatalf("basket qty = %v, want 1", q)
	}
}

// The pick takes a form body only: scan's JSON branch reads its own "code",
// which would add one item while the photo is stored on the sku's (review
// finding 1, ut-docs#4006).
func TestPluginIdentify_PickRefusesJSONBody_4006(t *testing.T) {
	h := newPickHarness(t)
	job, _ := h.identifyWith(realJPEG(t))
	req := httptest.NewRequest(http.MethodPost, pickRoute+"?_job="+job+"&sku=TEA-1", strings.NewReader(`{"code":"COF-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("JSON pick = %d, want 400", rec.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(aiRefs(t, "itm-tea")); n != 0 || basketQty(h, "TEA-1") != 0 {
		t.Fatalf("a refused pick still acted: refs=%d", n)
	}
}

// A slot outlives the finished job's own result TTL, so a hand-out can
// never meet an already-fired pre-hand-out expiry (review finding 4).
func TestIdentifySlotTTLOutlivesJobResult_4006(t *testing.T) {
	if pluginJobResultTTL >= pluginJobUnpolledTTL+identifySlotTTL {
		t.Fatalf("pluginJobResultTTL %v must stay below the slot's pre-hand-out TTL %v", pluginJobResultTTL, pluginJobUnpolledTTL+identifySlotTTL)
	}
}
