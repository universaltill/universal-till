//go:build wasip1

package plugin

import (
	"runtime"
	"unsafe"
)

// The "ut" host module. One //go:wasmimport per host function; the raw*
// shims below are the only code that touches guest pointers. fakehost.go
// defines the same shims for native builds, and
// scripts/ci/guard-sdk-hostfns.sh checks this list against the host's
// exports (both directions).

//go:wasmimport ut log_write
func utLogWrite(ptr, n uint32)

//go:wasmimport ut storage_get
func utStorageGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut storage_set
func utStorageSet(kPtr, kLen, vPtr, vLen uint32) int32

//go:wasmimport ut http_request
func utHTTPRequest(rPtr, rLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut settings_get
func utSettingsGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut tcp_open
func utTCPOpen(hostPtr, hostLen, port, timeoutMs uint32) int32

//go:wasmimport ut tcp_write
func utTCPWrite(h int32, ptr, n uint32) int32

//go:wasmimport ut tcp_read
func utTCPRead(h int32, dstPtr, dstCap, timeoutMs uint32) int32

//go:wasmimport ut tcp_close
func utTCPClose(h int32) int32

//go:wasmimport ut import_file_size
func utImportFileSize(h int32) int64

//go:wasmimport ut import_file_read
func utImportFileRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut import_file_close
func utImportFileClose(h int32) int32

//go:wasmimport ut upload_open
func utUploadOpen(tPtr, tLen uint32) int32

//go:wasmimport ut upload_read
func utUploadRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut upload_close
func utUploadClose(h int32) int32

//go:wasmimport ut http_open
func utHTTPOpen(rPtr, rLen uint32) int32

//go:wasmimport ut http_write
func utHTTPWrite(h int32, ptr, n uint32) int32

//go:wasmimport ut http_status
func utHTTPStatus(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_read
func utHTTPRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_close
func utHTTPClose(h int32) int32

//go:wasmimport ut view_query
func utViewQuery(nPtr, nLen, aPtr, aLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut secret_set
func utSecretSet(kPtr, kLen, vPtr, vLen uint32) int32

//go:wasmimport ut blob_put_open
func utBlobPutOpen(nPtr, nLen uint32) int32

//go:wasmimport ut blob_write
func utBlobWrite(h int32, ptr, n uint32) int32

//go:wasmimport ut blob_commit
func utBlobCommit(h int32) int32

//go:wasmimport ut blob_get_open
func utBlobGetOpen(nPtr, nLen uint32) int32

//go:wasmimport ut blob_read
func utBlobRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut blob_delete
func utBlobDelete(nPtr, nLen uint32) int32

//go:wasmimport ut blob_list
func utBlobList(dstPtr, dstCap uint32) int32

//go:wasmimport ut item_image_open
func utItemImageOpen(idPtr, idLen, rolePtr, roleLen uint32) int32

//go:wasmimport ut item_image_read
func utItemImageRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut event_publish
func utEventPublish(tPtr, tLen, pPtr, pLen uint32) int32

//go:wasmimport ut job_progress
func utJobProgress(pct, kPtr, kLen uint32) int32

//go:wasmimport ut device_id_get
func utDeviceIDGet(dstPtr, dstCap uint32) int32

//go:wasmimport ut device_local_ips_get
func utDeviceLocalIPsGet(dstPtr, dstCap uint32) int32

//go:wasmimport ut device_timezone_get
func utDeviceTimezoneGet(dstPtr, dstCap uint32) int32

// ptr is the guest address and length of b; (0, 0) for an empty slice. The
// caller keeps b alive across the host call with runtime.KeepAlive.
func ptr(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func rawLogWrite(msg []byte) {
	p, n := ptr(msg)
	utLogWrite(p, n)
	runtime.KeepAlive(msg)
}

func rawStorageGet(key, dst []byte) int32 {
	kp, kl := ptr(key)
	dp, dc := ptr(dst)
	r := utStorageGet(kp, kl, dp, dc)
	runtime.KeepAlive(key)
	runtime.KeepAlive(dst)
	return r
}

func rawStorageSet(key, val []byte) int32 {
	kp, kl := ptr(key)
	vp, vl := ptr(val)
	r := utStorageSet(kp, kl, vp, vl)
	runtime.KeepAlive(key)
	runtime.KeepAlive(val)
	return r
}

func rawHTTPRequest(req, dst []byte) int32 {
	rp, rl := ptr(req)
	dp, dc := ptr(dst)
	r := utHTTPRequest(rp, rl, dp, dc)
	runtime.KeepAlive(req)
	runtime.KeepAlive(dst)
	return r
}

func rawSettingsGet(key, dst []byte) int32 {
	kp, kl := ptr(key)
	dp, dc := ptr(dst)
	r := utSettingsGet(kp, kl, dp, dc)
	runtime.KeepAlive(key)
	runtime.KeepAlive(dst)
	return r
}

func rawTCPOpen(host []byte, port, timeoutMs uint32) int32 {
	hp, hl := ptr(host)
	r := utTCPOpen(hp, hl, port, timeoutMs)
	runtime.KeepAlive(host)
	return r
}

func rawTCPWrite(h int32, b []byte) int32 {
	p, n := ptr(b)
	r := utTCPWrite(h, p, n)
	runtime.KeepAlive(b)
	return r
}

func rawTCPRead(h int32, dst []byte, timeoutMs uint32) int32 {
	dp, dc := ptr(dst)
	r := utTCPRead(h, dp, dc, timeoutMs)
	runtime.KeepAlive(dst)
	return r
}

func rawTCPClose(h int32) int32 { return utTCPClose(h) }

func rawImportFileSize(h int32) int64 { return utImportFileSize(h) }

func rawImportFileRead(h int32, dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utImportFileRead(h, dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawImportFileClose(h int32) int32 { return utImportFileClose(h) }

func rawUploadOpen(tok []byte) int32 {
	tp, tl := ptr(tok)
	r := utUploadOpen(tp, tl)
	runtime.KeepAlive(tok)
	return r
}

func rawUploadRead(h int32, dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utUploadRead(h, dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawUploadClose(h int32) int32 { return utUploadClose(h) }

func rawHTTPOpen(req []byte) int32 {
	rp, rl := ptr(req)
	r := utHTTPOpen(rp, rl)
	runtime.KeepAlive(req)
	return r
}

func rawHTTPWrite(h int32, b []byte) int32 {
	p, n := ptr(b)
	r := utHTTPWrite(h, p, n)
	runtime.KeepAlive(b)
	return r
}

func rawHTTPStatus(h int32, dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utHTTPStatus(h, dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawHTTPRead(h int32, dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utHTTPRead(h, dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawHTTPClose(h int32) int32 { return utHTTPClose(h) }

func rawViewQuery(name, args, dst []byte) int32 {
	np, nl := ptr(name)
	ap, al := ptr(args)
	dp, dc := ptr(dst)
	r := utViewQuery(np, nl, ap, al, dp, dc)
	runtime.KeepAlive(name)
	runtime.KeepAlive(args)
	runtime.KeepAlive(dst)
	return r
}

func rawSecretSet(key, val []byte) int32 {
	kp, kl := ptr(key)
	vp, vl := ptr(val)
	r := utSecretSet(kp, kl, vp, vl)
	runtime.KeepAlive(key)
	runtime.KeepAlive(val)
	return r
}

func rawBlobPutOpen(name []byte) int32 {
	np, nl := ptr(name)
	r := utBlobPutOpen(np, nl)
	runtime.KeepAlive(name)
	return r
}

func rawBlobWrite(h int32, b []byte) int32 {
	p, n := ptr(b)
	r := utBlobWrite(h, p, n)
	runtime.KeepAlive(b)
	return r
}

func rawBlobCommit(h int32) int32 { return utBlobCommit(h) }

func rawBlobGetOpen(name []byte) int32 {
	np, nl := ptr(name)
	r := utBlobGetOpen(np, nl)
	runtime.KeepAlive(name)
	return r
}

func rawBlobRead(h int32, dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utBlobRead(h, dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawBlobDelete(name []byte) int32 {
	np, nl := ptr(name)
	r := utBlobDelete(np, nl)
	runtime.KeepAlive(name)
	return r
}

func rawBlobList(dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utBlobList(dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawItemImageOpen(id, role []byte) int32 {
	ip, il := ptr(id)
	rp, rl := ptr(role)
	r := utItemImageOpen(ip, il, rp, rl)
	runtime.KeepAlive(id)
	runtime.KeepAlive(role)
	return r
}

func rawItemImageRead(h int32, dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utItemImageRead(h, dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawEventPublish(typ, payload []byte) int32 {
	tp, tl := ptr(typ)
	pp, pl := ptr(payload)
	r := utEventPublish(tp, tl, pp, pl)
	runtime.KeepAlive(typ)
	runtime.KeepAlive(payload)
	return r
}

func rawJobProgress(pct uint32, key []byte) int32 {
	kp, kl := ptr(key)
	r := utJobProgress(pct, kp, kl)
	runtime.KeepAlive(key)
	return r
}

func rawDeviceIDGet(dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utDeviceIDGet(dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawDeviceLocalIPsGet(dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utDeviceLocalIPsGet(dp, dc)
	runtime.KeepAlive(dst)
	return r
}

func rawDeviceTimezoneGet(dst []byte) int32 {
	dp, dc := ptr(dst)
	r := utDeviceTimezoneGet(dp, dc)
	runtime.KeepAlive(dst)
	return r
}
