package plugindb

import (
	"context"
	"os"
	"path/filepath"
)

// idLock is a reference-counted per-plugin lock (a 1-slot channel so a waiter
// can give up when its context ends). Manager.mu only guards the maps;
// anything slow (open, migrate, snapshot, restore) holds the per-id lock so
// one plugin's upgrade never blocks another plugin's database.
type idLock struct {
	ch   chan struct{}
	refs int
}

// lockID acquires the per-id lock (no deadline) and returns its release.
func (m *Manager) lockID(id string) func() {
	unlock, _ := m.lockIDCtx(context.Background(), id)
	return unlock
}

// lockIDCtx acquires the per-id lock, giving up with ctx.Err() when ctx ends
// first so a caller queued behind a slow upgrade honours its own deadline.
func (m *Manager) lockIDCtx(ctx context.Context, id string) (func(), error) {
	m.mu.Lock()
	l, ok := m.locks[id]
	if !ok {
		l = &idLock{ch: make(chan struct{}, 1)}
		m.locks[id] = l
	}
	l.refs++
	m.mu.Unlock()
	drop := func() {
		m.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(m.locks, id)
		}
		m.mu.Unlock()
	}
	select {
	case l.ch <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	return func() { <-l.ch; drop() }, nil
}

// resolvedRoot makes the data root and resolves symlinks in it once per call:
// operators may symlink the whole data drive, which is fine; a plugin id
// below the root may not be a link (see safeDir).
func (m *Manager) resolvedRoot() (string, error) {
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return "", err
	}
	rr, err := filepath.EvalSymlinks(m.root)
	if err != nil {
		return "", err
	}
	return rr, nil
}

// realDir reports whether p is an existing real directory (not a symlink).
// A missing p returns (false, nil).
func realDir(p string) (bool, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false, ErrSymlink
	}
	return true, nil
}

// safeDir returns <resolvedRoot>/<id>, creating it when create is set, and
// refuses it when it is a symlink or resolves anywhere else.
func (m *Manager) safeDir(id string, create bool) (string, error) {
	rr, err := m.resolvedRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(rr, id)
	ok, err := realDir(dir)
	if err != nil {
		return "", err
	}
	if !ok {
		if !create {
			return dir, nil
		}
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		if ok, err = realDir(dir); err != nil || !ok {
			if err == nil {
				err = ErrSymlink
			}
			return "", err
		}
	}
	if ev, err := filepath.EvalSymlinks(dir); err != nil || ev != dir {
		return "", ErrSymlink
	}
	return dir, nil
}

// regularOrMissing refuses a path that exists but is not a regular file
// (symlink, directory, device).
func regularOrMissing(p string) (exists bool, err error) {
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return true, ErrSymlink
	}
	return true, nil
}
