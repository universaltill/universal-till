// Bug-report DRAFT storage (ut-docs#2342). The 🐞 panel's contents belong
// to the till session, not to the page: a full document load (a form POST,
// a language/theme change, a self-update, the panel's own /my-reports link)
// tears the panel down with the document, and before this file that took
// the typed note, every screenshot and any recording with it. A boosted
// rail navigation never did (the panel sits outside #ut-page, ADR-0098) —
// this covers the loads that are not boosted.
//
// Two stores, each for what it is good at:
//   - sessionStorage `ut-bugreport-draft`: the small meta — {id, open, note,
//     pos}. Tab-scoped on purpose: a draft dies with the till's browser
//     session, so yesterday's half-written report never pops open on a
//     shared till the next morning.
//   - IndexedDB `ut-bugreport` / store `blobs`: the screenshots, a finished
//     voice/screen recording, and the in-progress recording's timeslice
//     chunks. Keyed `<draft id>:<kind>[:<seq>]`, so blobs from a draft that
//     no longer exists (a previous session's) are purged on the next boot —
//     IndexedDB is per-origin and would otherwise keep them forever.
//
// Every call is best-effort: a private window, a blocked store or an old
// WebView without IndexedDB degrades to "the panel works, nothing survives a
// full load", which is exactly the pre-#2342 behaviour — never an error.
//
// One tab per till is assumed (the kiosk is single-tab): a second tab has
// no meta in ITS sessionStorage, so its boot-time purge treats the first
// tab's blobs as stale and drops them. Acceptable for a till; not a
// general-purpose draft store.
(function () {
  var UT = window.UT = window.UT || {};
  var META_KEY = 'ut-bugreport-draft';
  var DB_NAME = 'ut-bugreport';
  var STORE = 'blobs';
  var dbPromise = null;

  function readMeta() {
    try { return JSON.parse(sessionStorage.getItem(META_KEY) || 'null'); } catch (e) { return null; }
  }
  function writeMeta(m) {
    try {
      if (m) sessionStorage.setItem(META_KEY, JSON.stringify(m));
      else sessionStorage.removeItem(META_KEY);
    } catch (e) { /* storage blocked: the draft just doesn't survive a load */ }
  }
  function newId() {
    return Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8);
  }

  function openDB() {
    if (dbPromise) return dbPromise;
    dbPromise = new Promise(function (resolve, reject) {
      if (!window.indexedDB) { reject(new Error('no indexedDB')); return; }
      var req;
      try { req = indexedDB.open(DB_NAME, 1); } catch (e) { reject(e); return; }
      req.onupgradeneeded = function () { req.result.createObjectStore(STORE); };
      req.onsuccess = function () { resolve(req.result); };
      req.onerror = function () { reject(req.error); };
      req.onblocked = function () { reject(new Error('blocked')); };
    });
    // A failed open is retried on the next call rather than cached forever.
    dbPromise.catch(function () { dbPromise = null; });
    return dbPromise;
  }

  // Runs fn(store) inside one transaction; resolves with fn's request result
  // (or undefined) once the transaction completes. Rejections are swallowed
  // into `undefined` by the public wrappers below — see the header.
  function withStore(mode, fn) {
    return openDB().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(STORE, mode);
        var out;
        try { out = fn(tx.objectStore(STORE)); } catch (e) { reject(e); return; }
        tx.oncomplete = function () { resolve(out && 'result' in out ? out.result : out); };
        tx.onerror = function () { reject(tx.error); };
        tx.onabort = function () { reject(tx.error); };
      });
    });
  }
  function quiet(p) { return p.catch(function () { return undefined; }); }

  var draft = {
    // ---- meta -----------------------------------------------------------
    meta: readMeta,
    // Ensures a draft exists and returns its meta. `open` etc. default off.
    ensure: function () {
      var m = readMeta();
      if (!m || !m.id) { m = { id: newId(), open: false, note: '', pos: null }; writeMeta(m); }
      return m;
    },
    set: function (patch) {
      var m = draft.ensure();
      for (var k in patch) if (Object.prototype.hasOwnProperty.call(patch, k)) m[k] = patch[k];
      writeMeta(m);
      return m;
    },
    // ---- blobs ----------------------------------------------------------
    key: function (kind, seq) {
      var m = draft.ensure();
      return m.id + ':' + kind + (seq === undefined ? '' : ':' + String(seq).padStart(6, '0'));
    },
    put: function (key, blob) { return quiet(withStore('readwrite', function (s) { return s.put(blob, key); })); },
    get: function (key) { return quiet(withStore('readonly', function (s) { return s.get(key); })); },
    del: function (key) { return quiet(withStore('readwrite', function (s) { return s.delete(key); })); },
    // All sequenced keys of this draft of the given kind (`<id>:<kind>:*`),
    // sorted (seq order). The trailing ':' keeps `keys('screen')` from
    // matching `screen-chunk:*`.
    keys: function (kind) {
      var prefix = draft.key(kind) + ':';
      return quiet(withStore('readonly', function (s) {
        return s.getAllKeys(IDBKeyRange.bound(prefix, prefix + '\uffff'));
      })).then(function (ks) { return (ks || []).slice().sort(); });
    },
    // Every blob of this draft, as [{key, blob}] sorted by key.
    entries: function (kind) {
      return draft.keys(kind).then(function (ks) {
        return Promise.all(ks.map(function (k) {
          return draft.get(k).then(function (b) { return { key: k, blob: b }; });
        }));
      }).then(function (es) { return es.filter(function (e) { return e.blob; }); });
    },
    // Drops every blob of the current draft (keeps the draft's meta).
    clearBlobs: function () {
      var m = draft.ensure();
      return quiet(withStore('readwrite', function (s) {
        return s.delete(IDBKeyRange.bound(m.id + ':', m.id + ':\uffff'));
      }));
    },
    // Drops the whole draft: meta + blobs. The next `ensure()` starts a new one.
    discard: function () {
      var p = draft.clearBlobs();
      writeMeta(null);
      return p;
    },
    // Boot-time hygiene: blobs whose draft id is not the current one belong
    // to a session that is gone. One pass, cheap (a keys scan).
    purgeStale: function () {
      var m = readMeta();
      var keep = m && m.id ? m.id + ':' : null;
      return quiet(withStore('readwrite', function (s) {
        var req = s.getAllKeys();
        req.onsuccess = function () {
          (req.result || []).forEach(function (k) {
            if (typeof k === 'string' && (!keep || k.indexOf(keep) !== 0)) s.delete(k);
          });
        };
        return undefined;
      }));
    }
  };

  UT.bugreportDraft = draft;
})();
