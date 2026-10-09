package plugins

// Catalog item reference photos for wasm plugins (ADR-0121 R1,
// ut-docs#4005):
//
//	item_image_open(idPtr, idLen, rolePtr, roleLen) -> handle | err
//	item_image_read(h, dstPtr, dstCap) -> n (0 = end, releases h) | err
//
// role is "ai_ref" (the item's newest cashier-confirmed photo), "thumb" (its
// catalog thumbnail) or "ref" (ai_ref if it decodes, else thumb — the
// built-in identify's choice). The bytes are internal/itemimages.Ref's:
// the bounded internal/imaging decode re-encoded as a JPEG ≤ 160 px, q70,
// identical to what the built-in identify sends. The host builds the path
// from the validated id under the items asset dir; it never follows an
// item_images row and never hands the guest a path or the original file.
//
// Every open is checked against view:inventory (CheckPermission: audited,
// revocable live). Per event: at most itemImageOpensPerEvent opens (failed
// ones count; a separate counter from view_query's) and itemImageMaxHandles
// open handles. A handle holds the re-encoded bytes in memory (a few KiB);
// item_image_read's 0 releases it, and handleEvent releases every handle
// still open when the event returns.

import (
	"context"
	"errors"
	"sync"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/itemimages"
	"github.com/universaltill/universal-till/internal/logging"
)

const (
	// permViewInventory gates item_image_open (it is the view class of the
	// catalog and stock core views).
	permViewInventory = "view:inventory"
	// itemImageOpensPerEvent caps item_image_open calls per event: the
	// built-in's 60-item reference set fits, with room to spare.
	itemImageOpensPerEvent = 64
	// itemImageMaxHandles caps a plugin's open item image handles per event.
	itemImageMaxHandles = 4
	// itemImageReadCap bounds one item_image_read regardless of dstCap.
	itemImageReadCap = 256 << 10
	// itemImageRoleMax bounds the role argument read from the guest.
	itemImageRoleMax = 16
)

// itemImageHandle is one open image: the re-encoded bytes and a read offset.
type itemImageHandle struct {
	data []byte
	off  int
}

// itemImageHandles is the per-event registry (on hostState). Handles count
// from 0 within the event and are never reused within it.
type itemImageHandles struct {
	mu    sync.Mutex
	opens int
	next  int32
	open  map[int32]*itemImageHandle
}

// countOpen records one item_image_open call and reports whether it is
// within the per-event cap.
func (r *itemImageHandles) countOpen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opens++
	return r.opens <= itemImageOpensPerEvent
}

func (r *itemImageHandles) full() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.open) >= itemImageMaxHandles
}

func (r *itemImageHandles) add(h *itemImageHandle) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.open) >= itemImageMaxHandles {
		return 0, false
	}
	if r.open == nil {
		r.open = map[int32]*itemImageHandle{}
	}
	id := r.next
	r.next++
	r.open[id] = h
	return id, true
}

func (r *itemImageHandles) get(h int32) (*itemImageHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.open[h]
	return b, ok
}

func (r *itemImageHandles) release(h int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.open, h)
}

// closeAll drops every handle — handleEvent defers it so no image outlives
// its event.
func (r *itemImageHandles) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open = nil
}

func hostItemImageOpen(ctx context.Context, m api.Module, idPtr, idLen, rolePtr, roleLen uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if !s.itemImages.countOpen() {
		return hostErrQuota
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, permViewInventory); err != nil {
		return hostErrDenied
	}
	if idLen == 0 || idLen > itemimages.MaxIDLen || roleLen > itemImageRoleMax {
		return hostErrInvalid
	}
	idB, iok := readGuest(m, idPtr, idLen)
	roleB, rok := readGuest(m, rolePtr, roleLen)
	if !iok || !rok {
		return hostErrInvalid
	}
	id, role := string(idB), string(roleB)
	if !itemimages.ValidID(id) {
		return hostErrInvalid
	}
	switch role {
	case itemimages.RoleAIRef, itemimages.RoleThumb, itemimages.RoleRef:
	default:
		return hostErrInvalid
	}
	// Before the decode, so a full handle table costs no work.
	if s.itemImages.full() {
		return hostErrBusy
	}
	data, err := itemimages.Ref(id, role)
	switch {
	case errors.Is(err, itemimages.ErrNotFound):
		return hostErrNotFound
	case errors.Is(err, itemimages.ErrInvalid):
		return hostErrInvalid
	case err != nil:
		logging.L().Warnf("[wasm:%s] item image: %v", s.pluginID, err)
		return hostErrInternal
	}
	h, ok := s.itemImages.add(&itemImageHandle{data: data})
	if !ok {
		return hostErrBusy
	}
	return h
}

func hostItemImageRead(ctx context.Context, m api.Module, h int32, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	img, ok := s.itemImages.get(h)
	if !ok {
		return hostErrNotFound
	}
	n := min(dstCap, itemImageReadCap)
	if n == 0 {
		return hostErrInvalid
	}
	rest := img.data[img.off:]
	if len(rest) == 0 {
		s.itemImages.release(h)
		return 0
	}
	if uint32(len(rest)) < n {
		n = uint32(len(rest))
	}
	if !m.Memory().Write(dstPtr, rest[:n]) {
		return hostErrInvalid
	}
	img.off += int(n)
	return int32(n)
}
