//go:build wasip1

package channel

import (
	"runtime"
	"unsafe"
)

// The "channel" host module. One //go:wasmimport per host function; the raw*
// shims below are the only code that touches guest pointers. fakehost.go
// defines the same shims for native builds. ut-cloud's channelhost mirror
// test checks this list against the host's exports (both directions).

//go:wasmimport channel channel_http
func chHTTP(rPtr, rLen, dstPtr, dstCap uint32) int32

//go:wasmimport channel cursor_get
func chCursorGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport channel cursor_set
func chCursorSet(kPtr, kLen, vPtr, vLen uint32) int32

//go:wasmimport channel channel_submit
func chSubmit(kPtr, kLen, bPtr, bLen, dstPtr, dstCap uint32) int32

//go:wasmimport channel outbox_next
func chOutboxNext(max, dstPtr, dstCap uint32) int32

//go:wasmimport channel outbox_ack
func chOutboxAck(idPtr, idLen, aPtr, aLen uint32) int32

//go:wasmimport channel log
func chLog(level, cPtr, cLen, fPtr, fLen uint32) int32

func ptr(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func rawHTTP(req, dst []byte) int32 {
	rp, rn := ptr(req)
	dp, dn := ptr(dst)
	n := chHTTP(rp, rn, dp, dn)
	runtime.KeepAlive(req)
	runtime.KeepAlive(dst)
	return n
}

func rawCursorGet(key, dst []byte) int32 {
	kp, kn := ptr(key)
	dp, dn := ptr(dst)
	n := chCursorGet(kp, kn, dp, dn)
	runtime.KeepAlive(key)
	runtime.KeepAlive(dst)
	return n
}

func rawCursorSet(key, val []byte) int32 {
	kp, kn := ptr(key)
	vp, vn := ptr(val)
	n := chCursorSet(kp, kn, vp, vn)
	runtime.KeepAlive(key)
	runtime.KeepAlive(val)
	return n
}

func rawSubmit(kind, batch, dst []byte) int32 {
	kp, kn := ptr(kind)
	bp, bn := ptr(batch)
	dp, dn := ptr(dst)
	n := chSubmit(kp, kn, bp, bn, dp, dn)
	runtime.KeepAlive(kind)
	runtime.KeepAlive(batch)
	runtime.KeepAlive(dst)
	return n
}

func rawOutboxNext(max int32, dst []byte) int32 {
	dp, dn := ptr(dst)
	n := chOutboxNext(uint32(max), dp, dn)
	runtime.KeepAlive(dst)
	return n
}

func rawOutboxAck(id, ack []byte) int32 {
	ip, in := ptr(id)
	ap, an := ptr(ack)
	n := chOutboxAck(ip, in, ap, an)
	runtime.KeepAlive(id)
	runtime.KeepAlive(ack)
	return n
}

func rawLog(level int32, code, fields []byte) {
	cp, cn := ptr(code)
	fp, fn := ptr(fields)
	chLog(uint32(level), cp, cn, fp, fn)
	runtime.KeepAlive(code)
	runtime.KeepAlive(fields)
}
