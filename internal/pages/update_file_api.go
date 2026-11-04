package pages

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
)

// ut-docs#5438: "Install from a file" (Settings -> Software update). A till
// with no internet installs a release brought on a USB stick: the archive,
// checksums.txt and checksums.txt.sig. The handler only moves bytes into a
// private directory and maps errors; every trust decision is made by
// selfupdate.FileInstall (the same release-signature verifier as the online
// path, ADR-0178 and its 2026-11-04 amendment).

// fileInstaller is selfupdate.FileInstall as the handler uses it (a test seam).
type fileInstaller interface {
	Dir() string
	ArchiveCap() int64
	Install(ctx context.Context, in selfupdate.FileSet) (selfupdate.Installed, error)
	Close()
}

// Test seams. Production code never changes these.
var (
	fileInstallAvailability = selfupdate.FileInstallAvailability
	beginFileInstall        = func() (fileInstaller, error) { return selfupdate.BeginFileInstall() }
)

// updateUploadFormOverhead is the multipart framing allowed on top of the parts.
const updateUploadFormOverhead int64 = 1 << 20

// updateUploadFail is one refusal: a stable code (tests and the e2e assert on
// it, not on translated text), the HTTP status, the i18n key and its args.
type updateUploadFail struct {
	code   string
	status int
	key    string
	args   []any
}

func uploadFail(code string, status int, key string, args ...any) updateUploadFail {
	return updateUploadFail{code: code, status: status, key: "settings.update.file.error." + key, args: args}
}

// respondUpdateUpload writes a refusal as an inline alert fragment (the
// precedent, POST /api/backup/upload, answers HTML too). Package-level so a
// test drives the real function (ut-docs#387).
func respondUpdateUpload(w http.ResponseWriter, locale string, f updateUploadFail) {
	msg := httpx.T(locale, f.key)
	if len(f.args) > 0 {
		msg = fmt.Sprintf(msg, f.args...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(f.status)
	fmt.Fprintf(w, `<p role="alert" dir="auto" data-testid="update-upload-error" data-code="%s"><span class="row-warn-icon" aria-hidden="true">⚠</span>%s</p>`,
		template.HTMLEscapeString(f.code), template.HTMLEscapeString(msg))
}

// classifyUpdateUpload maps an error from Begin/Install/the upload to its
// refusal. archiveName only words the version in not_newer/same_version/install.
func classifyUpdateUpload(err error, archiveName string, limit int64) updateUploadFail {
	v := "v" + selfupdate.FileArchiveNameVersion(archiveName)
	var maxErr *http.MaxBytesError
	switch {
	case errors.Is(err, selfupdate.ErrFileNotArmed): // before ErrSignature: an unusable stamp wraps both
		return uploadFail("not_armed", http.StatusConflict, "not_armed")
	case errors.Is(err, selfupdate.ErrUnsupported):
		return uploadFail("unsupported", http.StatusBadRequest, "unsupported_install")
	case errors.Is(err, selfupdate.ErrFileUnsupported):
		return uploadFail("unsupported", http.StatusBadRequest, "unsupported")
	case errors.Is(err, selfupdate.ErrFileBadName):
		return uploadFail("bad_name", http.StatusBadRequest, "bad_name", selfupdate.FileArchiveSuffix())
	case errors.Is(err, selfupdate.ErrFileNotNewer):
		return uploadFail("not_newer", http.StatusConflict, "not_newer", v, "v"+buildinfo.Version)
	case errors.Is(err, selfupdate.ErrFileSameVersion):
		return uploadFail("same_version", http.StatusConflict, "same_version", v)
	case errors.Is(err, selfupdate.ErrSignature):
		return uploadFail("signature", http.StatusUnprocessableEntity, "signature")
	case errors.Is(err, selfupdate.ErrFileChecksum):
		return uploadFail("checksum", http.StatusUnprocessableEntity, "checksum")
	case errors.Is(err, selfupdate.ErrFileTooLarge) || errors.As(err, &maxErr):
		return uploadFail("too_large", http.StatusRequestEntityTooLarge, "too_large", formatBackupBytes(limit))
	case errors.Is(err, selfupdate.ErrFileNoSpace):
		return uploadFail("no_space", http.StatusInsufficientStorage, "no_space")
	case errors.Is(err, selfupdate.ErrBusy):
		return uploadFail("busy", http.StatusConflict, "busy")
	case errors.Is(err, selfupdate.ErrCannotStart):
		return uploadFail("install", http.StatusInternalServerError, "install", v, "v"+buildinfo.Version)
	}
	return uploadFail("failed", http.StatusInternalServerError, "failed")
}

// registerUpdateFileAPI mounts POST /api/update/upload (called from
// registerUpdateAPI). Same gate as "Update now": plugin_management.
func registerUpdateFileAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	audit := func(r *http.Request, action string, payload map[string]any) {
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "update", "-", action,
			payload, time.Now().UTC().Format(time.RFC3339), "")
	}

	mux.HandleFunc("POST /api/update/upload", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		locale := httpx.ResolveLocale(w, r)
		archiveName := ""
		var limit int64
		refuse := func(err error) {
			f := classifyUpdateUpload(err, archiveName, limit)
			logging.L().Infof("[update_upload] refused %s: %v", f.code, err)
			if f.code == "signature" || f.code == "checksum" {
				audit(r, "update_file_refused", map[string]any{"code": f.code, "file": archiveName})
			}
			respondUpdateUpload(w, locale, f)
		}

		// Availability and applyMu BEFORE any body byte is read: an unarmed
		// build or a busy till never takes the upload, and no online update
		// can start between the upload and the install.
		fi, err := beginFileInstall()
		if err != nil {
			refuse(err)
			return
		}
		defer fi.Close()

		limit = fi.ArchiveCap()
		if limit <= 0 {
			refuse(selfupdate.ErrFileNoSpace)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit+selfupdate.MaxFileChecksumsBytes+selfupdate.MaxFileSigBytes+updateUploadFormOverhead)
		// MultipartReader, never ParseMultipartForm: that would spill the
		// archive to disk outside fi.Dir().
		mr, err := r.MultipartReader()
		if err != nil {
			respondUpdateUpload(w, locale, uploadFail("missing_file", http.StatusBadRequest, "missing_file"))
			return
		}
		var (
			sums, sig                  []byte
			archivePath                string
			haveSums, haveSig, haveArc bool
		)
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				if perr != io.EOF {
					refuse(perr)
					return
				}
				break
			}
			field, fname := part.FormName(), part.FileName()
			lower := strings.ToLower(fname)
			var rerr error
			switch {
			case field == "checksums" && !haveSums:
				haveSums = true
				// A signature dropped into the checksums slot is a swapped
				// slot, not a bad signature (UX R-UX5).
				if strings.HasSuffix(lower, ".sig") {
					respondUpdateUpload(w, locale, uploadFail("missing_file", http.StatusBadRequest, "missing_file"))
					return
				}
				sums, rerr = readUpToCap(part, selfupdate.MaxFileChecksumsBytes)
			case field == "signature" && !haveSig:
				haveSig = true
				if !strings.HasSuffix(lower, ".sig") {
					respondUpdateUpload(w, locale, uploadFail("missing_file", http.StatusBadRequest, "missing_file"))
					return
				}
				// releasesig polices the size (over 16 KiB is malformed).
				sig, _ = io.ReadAll(io.LimitReader(part, selfupdate.MaxFileSigBytes))
			case field == "archive" && !haveArc:
				haveArc = true
				archiveName = fname
				// The name decides before a single archive byte is read.
				if _, nerr := selfupdate.FileArchiveVersion(fname); nerr != nil {
					// No part.Close(): it would drain the archive just refused.
					refuse(nerr)
					return
				}
				archivePath = filepath.Join(fi.Dir(), fname)
				rerr = writeCapped(archivePath, part, limit)
			}
			_ = part.Close()
			if rerr != nil {
				refuse(rerr)
				return
			}
		}
		archiveOK := false
		if archivePath != "" {
			if st, serr := os.Stat(archivePath); serr == nil && st.Size() > 0 {
				archiveOK = true
			}
		}
		if len(sums) == 0 || len(sig) == 0 || !archiveOK {
			respondUpdateUpload(w, locale, uploadFail("missing_file", http.StatusBadRequest, "missing_file"))
			return
		}

		got, err := fi.Install(r.Context(), selfupdate.FileSet{
			ArchiveName: archiveName, ArchivePath: archivePath, Sums: sums, Sig: sig,
		})
		if err != nil {
			refuse(err)
			return
		}
		audit(r, "update_installed_from_file", map[string]any{
			"version": got.Version, "from": buildinfo.Version, "key_id": got.KeyID,
		})
		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"ut:update-installing":{"version":%q}}`, got.Version))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<p role="status" dir="auto" data-testid="update-upload-installing" data-version="%s">%s</p>`,
			template.HTMLEscapeString(got.Version),
			template.HTMLEscapeString(fmt.Sprintf(httpx.T(locale, "settings.update.file.installing"), "v"+got.Version)))
	})
}

// readUpToCap reads at most max bytes; one more byte is ErrFileTooLarge.
func readUpToCap(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, selfupdate.ErrFileTooLarge
	}
	return b, nil
}

// writeCapped streams r into a new private file, refusing more than max bytes.
func writeCapped(path string, r io.Reader, max int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, cerr := io.Copy(f, io.LimitReader(r, max+1))
	if err := f.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return cerr
	}
	if n > max {
		return selfupdate.ErrFileTooLarge
	}
	return nil
}

// fileUpdateView is the Settings card's data: whether the form is offered and,
// when not, the one reason shown (UX R-UX4/R-UX6). Android and iOS render
// nothing: they have their own install path in the same card.
func fileUpdateView() map[string]any {
	view := map[string]any{"suffix": selfupdate.FileArchiveSuffix(), "available": false, "hidden": false, "reason": ""}
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		view["hidden"] = true
		return view
	}
	switch err := fileInstallAvailability(); {
	case err == nil:
		view["available"] = true
	case errors.Is(err, selfupdate.ErrFileNotArmed):
		view["reason"] = "not_armed"
	case errors.Is(err, selfupdate.ErrFileUnsupported):
		view["reason"] = "unsupported"
	default:
		view["reason"] = "unsupported_install"
	}
	return view
}
