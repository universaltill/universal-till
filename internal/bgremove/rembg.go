package bgremove

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// cutoutTimeout bounds one background-removal call — the same 90 s as
// identifyTimeout: CPU inference on modest shop hardware is slow.
const cutoutTimeout = 90 * time.Second

// maxCutoutBytes caps the rembg answer. Larger is an error, never a
// truncated (and so corrupt) image.
const maxCutoutBytes = 20 << 20

// pngSignature is the 8-byte PNG file header.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// rembgCutter talks to the shop's own rembg server (`rembg s`,
// github.com/danielgatis/rembg, MIT): POST <endpoint>/api/remove, multipart
// form with the photo in `file` and the model in `model` — the field names
// of rembg/commands/s_command.py's post_index (`file: bytes = File(...)`,
// `model: str = Form(default="bria-rmbg")`). The model is ALWAYS sent, so
// rembg's bria-rmbg default (commercial licence needed) never runs by
// accident.
type rembgCutter struct {
	endpoint string
	model    string
	timeout  time.Duration
	client   *http.Client
}

func newRembgCutter(endpoint, model string) *rembgCutter {
	client := netaccess.NewClient(0) // the deadline comes from ctx (timeout below)
	// The endpoint is shop-entered: never follow a redirect elsewhere; a 3xx
	// is answered as an error below.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &rembgCutter{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		model:    model,
		timeout:  cutoutTimeout,
		client:   client,
	}
}

func (c *rembgCutter) removeBackground(ctx context.Context, photo []byte, mediaType string) (image.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, contentType, err := rembgForm(photo, mediaType, c.model)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/remove", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "image/png")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("background removal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("background removal: service answered a redirect (%d), not followed", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("background removal: service answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCutoutBytes+1))
	if err != nil {
		return nil, fmt.Errorf("background removal: read answer: %w", err)
	}
	if len(raw) > maxCutoutBytes {
		return nil, fmt.Errorf("background removal: answer exceeds %d MB", maxCutoutBytes>>20)
	}
	if !bytes.HasPrefix(raw, pngSignature) {
		return nil, errors.New("background removal: answer is not a PNG")
	}
	img, _, err := imaging.DecodeBoundedFormats(raw, imaging.MaxPixels, map[string]bool{"png": true})
	if err != nil {
		return nil, fmt.Errorf("background removal: answer is not a usable PNG: %w", err)
	}
	if !hasTransparency(img) {
		return nil, errors.New("background removal: answer has no alpha channel (opaque image)")
	}
	return img, nil
}

// rembgForm builds the multipart body: `file` (the photo, with its media
// type) and `model`.
func rembgForm(photo []byte, mediaType, model string) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// mediaType goes into a MIME part header verbatim, so only the two
	// known values pass; anything else (CR/LF included) becomes
	// application/octet-stream and can't inject parts or fields.
	filename := "photo.bin"
	switch mediaType {
	case "image/png":
		filename = "photo.png"
	case "image/jpeg":
		filename = "photo.jpg"
	default:
		mediaType = "application/octet-stream"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", mediaType)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(photo); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("model", model); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// hasTransparency reports whether any pixel is not fully opaque. A cutout
// with no transparent pixel removed nothing (or the service ignored the
// request), so it is treated as a bad answer.
func hasTransparency(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return true
			}
		}
	}
	return false
}
