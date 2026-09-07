package discovery

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
)

// hpRealIPPFormats is the verbatim document-format-supported list the product
// owner's HP OfficeJet Pro 9020 returned to a real IPP Get-Printer-Attributes
// request on 2026-09-05, read off the wire at 192.168.1.245:631.
//
// Note what is in it: application/octet-stream. IPP printers list that as
// "send me anything and I will sniff it", and virtually all of them do — so
// the octet-stream test that correctly reads an mDNS pdl= TXT (where this
// same HP does NOT list it) says the opposite of the truth when applied to an
// IPP format list. Measuring the real device is what caught that; the first
// draft of this guard would have shipped a rule that let the HP straight
// through.
var hpRealIPPFormats = []string{
	"application/vnd.hp-PCL",
	"image/jpeg",
	"image/urf",
	"image/pwg-raster",
	"application/PCLm",
	"application/octet-stream",
}

// ippResponse encodes a minimal IPP response body carrying one
// document-format-supported attribute with the given values, in the same
// shape a real printer answers with: the first value names the attribute,
// every later value repeats the tag with a zero-length name.
func ippResponse(formats []string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x01, 0x01})             // version 1.1
	b.Write([]byte{0x00, 0x00})             // status-code: successful-ok
	b.Write([]byte{0x00, 0x00, 0x00, 0x01}) // request-id
	b.WriteByte(0x04)                       // printer-attributes-tag
	for i, f := range formats {
		name := "document-format-supported"
		if i > 0 {
			name = "" // additional value of the attribute above
		}
		b.WriteByte(0x49) // mimeMediaType
		_ = binary.Write(&b, binary.BigEndian, uint16(len(name)))
		b.WriteString(name)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(f)))
		b.WriteString(f)
	}
	b.WriteByte(0x03) // end-of-attributes-tag
	return b.Bytes()
}

// ippErrorResponse is what a printer answers when the resource path is wrong:
// a well-formed IPP response whose status-code is client-error-not-found
// (0x0406), carrying no attributes.
func ippErrorResponse() []byte {
	var b bytes.Buffer
	b.Write([]byte{0x01, 0x01})
	b.Write([]byte{0x04, 0x06})
	b.Write([]byte{0x00, 0x00, 0x00, 0x01})
	b.WriteByte(0x03)
	return b.Bytes()
}

// ippHTTPReply wraps an IPP body in the HTTP/1.1 response a printer sends.
func ippHTTPReply(body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("HTTP/1.1 200 OK\r\n")
	b.WriteString("Content-Type: application/ipp\r\n")
	b.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n")
	b.WriteString("Connection: close\r\n\r\n")
	b.Write(body)
	return b.Bytes()
}

// ---------------------------------------------------------------------------
// The request must be a read, not a print job.
// ---------------------------------------------------------------------------

func TestIPPRequest_IsAGetPrinterAttributesQuery(t *testing.T) {
	req := ippGetPrinterAttributes("192.168.1.245", "/ipp/print")

	if len(req) < 8 {
		t.Fatalf("request too short: %d bytes", len(req))
	}
	if got := binary.BigEndian.Uint16(req[2:4]); got != ippOpGetPrinterAttributes {
		t.Errorf("operation-id = %#04x, want %#04x (Get-Printer-Attributes)", got, ippOpGetPrinterAttributes)
	}
	// Whatever else changes, this must stay an attributes query: a
	// Print-Job (0x0002) here would put paper through every printer on the
	// shop's LAN.
	if !bytes.Contains(req, []byte("document-format-supported")) {
		t.Error("request does not ask for document-format-supported")
	}
	if !bytes.Contains(req, []byte("ipp://192.168.1.245:631/ipp/print")) {
		t.Errorf("printer-uri missing or wrong in %q", req)
	}
}

func TestParseIPPFormats_ReadsTheRealHPReply(t *testing.T) {
	got := parseIPPFormats(ippResponse(hpRealIPPFormats))
	if len(got) != len(hpRealIPPFormats) {
		t.Fatalf("parsed %d formats %q, want %d", len(got), got, len(hpRealIPPFormats))
	}
	for i, want := range hpRealIPPFormats {
		if got[i] != want {
			t.Errorf("format[%d] = %q, want %q", i, got[i], want)
		}
	}
}

func TestNamesPageDescriptionLanguage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		formats []string
		want    bool
	}{
		{"the real HP list", hpRealIPPFormats, true},
		// Order is not signal, and a table where every PDL happens to come
		// first cannot tell you that. An earlier draft returned on the
		// first non-raw entry, so a list that opened with octet-stream and
		// named PCL afterwards was read as raw-only — the exact shape a
		// device advertising on the raw-datastream service produces.
		{"raw first, then a PDL", []string{"application/octet-stream", "application/vnd.hp-PCL"}, true},
		// The cases that must NOT be excluded: a device that accepts raw
		// bytes and names no page description language. Hiding the shop's
		// only receipt printer is a worse bug than the one this guard fixes
		// (ut-docs#1606), and text/plain, vnd.cups-raw and vendor raw types
		// are in real receipt printers' lists.
		{"raw only", []string{"application/octet-stream"}, false},
		{"raw plus plain text", []string{"application/octet-stream", "text/plain"}, false},
		{"cups raw", []string{"application/vnd.cups-raw", "application/octet-stream"}, false},
		{"vendor raw", []string{"application/vnd.epson.escp"}, false},
		{"raw plus an explicit escpos claim", []string{"application/octet-stream", "application/vnd.escpos"}, false},
		{"empty list", nil, false},
		{"pdf", []string{"application/pdf", "application/octet-stream"}, true},
		{"case and spacing are not signal", []string{"  Application/PDF  "}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := namesPageDescriptionLanguage(tc.formats); got != tc.want {
				t.Errorf("namesPageDescriptionLanguage(%q) = %v, want %v", tc.formats, got, tc.want)
			}
		})
	}
}

// TestDeclaresRawPrinting covers the exemption that lets an advertisement
// override the IPP guard. It has to be an UNAMBIGUOUS raw claim: the first
// draft exempted anything nonESCPOSPDL failed to reject, which let an office
// printer advertising octet-stream alongside PCL bypass the guard and get
// written to.
func TestDeclaresRawPrinting(t *testing.T) {
	for _, tc := range []struct {
		name string
		pdl  string
		want bool
	}{
		{"raw only", "application/octet-stream", true},
		{"explicit escpos", "application/vnd.escpos", true},
		{"raw alongside a PDL is not a raw claim", "application/octet-stream,application/vnd.hp-PCL", false},
		{"the real HP advertisement", hpRealPDL, false},
		{"nothing advertised", "", false},
		{"raw plus plain text", "application/octet-stream,text/plain", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaresRawPrinting(tc.pdl); got != tc.want {
				t.Errorf("declaresRawPrinting(%q) = %v, want %v", tc.pdl, got, tc.want)
			}
		})
	}
}

// TestParseIPPFormats_ReservedDelimiterTag: RFC 8010 §3.5.1 reserves tags
// 0x06-0x0F for future delimiters. Reading one as a value tag misparses
// everything after it and truncates the list — and a shorter list is the
// permissive direction, so this is a safety property, not tidiness.
func TestParseIPPFormats_ReservedDelimiterTag(t *testing.T) {
	body := ippResponse([]string{"application/vnd.hp-PCL"})
	// Splice a reserved delimiter tag in front of the attribute group.
	spliced := append(append([]byte{}, body[:8]...), append([]byte{0x07}, body[8:]...)...)
	got := parseIPPFormats(spliced)
	if len(got) != 1 || got[0] != "application/vnd.hp-PCL" {
		t.Errorf("parsed %q across a reserved delimiter tag, want the one format", got)
	}
}

// ---------------------------------------------------------------------------
// officePrinterOverIPP: the decision, over the same dial seam everything else
// in this package uses.
// ---------------------------------------------------------------------------

func TestOfficePrinterOverIPP(t *testing.T) {
	const host = "192.168.1.245"
	ippAddr := net.JoinHostPort(host, "631")

	t.Run("631 closed is not a declaration", func(t *testing.T) {
		// The product owner's thermal printer: :80 and :9100 only, no IPP,
		// no mDNS. It must stay probeable, or discovery finds nothing.
		rec := stubDial(t, map[string]device{"192.168.1.111:9100": {reply: []byte{0x16}}})
		if officePrinterOverIPP(context.Background(), "192.168.1.111") {
			t.Error("a host with no IPP port was treated as an office printer")
		}
		for _, a := range rec.dialledAddrs() {
			if strings.HasSuffix(a, ":9100") {
				t.Errorf("the IPP check dialled the raw print port %s", a)
			}
		}
	})

	t.Run("declares PCL over IPP", func(t *testing.T) {
		stubDial(t, map[string]device{ippAddr: {reply: ippHTTPReply(ippResponse(hpRealIPPFormats))}})
		if !officePrinterOverIPP(context.Background(), host) {
			t.Error("the HP's own IPP answer names PCL, PCLm, URF and PWG-raster and was not recognised")
		}
	})

	t.Run("raw-only IPP printer is left alone", func(t *testing.T) {
		stubDial(t, map[string]device{ippAddr: {reply: ippHTTPReply(ippResponse([]string{"application/octet-stream"}))}})
		if officePrinterOverIPP(context.Background(), host) {
			t.Error("a printer that declares only raw bytes must stay probeable")
		}
	})

	t.Run("raw-only IPP printer with plain text is left alone", func(t *testing.T) {
		stubDial(t, map[string]device{ippAddr: {reply: ippHTTPReply(ippResponse([]string{"application/octet-stream", "text/plain"}))}})
		if officePrinterOverIPP(context.Background(), host) {
			t.Error("text/plain is in every real format list and must not exclude a receipt printer")
		}
	})

	t.Run("something on 631 that will not answer IPP", func(t *testing.T) {
		// Conservative on purpose: a device running an unidentifiable
		// service on the IPP port is not a cheap ESC/POS box, and the cost
		// of being wrong in this direction is paper.
		stubDial(t, map[string]device{ippAddr: {}})
		if !officePrinterOverIPP(context.Background(), host) {
			t.Error("an unidentifiable service on :631 should be excluded, not written to")
		}
	})

	// HTTP 200 is not the same as a successful IPP response, and the gap
	// between the two is not hypothetical: /ipp/print is not universal, so
	// asking the wrong resource path gets 200 OK carrying
	// client-error-not-found and no attributes. Read as a format list, that
	// is empty — i.e. "names no page description language", i.e. permission
	// to write.
	t.Run("200 OK carrying an IPP error status", func(t *testing.T) {
		stubDial(t, map[string]device{ippAddr: {reply: ippHTTPReply(ippErrorResponse())}})
		if !officePrinterOverIPP(context.Background(), host) {
			t.Error("an IPP error response was read as a declaration of no page description language")
		}
	})

	t.Run("200 OK carrying something that is not IPP at all", func(t *testing.T) {
		stubDial(t, map[string]device{ippAddr: {reply: []byte("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: 13\r\nConnection: close\r\n\r\n<html>hi</ht")}})
		if !officePrinterOverIPP(context.Background(), host) {
			t.Error("an HTML page on :631 was read as a declaration of no page description language")
		}
	})

	// The fail-closed branches above must not swallow the one case that
	// matters most: no IPP service at all is silence, and silence never
	// excludes.
	t.Run("every path refused means no IPP service", func(t *testing.T) {
		stubDial(t, map[string]device{})
		if officePrinterOverIPP(context.Background(), "192.168.1.111") {
			t.Error("a host with nothing on :631 was treated as an office printer")
		}
	})
}
