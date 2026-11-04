package pages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/selfupdate"
)

// ut-docs#5438: POST /api/update/upload. The selfupdate package has the real
// verification tests; these drive the real handler against a fake installer.

const fileTestArchive = "unitill-pos_9.9.9_linux_amd64.tar.gz"

type fakeFileInstall struct {
	dir        string
	cap        int64
	installErr error
	got        selfupdate.FileSet
	archiveLen int
	installs   int
	closed     int
}

func (f *fakeFileInstall) Dir() string       { return f.dir }
func (f *fakeFileInstall) ArchiveCap() int64 { return f.cap }
func (f *fakeFileInstall) Close()            { f.closed++ }
func (f *fakeFileInstall) Install(_ context.Context, in selfupdate.FileSet) (selfupdate.Installed, error) {
	f.installs++
	f.got = in
	if b, err := os.ReadFile(in.ArchivePath); err == nil {
		f.archiveLen = len(b)
	}
	if f.installErr != nil {
		return selfupdate.Installed{}, f.installErr
	}
	return selfupdate.Installed{Version: "9.9.9", KeyID: "key1"}, nil
}

// useFakeFileInstall swaps the handler seams; begun counts Begin calls.
func useFakeFileInstall(t *testing.T, fi *fakeFileInstall, availErr, beginErr error) *int {
	t.Helper()
	fi.dir = t.TempDir()
	if fi.cap == 0 {
		fi.cap = 1 << 20
	}
	begun := 0
	oa, ob := fileInstallAvailability, beginFileInstall
	fileInstallAvailability = func() error { return availErr }
	beginFileInstall = func() (fileInstaller, error) {
		begun++
		if beginErr != nil {
			return nil, beginErr
		}
		return fi, nil
	}
	t.Cleanup(func() { fileInstallAvailability, beginFileInstall = oa, ob })
	return &begun
}

type updPart struct {
	field, filename string
	body            []byte
}

func updStdParts() []updPart {
	return []updPart{
		{"checksums", "checksums.txt", []byte("abc  " + fileTestArchive + "\n")},
		{"signature", "checksums.txt.sig", []byte("ed25519 k sig\n")},
		{"archive", fileTestArchive, []byte("ARCHIVE-BYTES")},
	}
}

func updMultipart(t *testing.T, parts []updPart) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.field, p.filename))
		h.Set("Content-Type", "application/octet-stream")
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(p.body)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func postUpdUpload(mux *http.ServeMux, body io.Reader, ct string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/update/upload", body)
	req.Header.Set("Content-Type", ct)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func updUploadMux(t *testing.T) *http.ServeMux {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	_, _, d := newFullAuthDeps(t)
	api := http.NewServeMux()
	registerUpdateAPI(api, d)
	return api
}

func assertUpdCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code, wantText string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `role="alert"`) || !strings.Contains(out, `data-code="`+code+`"`) {
		t.Fatalf("want alert with data-code=%q: %s", code, out)
	}
	if wantText != "" && !strings.Contains(out, strings.NewReplacer("&#39;", "'", "&amp;", "&").Replace(wantText)) &&
		!strings.Contains(out, wantText) {
		// message text is HTML-escaped; compare unescaped
		if !strings.Contains(strings.NewReplacer("&#39;", "'", "&amp;", "&", "&#34;", `"`).Replace(out), wantText) {
			t.Errorf("missing text %q in %s", wantText, out)
		}
	}
}

func updEn(key string) string { return httpx.T("en", key) }

func TestUpdateUpload_CashierRefusedBeforeAnythingHappens(t *testing.T) {
	mux := updUploadMux(t)
	fi := &fakeFileInstall{}
	begun := useFakeFileInstall(t, fi, nil, nil)
	body, ct := updMultipart(t, updStdParts())
	rec := postUpdUpload(mux, body, ct, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	if *begun != 0 {
		t.Error("beginFileInstall called for a cashier")
	}
}

func TestUpdateUpload_ManagerInstalls(t *testing.T) {
	mux := updUploadMux(t)
	fi := &fakeFileInstall{}
	useFakeFileInstall(t, fi, nil, nil)
	body, ct := updMultipart(t, updStdParts())
	rec := postUpdUpload(mux, body, ct, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if h := rec.Header().Get("HX-Trigger"); !strings.Contains(h, "ut:update-installing") || !strings.Contains(h, `"version":"9.9.9"`) {
		t.Errorf("HX-Trigger = %q", h)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `data-testid="update-upload-installing"`) || !strings.Contains(out, `data-version="9.9.9"`) || !strings.Contains(out, "v9.9.9") {
		t.Errorf("success fragment: %s", out)
	}
	if fi.installs != 1 || fi.got.ArchiveName != fileTestArchive || fi.archiveLen != len("ARCHIVE-BYTES") ||
		!strings.HasPrefix(string(fi.got.Sums), "abc") || !strings.HasPrefix(string(fi.got.Sig), "ed25519") {
		t.Errorf("install input = %+v len=%d", fi.got, fi.archiveLen)
	}
	if filepath.Dir(fi.got.ArchivePath) != fi.dir {
		t.Errorf("archive written outside Dir(): %s", fi.got.ArchivePath)
	}
	if fi.closed != 1 {
		t.Errorf("Close called %d times", fi.closed)
	}
}

func TestUpdateUpload_AuditRow(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	_, _, d := newFullAuthDeps(t)
	api := http.NewServeMux()
	registerUpdateAPI(api, d)
	fi := &fakeFileInstall{}
	useFakeFileInstall(t, fi, nil, nil)
	body, ct := updMultipart(t, updStdParts())
	if rec := postUpdUpload(api, body, ct, &mgrUser); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'update_installed_from_file'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit update_installed_from_file = %d %v", n, err)
	}
	fi.installErr = selfupdate.ErrSignature
	fi.dir = t.TempDir() // production gives every attempt a fresh private dir
	body, ct = updMultipart(t, updStdParts())
	postUpdUpload(api, body, ct, &mgrUser)
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'update_file_refused'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit update_file_refused = %d %v", n, err)
	}
}

func TestUpdateUpload_ErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		availErr   error
		beginErr   error
		installErr error
		status     int
		code       string
		text       string
	}{
		{"not armed", selfupdate.ErrFileNotArmed, nil, nil, 409, "not_armed", updEn("settings.update.file.error.not_armed")},
		{"not armed wraps signature", fmt.Errorf("%w: %w", selfupdate.ErrFileNotArmed, selfupdate.ErrSignature), nil, nil, 409, "not_armed", ""},
		{"unsupported install", selfupdate.ErrUnsupported, nil, nil, 400, "unsupported", updEn("settings.update.file.error.unsupported_install")},
		{"unsupported file", selfupdate.ErrFileUnsupported, nil, nil, 400, "unsupported", updEn("settings.update.file.error.unsupported")},
		{"busy", nil, selfupdate.ErrBusy, nil, 409, "busy", updEn("settings.update.file.error.busy")},
		{"signature", nil, nil, fmt.Errorf("%w: bad", selfupdate.ErrSignature), 422, "signature", updEn("settings.update.file.error.signature")},
		{"checksum", nil, nil, fmt.Errorf("%w: x", selfupdate.ErrFileChecksum), 422, "checksum", updEn("settings.update.file.error.checksum")},
		{"not newer", nil, nil, selfupdate.ErrFileNotNewer, 409, "not_newer", "v9.9.9 is older than the version this till runs"},
		{"same version", nil, nil, selfupdate.ErrFileSameVersion, 409, "same_version", "already runs v9.9.9"},
		{"bad name", nil, nil, selfupdate.ErrFileBadName, 400, "bad_name", selfupdate.FileArchiveSuffix()},
		{"too large", nil, nil, selfupdate.ErrFileTooLarge, 413, "too_large", ""},
		{"no space", nil, nil, selfupdate.ErrFileNoSpace, 507, "no_space", updEn("settings.update.file.error.no_space")},
		{"cannot start", nil, nil, fmt.Errorf("update v9.9.9 could not start: %w", selfupdate.ErrCannotStart), 500, "install", "could not start on this till"},
		{"other", nil, nil, errors.New("disk on fire"), 500, "failed", updEn("settings.update.file.error.failed")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := updUploadMux(t)
			fi := &fakeFileInstall{installErr: c.installErr}
			begun := useFakeFileInstall(t, fi, c.availErr, c.beginErr)
			// availability is checked at begin in production; the fake models
			// both as a Begin failure.
			if c.availErr != nil {
				fileInstallAvailability = func() error { return c.availErr }
				beginFileInstall = func() (fileInstaller, error) { *begun++; return nil, c.availErr }
			}
			body, ct := updMultipart(t, updStdParts())
			rec := postUpdUpload(mux, body, ct, &mgrUser)
			assertUpdCode(t, rec, c.status, c.code, c.text)
			if c.beginErr == nil && c.availErr == nil && fi.closed != 1 {
				t.Errorf("Close called %d times, want 1", fi.closed)
			}
		})
	}
}

func TestUpdateUpload_BadNameAnsweredBeforeArchiveBytesAreRead(t *testing.T) {
	mux := updUploadMux(t)
	fi := &fakeFileInstall{cap: 100 << 20}
	useFakeFileInstall(t, fi, nil, nil)
	big := bytes.Repeat([]byte("A"), 3<<20)
	body, ct := updMultipart(t, []updPart{{"archive", "evil (1).tar.gz", big}})
	cr := &updCountingReader{r: body}
	rec := postUpdUpload(mux, cr, ct, &mgrUser)
	assertUpdCode(t, rec, 400, "bad_name", "")
	if cr.n > 256<<10 {
		t.Errorf("read %d bytes of a refused upload", cr.n)
	}
	if entries, _ := os.ReadDir(fi.dir); len(entries) != 0 {
		t.Errorf("files written for a bad name: %v", entries)
	}
	if fi.closed != 1 || fi.installs != 0 {
		t.Errorf("closed=%d installs=%d", fi.closed, fi.installs)
	}
}

type updCountingReader struct {
	r io.Reader
	n int
}

func (c *updCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestUpdateUpload_TooLarge(t *testing.T) {
	t.Run("archive", func(t *testing.T) {
		mux := updUploadMux(t)
		fi := &fakeFileInstall{cap: 1000}
		useFakeFileInstall(t, fi, nil, nil)
		parts := updStdParts()
		parts[2].body = bytes.Repeat([]byte("A"), 5000)
		body, ct := updMultipart(t, parts)
		assertUpdCode(t, postUpdUpload(mux, body, ct, &mgrUser), 413, "too_large", "")
		if fi.closed != 1 || fi.installs != 0 {
			t.Errorf("closed=%d installs=%d", fi.closed, fi.installs)
		}
	})
	t.Run("checksums", func(t *testing.T) {
		mux := updUploadMux(t)
		fi := &fakeFileInstall{}
		useFakeFileInstall(t, fi, nil, nil)
		parts := updStdParts()
		parts[0].body = bytes.Repeat([]byte("A"), int(selfupdate.MaxFileChecksumsBytes)+10)
		body, ct := updMultipart(t, parts)
		assertUpdCode(t, postUpdUpload(mux, body, ct, &mgrUser), 413, "too_large", "")
		if fi.closed != 1 {
			t.Errorf("closed=%d", fi.closed)
		}
	})
	t.Run("no room", func(t *testing.T) {
		mux := updUploadMux(t)
		fi := &fakeFileInstall{cap: -1}
		useFakeFileInstall(t, fi, nil, nil)
		fi.cap = -1
		body, ct := updMultipart(t, updStdParts())
		assertUpdCode(t, postUpdUpload(mux, body, ct, &mgrUser), 507, "no_space", "")
		if fi.closed != 1 {
			t.Errorf("closed=%d", fi.closed)
		}
	})
}

func TestUpdateUpload_MissingAndSwappedFiles(t *testing.T) {
	drop := func(i int) []updPart {
		p := updStdParts()
		return append(p[:i:i], p[i+1:]...)
	}
	swapped := updStdParts()
	swapped[0].filename, swapped[1].filename = "checksums.txt.sig", "checksums.txt"
	emptySig := updStdParts()
	emptySig[1].body = nil
	upperSig := updStdParts()
	upperSig[1].filename = "CHECKSUMS.TXT.SIG"
	renamedSums := updStdParts()
	renamedSums[0].filename = "checksums (1).txt"
	cases := []struct {
		name  string
		parts []updPart
		code  string
		ok    bool
	}{
		{"no checksums", drop(0), "missing_file", false},
		{"no signature", drop(1), "missing_file", false},
		{"no archive", drop(2), "missing_file", false},
		{"nothing", nil, "missing_file", false},
		{"empty signature", emptySig, "missing_file", false},
		{"slots swapped", swapped, "missing_file", false},
		{"upper-case .SIG accepted", upperSig, "", true},
		{"browser-renamed checksums accepted", renamedSums, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := updUploadMux(t)
			fi := &fakeFileInstall{}
			useFakeFileInstall(t, fi, nil, nil)
			body, ct := updMultipart(t, c.parts)
			rec := postUpdUpload(mux, body, ct, &mgrUser)
			if c.ok {
				if rec.Code != 200 {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
				return
			}
			assertUpdCode(t, rec, 400, c.code, "")
			if fi.installs != 0 || fi.closed != 1 {
				t.Errorf("installs=%d closed=%d", fi.installs, fi.closed)
			}
		})
	}
}

func TestUpdateUpload_RepeatedPartIgnored(t *testing.T) {
	mux := updUploadMux(t)
	fi := &fakeFileInstall{}
	useFakeFileInstall(t, fi, nil, nil)
	parts := append(updStdParts(), updPart{"archive", fileTestArchive, []byte("SECOND")})
	body, ct := updMultipart(t, parts)
	if rec := postUpdUpload(mux, body, ct, &mgrUser); rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fi.archiveLen != len("ARCHIVE-BYTES") {
		t.Errorf("a repeated part replaced the first one (len %d)", fi.archiveLen)
	}
}

func TestUpdateUpload_DemoModeDenied(t *testing.T) {
	if !demoDeniedRoutes["POST /api/update/upload"] {
		t.Fatal("POST /api/update/upload must be in demoDeniedRoutes (ADR-0113)")
	}
}

func TestUpdateUpload_NotArmedNeverBegins(t *testing.T) {
	mux := updUploadMux(t)
	fi := &fakeFileInstall{}
	begun := useFakeFileInstall(t, fi, selfupdate.ErrFileNotArmed, nil)
	beginFileInstall = func() (fileInstaller, error) { *begun++; return nil, selfupdate.ErrFileNotArmed }
	body, ct := updMultipart(t, updStdParts())
	assertUpdCode(t, postUpdUpload(mux, body, ct, &mgrUser), 409, "not_armed", updEn("settings.update.file.error.not_armed"))
	if entries, _ := os.ReadDir(fi.dir); len(entries) != 0 {
		t.Error("files written for an unarmed till")
	}
}

// Settings render: manager sees the form (or the reason); cashier sees nothing.
func TestSettingsFileUpdateCard(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, _ := newFullAuthDeps(t)
	fi := &fakeFileInstall{}
	useFakeFileInstall(t, fi, nil, nil)

	body := renderSettingsAs(t, mux, mgrUser, false)
	for _, want := range []string{`id="settings-update-file"`, `hx-post="/api/update/upload"`, `name="checksums"`, `name="signature"`, `name="archive"`, `update-upload-name`} {
		if !strings.Contains(body, want) {
			t.Errorf("available: missing %q", want)
		}
	}
	if strings.Contains(body, "accept=") && strings.Contains(body[strings.Index(body, `id="settings-update-file"`):], `name="archive" accept`) {
		t.Error("file inputs must not set accept=")
	}

	for reason, err := range map[string]error{
		"not_armed":           selfupdate.ErrFileNotArmed,
		"unsupported":         selfupdate.ErrFileUnsupported,
		"unsupported_install": selfupdate.ErrUnsupported,
	} {
		fileInstallAvailability = func() error { return err }
		body := renderSettingsAs(t, mux, mgrUser, false)
		if !strings.Contains(body, `data-reason="`+reason+`"`) {
			t.Errorf("%s: reason missing", reason)
		}
		if strings.Contains(body, `hx-post="/api/update/upload"`) {
			t.Errorf("%s: form still offered", reason)
		}
	}

	fileInstallAvailability = func() error { return nil }
	// The whole page is manager-gated, so a cashier never sees the section;
	// the server refusal is TestUpdateUpload_CashierRefusedBeforeAnythingHappens.
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(req, cashUser))
	if strings.Contains(rec.Body.String(), "settings-update-file") {
		t.Error("cashier sees the install-from-file section")
	}
}
