package pages

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// Plugin view uploads (ADR-0121 §7, ut-docs#3793; format: ut-docs
// reference/plugin-views.md). A form with a file field posts
// multipart/form-data. The body is streamed part by part
// (r.MultipartReader — never ParseMultipartForm/ReadForm, which would
// spool a second copy; see import_dispatch.go): text parts go through
// pluginview.DecodeForm unchanged, each file part is streamed to its own
// temp file, capped at the entry's config.upload_max_mb, and handed to the
// entry's plugin as a token (plugins.StageUpload) it reads with
// upload_open/upload_read. The temp file never outlives the ask — or the
// job it starts — that received it (plugins.ReleaseUploads).

const (
	// pluginViewMaxUploads: files per post.
	pluginViewMaxUploads = 4
	// pluginViewMultipartSlack bounds the multipart framing (boundaries,
	// part headers) on top of the form's own and the files' bytes.
	pluginViewMultipartSlack = 64 << 10
	// pluginUploadMaxFilenameBytes caps the filename forwarded to the plugin.
	pluginUploadMaxFilenameBytes = 255
	// pluginUploadSniffBytes: what http.DetectContentType reads.
	pluginUploadSniffBytes = 512
)

// pluginUpload is one staged file as ui.action.ask's upload_handles
// carries it.
type pluginUpload struct {
	Field       string `json:"field"`
	Handle      string `json:"handle"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

var (
	// errPluginUploadsBusy: the plugin already holds its maximum of staged
	// uploads (earlier asks or jobs still running).
	errPluginUploadsBusy = errors.New("plugin already holds its maximum of staged uploads")
	// errPluginUploadStage: the till could not write the temp file.
	errPluginUploadStage = errors.New("could not stage the upload")
)

// pluginViewFormFailStatus is the status a refused post answers with.
func pluginViewFormFailStatus(err error) int {
	switch {
	case errors.Is(err, errPluginUploadsBusy):
		return http.StatusTooManyRequests
	case errors.Is(err, errPluginUploadStage):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

func uploadTokens(ups []pluginUpload) []string {
	toks := make([]string, len(ups))
	for i, u := range ups {
		toks[i] = u.Handle
	}
	return toks
}

// isMultipartForm reports whether the post is multipart/form-data.
func isMultipartForm(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "multipart/form-data"
}

// partFileName returns the part's filename and whether it is a file part
// at all (its Content-Disposition carries a filename parameter, even an
// empty one — a browser's "no file chosen").
func partFileName(header string) (string, bool) {
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return "", false
	}
	name, ok := params["filename"]
	return name, ok
}

// uploadFilename is the base name (either path separator) of a submitted
// filename, control and format characters stripped, at most 255 bytes of valid UTF-8.
func uploadFilename(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		// Format characters too (U+202E right-to-left override, zero-width
		// joiners): they would make the label read as another name.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	for len(s) > pluginUploadMaxFilenameBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// readPluginViewMultipart reads a multipart plugin view post for entry.
// On a nil error the caller owns the returned uploads (it releases them,
// or hands them to a job); on an error nothing stays staged.
func readPluginViewMultipart(w http.ResponseWriter, r *http.Request, entry data.PageEntryRow) (pluginview.Submission, []pluginUpload, error) {
	maxBytes := int64(entry.UploadMaxMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, pluginview.MaxFormBytes+pluginViewMaxUploads*maxBytes+pluginViewMultipartSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		return pluginview.Submission{}, nil, fmt.Errorf("read multipart: %w", err)
	}
	var ups []pluginUpload
	done := false
	defer func() {
		if !done {
			plugins.ReleaseUploads(entry.PluginID, uploadTokens(ups))
		}
	}()
	vals := url.Values{}
	var formBytes int64
	var invalid []string
	files := 0
	seenFile := map[string]bool{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return pluginview.Submission{}, nil, fmt.Errorf("read multipart: %w", err)
		}
		name := part.FormName()
		fileName, isFile := partFileName(part.Header.Get("Content-Disposition"))
		if !isFile {
			v, err := io.ReadAll(io.LimitReader(part, pluginview.MaxFormBytes-formBytes+1))
			_ = part.Close()
			if err != nil {
				return pluginview.Submission{}, nil, fmt.Errorf("read form field: %w", err)
			}
			formBytes += int64(len(name) + len(v))
			if formBytes > pluginview.MaxFormBytes {
				return pluginview.Submission{}, nil, fmt.Errorf("form exceeds %d bytes", pluginview.MaxFormBytes)
			}
			vals.Add(name, string(v))
			continue
		}
		up, staged, oversize, err := stagePluginUploadPart(part, entry, name, fileName, maxBytes, &files, seenFile)
		_ = part.Close()
		switch {
		case err != nil:
			return pluginview.Submission{}, nil, err
		case oversize:
			invalid = append(invalid, name)
		case staged:
			ups = append(ups, up)
		}
	}
	c := httpx.ActiveCurrency()
	sub, err := pluginview.DecodeForm(vals, c.Decimals, c.Code)
	if err != nil {
		return sub, nil, err
	}
	sub.Invalid = append(sub.Invalid, invalid...)
	sort.Strings(sub.Invalid)
	sort.Slice(ups, func(i, j int) bool { return ups[i].Field < ups[j].Field })
	if ups == nil {
		ups = []pluginUpload{}
	}
	done = true
	return sub, ups, nil
}

// stagePluginUploadPart streams one file part. staged: up is now in the
// plugin's registry. oversize: the file was over the entry's cap and is
// already removed. Neither: no file was chosen (an empty, nameless part).
func stagePluginUploadPart(part io.Reader, entry data.PageEntryRow, name, fileName string, maxBytes int64, files *int, seen map[string]bool) (up pluginUpload, staged, oversize bool, err error) {
	if !pluginview.ValidFieldName(name) {
		return up, false, false, fmt.Errorf("file field %q is not a field name", name)
	}
	head := make([]byte, pluginUploadSniffBytes)
	n, rerr := io.ReadFull(part, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return up, false, false, fmt.Errorf("read upload: %w", rerr)
	}
	head = head[:n]
	if n == 0 && fileName == "" {
		return up, false, false, nil // no file chosen
	}
	if maxBytes == 0 {
		return up, false, false, fmt.Errorf("file field %q posted to an entry without config.upload_max_mb", name)
	}
	if seen[name] {
		return up, false, false, fmt.Errorf("file field %q posted twice", name)
	}
	seen[name] = true
	*files++
	if *files > pluginViewMaxUploads {
		return up, false, false, fmt.Errorf("more than %d files in one post", pluginViewMaxUploads)
	}
	tmp, err := os.CreateTemp("", "ut-view-upload-*.upload")
	if err != nil {
		logging.L().Errorf("plugin view upload %s: create temp file: %v", entry.PluginID, err)
		return up, false, false, errPluginUploadStage
	}
	path := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	written, werr := tmp.Write(head)
	var copied int64
	if werr == nil {
		copied, werr = io.Copy(tmp, io.LimitReader(part, maxBytes+1-int64(n)))
	}
	cerr := tmp.Close()
	size := int64(written) + copied
	if werr != nil || cerr != nil {
		var tooBig *http.MaxBytesError
		if errors.As(werr, &tooBig) {
			return up, false, false, fmt.Errorf("post body too large: %w", werr)
		}
		logging.L().Errorf("plugin view upload %s: stage: write=%v close=%v", entry.PluginID, werr, cerr)
		return up, false, false, errPluginUploadStage
	}
	if size > maxBytes {
		return up, false, true, nil
	}
	tok, err := plugins.StageUpload(entry.PluginID, path)
	if err != nil {
		return up, false, false, errPluginUploadsBusy
	}
	keep = true
	return pluginUpload{
		Field:       name,
		Handle:      tok,
		Filename:    uploadFilename(fileName),
		Size:        size,
		ContentType: http.DetectContentType(head),
	}, true, false, nil
}
