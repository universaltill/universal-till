// Package issuereport is the till-side half of ADR-0022 / spec 012: a
// manager captures a description (typed and/or spoken as a voice note, plus
// an optional screen recording) and the till's recent logs into a bundle
// saved on local disk, queued for upload to Universal Till Cloud on the
// cloudsync retry cadence (internal/cloudsync).
//
// Saving a bundle never talks to the network and always succeeds regardless
// of connectivity (same offline-first bar as the rest of this till) — upload
// is a separate, best-effort step.
package issuereport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/logging"
)

// PendingDir is where unuploaded bundles live. Overridable in tests (same
// convention as internal/pages' pluginPagesDir); production sets it from
// paths.Data("issue-reports", "pending") during Init.
var PendingDir = "./data/issue-reports/pending"

// newBundleID is overridable in tests that need a predictable directory
// name (e.g. to pre-arrange a write failure and assert the cleanup path).
var newBundleID = uuid.NewString

// Meta is the non-blob part of a captured bundle, persisted as meta.json
// alongside the recording files.
type Meta struct {
	ID   string `json:"id"`
	Note string `json:"note"`
	// Locale is the till's UI locale (e.g. "fa") at capture time, so
	// cloud-side transcription can request it from Whisper (ut-docs#397).
	// Empty when it was never captured (bundles saved before this field
	// existed, or the JS-less path) — the cloud treats that as auto-detect.
	Locale    string            `json:"locale"`
	CreatedAt time.Time         `json:"created_at"`
	Logs      []logging.Problem `json:"logs"`
}

// Bundle is one saved, not-yet-uploaded capture on disk.
type Bundle struct {
	Meta       Meta
	Dir        string
	AudioPath  string   // "" when no voice note was captured
	VideoPath  string   // "" when no screen recording was captured
	ImagePaths []string // empty when no screenshots were captured, else sorted capture order
}

// Save writes a new bundle to the pending queue. A description is required —
// either typed (note) or spoken (audio) — but not both; video and images are
// always optional. locale is the operator's UI locale at capture time
// (ut-docs#397) — empty is valid (never captured / JS-less path). images is
// written as image-0.png, image-1.png, … in call order (screenshot capture
// order, ut-docs#347).
func Save(note, locale string, audio, video []byte, images [][]byte) (string, error) {
	if strings.TrimSpace(note) == "" && len(audio) == 0 {
		return "", fmt.Errorf("issuereport: a description (typed note or voice recording) is required")
	}
	id := newBundleID()
	dir := filepath.Join(PendingDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("issuereport: create bundle dir: %w", err)
	}
	// Any failure past this point leaves a directory with no meta.json —
	// invisible to Pending() and so never reachable by Discard() either.
	// Clean it up here instead of leaking it permanently.
	if err := saveBundleFiles(dir, id, note, locale, audio, video, images); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return id, nil
}

func saveBundleFiles(dir, id, note, locale string, audio, video []byte, images [][]byte) error {
	if len(audio) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "audio.webm"), audio, 0o644); err != nil {
			return fmt.Errorf("issuereport: write audio: %w", err)
		}
	}
	if len(video) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "video.webm"), video, 0o644); err != nil {
			return fmt.Errorf("issuereport: write video: %w", err)
		}
	}
	for i, img := range images {
		name := fmt.Sprintf("image-%d.png", i)
		if err := os.WriteFile(filepath.Join(dir, name), img, 0o644); err != nil {
			return fmt.Errorf("issuereport: write %s: %w", name, err)
		}
	}
	meta := Meta{ID: id, Note: note, Locale: locale, CreatedAt: time.Now().UTC(), Logs: logging.Recent()}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("issuereport: encode meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o644); err != nil {
		return fmt.Errorf("issuereport: write meta: %w", err)
	}
	return nil
}

// Pending lists bundles waiting for upload, oldest-captured first (so a
// backlog uploads in the order it happened once connectivity returns). A
// bundle whose meta.json is missing or unreadable is skipped, not fatal —
// one corrupt directory must not block every other pending report.
func Pending() ([]Bundle, error) {
	entries, err := os.ReadDir(PendingDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("issuereport: list pending: %w", err)
	}
	var bundles []Bundle
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(PendingDir, e.Name())
		mb, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		var meta Meta
		if err := json.Unmarshal(mb, &meta); err != nil {
			continue
		}
		b := Bundle{Meta: meta, Dir: dir}
		if _, err := os.Stat(filepath.Join(dir, "audio.webm")); err == nil {
			b.AudioPath = filepath.Join(dir, "audio.webm")
		}
		if _, err := os.Stat(filepath.Join(dir, "video.webm")); err == nil {
			b.VideoPath = filepath.Join(dir, "video.webm")
		}
		if paths, err := filepath.Glob(filepath.Join(dir, "image-*.png")); err == nil && len(paths) > 0 {
			sort.Slice(paths, func(i, j int) bool { return imageIndex(paths[i]) < imageIndex(paths[j]) })
			b.ImagePaths = paths
		}
		bundles = append(bundles, b)
	}
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].Meta.CreatedAt.Before(bundles[j].Meta.CreatedAt)
	})
	return bundles, nil
}

// imageIndex extracts the numeric N out of a ".../image-N.png" path, for
// sorting screenshots back into capture order. filepath.Glob's own result
// order is lexicographic (image-1.png, image-10.png, image-2.png, …), which
// misorders any bundle with 10+ screenshots — sort on the parsed integer
// instead. A path that doesn't parse (shouldn't happen — Save is the only
// writer of these files) sorts last rather than panicking.
func imageIndex(path string) int {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".png")
	base = strings.TrimPrefix(base, "image-")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 1<<31 - 1
	}
	return n
}

// Discard removes a bundle from the pending queue — called once the cloud
// has confirmed receipt, so the till never re-uploads it.
func Discard(id string) error {
	return os.RemoveAll(filepath.Join(PendingDir, id))
}
