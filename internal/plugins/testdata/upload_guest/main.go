//go:build wasip1

// Test guest for the plugin view upload host functions (ADR-0121 §3/§7,
// ut-docs#3793). Reads a ui.action.ask-shaped event from stdin whose
// payload carries upload_handles — opaque tokens, never the file bytes —
// and pulls the first upload through upload_open / upload_read /
// upload_close with a buffer smaller than the file, hashing as it goes.
// Writes a JSON answer to stdout.
package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/universaltill/universal-till/sdk/plugin"
)

const readBufSize = 16 << 10

// code maps an SDK result back to the host's numeric return: 0 for
// success, the negative host code for a *plugin.Error.
func code(err error) int32 {
	var e *plugin.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return -3
	}
	return 0
}

type uploadPayload struct {
	Mode    string `json:"mode"`
	Uploads []struct {
		Field  string `json:"field"`
		Handle string `json:"handle"`
	} `json:"upload_handles"`
}

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func handle(ev plugin.Event) (any, error) {
	var p uploadPayload
	_ = ev.Decode(&p)
	if len(p.Uploads) == 0 {
		return []byte(`{"error":"no upload"}` + "\n"), nil
	}
	tok := p.Uploads[0].Handle

	switch p.Mode {
	case "probe":
		// Only report what upload_open answers (a foreign or malformed
		// token), never read or close. Success reports 0: the SDK keeps
		// the handle number private.
		_, err := plugin.UploadOpen(tok)
		return []byte(fmt.Sprintf(`{"open":%d}`+"\n", code(err))), nil
	case "leave_open":
		u, err := plugin.UploadOpen(tok)
		n := 0
		if err == nil {
			var rerr error
			n, rerr = u.Read(make([]byte, 4))
			if rerr != nil && !errors.Is(rerr, io.EOF) {
				n = int(code(rerr))
			}
		} else {
			n = int(code(err))
		}
		return []byte(fmt.Sprintf(`{"open_ok":%t,"read":%d}`+"\n", err == nil, n)), nil
	}

	u, err := plugin.UploadOpen(tok)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":"upload_open %d"}`+"\n", code(err))), nil
	}
	hasher := sha256.New()
	buf := make([]byte, readBufSize)
	total, reads := 0, 0
	for {
		n, err := u.Read(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return []byte(fmt.Sprintf(`{"error":"upload_read %d"}`+"\n", code(err))), nil
		}
		hasher.Write(buf[:n])
		total += n
		reads++
	}
	cc := code(u.Close())
	cc2 := code(u.Close())
	_, reopenErr := plugin.UploadOpen(tok) // the token is consumed by close
	return []byte(fmt.Sprintf(`{"sha256":"%x","bytes":%d,"reads":%d,"close":%d,"close_again":%d,"reopen":%d}`+"\n",
		hasher.Sum(nil), total, reads, cc, cc2, code(reopenErr))), nil
}
