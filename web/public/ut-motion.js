// ut-docs#2972: the ONE shared motion code (ADR-0122 §5, ADR-0119, ADR-0123),
// cut verbatim from base.html's first inline script so base.html and every
// standalone page that hosts a <dialog> (the self-order kiosk) load the same
// file. Loaded synchronously and first in <head> (no defer/async): the
// showModal/show wrapper must exist before htmx or inline-actions.js can open
// a dialog. Guarded by internal/pages popup_zoom_guard_test.go.
(function () {
        // ut-docs#2496: the one "a transition must never hold the screen"
        // watchdog, shared by the cross-document (pagereveal, below) and
        // same-document (boosted #ut-page swap, shell script) paths. The
        // 1000ms budget (the 400ms motion of ADR-0118/0122 plus margin for a slow
        // tablet's dropped frames) starts when the MOTION starts (vt.ready),
        // never when the transition is created: a same-document transition
        // is created BEFORE htmx swaps the page in, and on the pilot tablet
        // that swap alone can take most of the budget, so a creation-time
        // timer cut the motion short or skipped it outright -- the "not like
        // Apple anymore" regression (reproduced at 4x/8x CPU throttle). A 2s
        // backstop from creation still covers an engine whose ready never
        // settles (a swap that itself runs past 2s trips it just before
        // ready and skips a motion that was about to play -- harmless: the
        // swap held the screen, not the transition). Defined before the
        // feature check below so the shell script can always find it.
        //
        // Input is never blocked by the motion either (ADR-0118). app.css
        // sets `::view-transition { pointer-events: none }`, but Chromium
        // (141, probed) still hit-tests every pointer to <html> while a
        // transition runs, so a tap during the 400ms would be lost. A
        // pointerdown during a transition therefore ends it on the spot
        // (the operator's touch takes over, as on a phone): hit testing is
        // back on the real, already-swapped DOM synchronously, so a touch
        // tap's click (hit-tested again at tap time) lands natively; a mouse
        // click, whose target was fixed at mousedown (<html>), is handed to
        // the element actually under the pointer.
        var UT = window.UT = window.UT || {};
        // ADR-0119 (ut-docs#2859): the per-till effects level, read ONCE at load
        // from the fx-* class every page renders on <html> from the same
        // fxlevel value (ut-docs#2972; a runtime edit of the class is not seen,
        // as before when it was a templated literal).
        var fxm = /(?:^|\s)fx-(full|balanced|light)(?:\s|$)/.exec(document.documentElement.className);
        UT.fxLevel = fxm ? fxm[1] : '';
        // ADR-0119 (ut-docs#2859): the per-till effects level (the same
        // value as the fx-* class on <html>) and the ONE motion predicate
        // every JS motion path checks -- this script, the shell script
        // below, app.js's swap ease and the shared popup zoom (ADR-0122 §5).
        // prefers-reduced-motion stays the floor: the level can only take
        // motion away, never switch it back on. Defined here, the first
        // script in the document, so everything after it can rely on it.
        var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)');
        UT.motionOff = function () {
          return !!(reduce && reduce.matches) || UT.fxLevel === 'light';
        };
        // ADR-0122 §4/§7: every running zoom (Web Animations) registers here
        // so a tap, a popup opening or the next pane swap can finish() it --
        // the transform is transient, never left on a pane that hosts
        // position:fixed dialogs (the #2338 containing-block hazard).
        // A PASSIVE zoom (opts.passive: the closing popup shrink -- inert,
        // pointer-events:none, so no tap is ever hit-tested on it) is still
        // finished by a tap, but finishZooms() reports only non-passive ones:
        // its true is what arms the §7 click re-dispatch below.
        var zooms = [];
        UT.trackZoom = function (anim, opts) {
          if (!anim) return;
          if (opts && opts.passive) anim.utPassive = true;
          zooms.push(anim);
          function drop() { var i = zooms.indexOf(anim); if (i >= 0) zooms.splice(i, 1); }
          anim.onfinish = drop; anim.oncancel = drop;
        };
        UT.finishZooms = function () {
          var list = zooms; zooms = [];
          var any = false;
          list.forEach(function (a) {
            try { if (a.playState !== 'finished') { a.finish(); if (!a.utPassive) any = true; } } catch (e) { /* gone */ }
          });
          return any;
        };
        var activeVT = null, skippedByTapAt = 0, zoomTap = false;
        window.addEventListener('pointerdown', function (e) {
          // A tap during a zoom completes it on the spot (ADR-0122 §7). The
          // tap was hit-tested at the moving position, so the origin is read
          // from the element under the pointer NOW, at its final position.
          // Any new press disarms a re-dispatch window a previous press left
          // behind (a scroll or drag that never produced a click).
          skippedByTapAt = 0; zoomTap = false;
          // The origin itself is recorded by the ONE recorder below (#2942),
          // which runs after this listener and reads zoomTap.
          var finished = UT.finishZooms();
          var vt = activeVT;
          if (vt) {
            activeVT = null;
            try { vt.skipTransition(); } catch (err) { /* already done */ }
          }
          if (vt || finished) { skippedByTapAt = Date.now(); zoomTap = finished; }
        }, true);
        window.addEventListener('pointercancel', function () { skippedByTapAt = 0; zoomTap = false; }, true);
        window.addEventListener('click', function (e) {
          if (!skippedByTapAt || Date.now() - skippedByTapAt > 1000) return;
          var byZoom = zoomTap;
          skippedByTapAt = 0; zoomTap = false;
          // A skipped View Transition hit-tests to <html>; a finished zoom
          // hit-tested the moving element, so a mouse click lands on a common
          // ancestor -- re-dispatch whenever the target is not the element now
          // under the pointer (ADR-0122 §7). A keyboard click (detail 0) has
          // no pointer position and is left alone.
          if (byZoom ? e.detail === 0 : e.target !== document.documentElement) return;
          var el = document.elementFromPoint(e.clientX, e.clientY);
          if (!el || el === document.documentElement || el === e.target) return;
          e.stopImmediatePropagation();
          e.preventDefault();
          // The trusted mousedown was hit-tested to <html> too, so it never
          // focused anything: focus the field/control under the pointer first
          // (a mouse click on the scan field mid-motion must leave the caret
          // there -- review M1). A finished zoom's mousedown DID focus the
          // moving control, so a button/link under the pointer takes focus
          // too (ut-docs#2943 review).
          var f = el.closest && el.closest('input,textarea,select,button,a[href],[contenteditable],[tabindex]');
          if (f && f.focus) { try { f.focus(); } catch (err) { /* not focusable */ } }
          // dispatchEvent, not el.click(): the element under the pointer is
          // often an SVG icon inside the tile (no .click()); a synthetic
          // click still runs a link's/button's activation behaviour.
          el.dispatchEvent(new MouseEvent('click', {
            bubbles: true, cancelable: true, composed: true, view: window,
            clientX: e.clientX, clientY: e.clientY, screenX: e.screenX, screenY: e.screenY,
            button: e.button, buttons: e.buttons, detail: e.detail,
            ctrlKey: e.ctrlKey, shiftKey: e.shiftKey, altKey: e.altKey, metaKey: e.metaKey
          }));
        }, true);
        // ADR-0122 §2 (ut-docs#2942): the ONE motion-origin recorder -- the
        // iOS zoom's "source view". Capture phase, so it runs before any
        // htmx request or swap the same press causes (and after the
        // transition skip above, so a tap during a page motion reads the
        // element really under the pointer, not <html>). Consumers --
        // the page zoom below, the tree-pane zoom (#2943), the popup zoom
        // (#2944) -- call UT.takeOrigin(), which hands the origin out ONCE.
        // The next press replaces it, and it expires after 5 s, so a slow
        // response on a weak device still zooms from the right place while
        // a stale tap never does. An off-screen or zero-size box is no
        // origin (the motion then uses the bottom-centre fallback). Nothing
        // here may throw into the page.
        var ORIGIN_SEL = 'a, button, [hx-get], [hx-post], [role=treeitem], [data-zoom-origin]';
        var ORIGIN_TTL_MS = 5000;
        UT.motionOrigin = null;
        function viewport() {
          var de = document.documentElement;
          return { w: window.innerWidth || de.clientWidth || 0, h: window.innerHeight || de.clientHeight || 0 };
        }
        // Takes a DOMRect or the plain { left, top, width, height } the back
        // store hands back.
        function onScreen(r) {
          var v = viewport();
          return !!r && r.width > 0 && r.height > 0 &&
            r.left + r.width > 0 && r.top + r.height > 0 && r.left < v.w && r.top < v.h;
        }
        function recordOrigin(e, target) {
          try {
            var t = target;
            if (t === document.documentElement && e && typeof e.clientX === 'number' && document.elementFromPoint) {
              t = document.elementFromPoint(e.clientX, e.clientY);
            }
            var el = t && t.closest ? t.closest(ORIGIN_SEL) : null;
            var r = el ? el.getBoundingClientRect() : null;
            if (!el || !onScreen(r)) { UT.motionOrigin = null; return; }
            UT.motionOrigin = { rect: r, el: el, at: Date.now() };
          } catch (err) { UT.motionOrigin = null; }
        }
        // A tap that just finished a zoom (the pointerdown listener above set
        // zoomTap) was hit-tested at the moving position: record the element
        // under the pointer NOW, at its final position (ADR-0122 §7).
        window.addEventListener('pointerdown', function (e) {
          var t = e.target;
          if (zoomTap && document.elementFromPoint) t = document.elementFromPoint(e.clientX, e.clientY) || t;
          recordOrigin(e, t);
        }, true);
        window.addEventListener('keydown', function (e) {
          if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;
          // Typing in a field (Space in a search box, Enter in a form) is
          // not an activation of a source view -- and would otherwise force
          // a layout read per keystroke and record the whole form.
          var tg = e.target;
          if (tg && (tg.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(tg.tagName || ''))) return;
          // Like a tap (ADR-0122 §7): finish a running pane zoom first, so
          // the box read is the control's final position, not a mid-zoom one.
          try { UT.finishZooms(); } catch (err) { /* motion only */ }
          recordOrigin(null, e.target);
        }, true);
        UT.takeOrigin = function () {
          var o = UT.motionOrigin;
          UT.motionOrigin = null;
          if (!o || Date.now() - o.at > ORIGIN_TTL_MS) return null;
          return o;
        };
        // ADR-0122 §5 (ut-docs#2944): the ONE popup motion. Every <dialog>,
        // sheet and picker grows from its source element (UT.takeOrigin(),
        // recorded on pointerdown -- before the htmx request that fetched an
        // item's variants popup) and, on close, shrinks back into it. No
        // per-dialog code: showModal/show are wrapped once, below, and the
        // close side is the dialog `close` EVENT (it also covers Escape,
        // <form method="dialog"> and requestClose(), which never call
        // .close()). A popup that is not a <dialog> (the bug-report panel)
        // calls UT.popupOpened(el) / UT.popupClosed(el) -- the same two code
        // paths. FLIP on the SAME element: uniform scale (source width /
        // popup width, clamped .1-1) plus the translation between the two
        // centres, transform + opacity only, from opacity .4 (never blank),
        // fill 'none' (nothing remains after the last frame), registered with
        // UT.trackZoom so a tap finishes it and lands on the control at its
        // final position (§7). The popup's own CSS transform (e.g.
        // .shrinkage-sheet's translateY(-50%)) is kept: the zoom is composed
        // in front of it, assuming the default centred transform-origin.
        // Nothing here may throw into the page, and under UT.motionOff()
        // (reduced motion or Light, ADR-0119) no animate() call is made and
        // no closing popup is re-shown (§6). The close has its own timing:
        // POPUP_CLOSE_EASE/closeMs() (ADR-0123, ut-docs#2987).
        var POPUP_EASE = 'cubic-bezier(.32, .72, 0, 1)';
        var POPUP_CLOSE_EASE = 'cubic-bezier(.4, 0, .6, 1)';
        var CLOSING_VARS = ['--ut-closing-t', '--ut-closing-is', '--ut-closing-w', '--ut-closing-h', '--ut-closing-display'];
        // el -> { origin, box, display, opening, closing }
        var popups = typeof WeakMap === 'function' ? new WeakMap() : null;
        // Open, tracked popups whose box is kept current: a closed <dialog>
        // has no box, so its closing shrink starts from the last one seen.
        var livePopups = [];
        var popupRO = null;
        function smallMs() {
          var ms = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--ut-zoom-small-ms'));
          return ms > 0 ? ms : 300;
        }
        // ADR-0123 (ut-docs#2987): the close shrink's own, longer duration
        // (--ut-zoom-close-ms) -- falls back to smallMs() (not its own
        // literal) when unset/invalid, so an old token still animates.
        function closeMs() {
          var ms = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--ut-zoom-close-ms'));
          return ms > 0 ? ms : smallMs();
        }
        function popupState(el) {
          var st = popups.get(el);
          if (!st) { st = { origin: null, box: null, display: '', opening: null, closing: null }; popups.set(el, st); }
          return st;
        }
        // Records the popup's box while it is shown and still: a box read
        // mid-zoom is transformed, a hidden one is empty (keep the last).
        function measure(el) {
          var st = popups && popups.get(el);
          if (!st || st.opening || st.closing || !el.getClientRects().length) return;
          var r = el.getBoundingClientRect();
          if (!(r.width > 0 && r.height > 0)) return;
          st.box = { left: r.left, top: r.top, width: r.width, height: r.height };
          st.display = getComputedStyle(el).display;
        }
        function watchPopup(el) {
          if (livePopups.indexOf(el) < 0) livePopups.push(el);
          try {
            if (!popupRO && window.ResizeObserver) {
              popupRO = new ResizeObserver(function (entries) {
                entries.forEach(function (en) { try { measure(en.target); } catch (e) { /* motion only */ } });
              });
            }
            if (popupRO) popupRO.observe(el);
          } catch (e) { /* motion only */ }
        }
        function unwatchPopup(el) {
          var i = livePopups.indexOf(el);
          if (i >= 0) livePopups.splice(i, 1);
          try { if (popupRO) popupRO.unobserve(el); } catch (e) { /* motion only */ }
        }
        function measureLive() {
          livePopups.slice().forEach(function (el) {
            if (!el.isConnected) { unwatchPopup(el); return; }
            try { measure(el); } catch (e) { /* motion only */ }
          });
        }
        // A tap (after the listener above has finished every zoom), an
        // Escape about to close a dialog (`cancel` fires before `close`) and
        // a window resize all refresh the boxes; a ResizeObserver catches
        // content swapped into an open popup.
        window.addEventListener('pointerdown', measureLive, true);
        document.addEventListener('cancel', measureLive, true);
        window.addEventListener('resize', measureLive);
        function bottomCentre() {
          var v = viewport();
          return { left: v.w / 2, top: v.h, width: 0, height: 0 };
        }
        // The translation-part of the popup's own transform, so the zoom can
        // be composed in front of it (translate(T) scale(s) <own>, centred
        // origin: the visual centre then lands on the source's centre).
        function ownTransform(el) {
          var css = getComputedStyle(el).transform;
          if (!css || css === 'none') return { css: '', x: 0, y: 0 };
          try {
            var m = new DOMMatrixReadOnly(css);
            return { css: css, x: m.m41, y: m.m42 };
          } catch (e) { return { css: css, x: 0, y: 0 }; }
        }
        // Uniform start scale plus translation from box to the source rect.
        function zoomFrom(box, r, own) {
          var s = Math.min(1, Math.max(0.1, r.width / box.width));
          var dx = (r.left + r.width / 2) - (box.left + box.width / 2) + (1 - s) * own.x;
          var dy = (r.top + r.height / 2) - (box.top + box.height / 2) + (1 - s) * own.y;
          return 'translate(' + dx + 'px, ' + dy + 'px) scale(' + s + ')' + (own.css ? ' ' + own.css : '');
        }
        // Where a closing popup shrinks to: the source element's box NOW if it
        // is still in the document, rendered (not inside a closed dialog or a
        // display:none subtree) and on screen; otherwise the bottom centre
        // (iOS: the source view is gone). Never the box recorded at the tap:
        // the element is no longer there (ADR-0122 §2/§5).
        function closeTarget(o) {
          try {
            if (o && o.el && o.el.isConnected && o.el.getClientRects().length) {
              var r = o.el.getBoundingClientRect();
              if (onScreen(r)) return r;
            }
          } catch (e) { /* fall back */ }
          return bottomCentre();
        }
        function popupOpen(el) {
          if (UT.motionOff()) return;
          if (!popups || !el || !el.animate || !el.getClientRects().length) return;
          var st = popupState(el);
          endClosing(el);
          var o = UT.takeOrigin();
          st.origin = o ? { el: o.el, rect: o.rect } : null;
          watchPopup(el);
          var box = el.getBoundingClientRect();
          if (!box.width || !box.height) return;
          st.box = { left: box.left, top: box.top, width: box.width, height: box.height };
          st.display = getComputedStyle(el).display;
          var r = (o && onScreen(o.rect)) ? o.rect : bottomCentre();
          var own = ownTransform(el), ms = smallMs();
          var anim = el.animate([
            { transform: zoomFrom(box, r, own), opacity: 0.4 },
            { transform: own.css || 'none', opacity: 1 }
          ], { duration: ms, easing: POPUP_EASE, fill: 'none' });
          st.opening = anim;
          var settled = function () { if (st.opening === anim) { st.opening = null; measure(el); } };
          anim.addEventListener('finish', settled);
          anim.addEventListener('cancel', settled);
          UT.trackZoom(anim);
          // The ::backdrop (a modal's) fades in with the open motion; it goes
          // with the native close, so the shrink has none by design.
          try {
            if (el.matches(':modal')) {
              UT.trackZoom(el.animate({ opacity: [0, 1] }, { duration: ms, easing: POPUP_EASE, fill: 'none', pseudoElement: '::backdrop' }));
            }
          } catch (e) { /* engine without :modal or ::backdrop animations */ }
        }
        // Ends a closing shrink at once: the popup goes back to its own
        // (closed) styling. Reopening during the shrink calls this first.
        function endClosing(el) {
          var st = popups && popups.get(el), c = st && st.closing;
          if (!c) return;
          st.closing = null;
          clearTimeout(c.timer);
          try { c.anim.cancel(); } catch (e) { /* already gone */ }
          el.removeAttribute('data-ut-closing');
          if (!c.inert) el.removeAttribute('inert');
          if (c.aria === null) el.removeAttribute('aria-hidden'); else el.setAttribute('aria-hidden', c.aria);
          CLOSING_VARS.forEach(function (p) { el.style.removeProperty(p); });
        }
        // The native close has already happened (the page takes input, focus
        // is back): the SAME element is painted again for the motion's length
        // only -- data-ut-closing (app.css: fixed at its last box, no pointer
        // events, above the rail and the on-screen keyboard), inert and
        // aria-hidden -- and shrinks into its source. Its id-keyed CSS, typed
        // values and canvas stay as they were; a clone would lose them.
        // Runs from the dialog `close` EVENT, a queued task -- the close site
        // has already returned, so close sites must not mutate the dialog.
        function popupClose(el) {
          if (UT.motionOff()) return;
          if (!popups || !el || !el.animate) return;
          var st = popupState(el);
          if (st.opening) { var op = st.opening; st.opening = null; try { op.finish(); } catch (e) { /* gone */ } }
          endClosing(el);
          measure(el); // a non-dialog may still be shown when it reports
          unwatchPopup(el);
          var box = st.box;
          if (!box || !el.isConnected || el.open === true) return;
          // Its content is already gone (a close site emptied it): a blank
          // frame shrinking into the source is worse than no motion.
          if (!el.childElementCount) return;
          var r = closeTarget(st.origin);
          var rtl = getComputedStyle(el).direction === 'rtl';
          var vw = document.documentElement.clientWidth || viewport().w;
          var s = el.style;
          s.setProperty('--ut-closing-t', box.top + 'px');
          s.setProperty('--ut-closing-is', (rtl ? vw - box.left - box.width : box.left) + 'px');
          s.setProperty('--ut-closing-w', box.width + 'px');
          s.setProperty('--ut-closing-h', box.height + 'px');
          if (st.display && st.display !== 'none') s.setProperty('--ut-closing-display', st.display);
          var c = { anim: null, timer: 0, inert: el.hasAttribute('inert'), aria: el.getAttribute('aria-hidden') };
          st.closing = c;
          el.setAttribute('data-ut-closing', '');
          el.setAttribute('inert', '');
          el.setAttribute('aria-hidden', 'true');
          // ADR-0123: the shrink's own, longer duration and ease-in-out
          // curve -- opacity holds at 1 to offset .55 of that eased motion,
          // then fades with the rest of the shrink (the middle keyframe has
          // no transform: WAAPI interpolates it between the ends), so the
          // owner sees the shape shrinking before it fades, not both at once
          // from frame one.
          var ms = closeMs();
          var done = function () {
            if (st.closing !== c) return;
            endClosing(el);
            // ut-docs#2972: data-ut-clear-on-close -- drop the markup the
            // dialog held when it closed, now that the shrink has read it.
            // Only those nodes, only while still closed: content swapped in
            // for the next open is never touched.
            var kids = c.clearKids;
            if (kids && !el.open) kids.forEach(function (n) { if (n.parentNode === el) el.removeChild(n); });
          };
          c.timer = setTimeout(done, ms + 250); // backstop: never left painted
          try {
            c.anim = el.animate([
              { transform: 'none', opacity: 1 },
              { opacity: 1, offset: 0.55 },
              { transform: zoomFrom(box, r, { css: '', x: 0, y: 0 }), opacity: 0 }
            ], { duration: ms, easing: POPUP_CLOSE_EASE, fill: 'none' });
            c.anim.addEventListener('finish', done);
            c.anim.addEventListener('cancel', done);
            UT.trackZoom(c.anim, { passive: true });
          } catch (e) { done(); }
        }
        // ut-docs#2873: the shared popup scrim (#ut-scrim, near the end of
        // <body>; app.css). A showModal() dialog (since ut-docs#2097 only
        // the self-order kiosk's) brings its own native ::backdrop; a
        // NON-modal popup (a .show() dialog -- every till dialog -- or a
        // UT.popupOpened() panel) gets
        // this one div behind it, so a tap beside the popup lands on the
        // scrim, never on the sale screen. Driven by the SAME hooks as the
        // motion (the show/showModal wrapper, the `close` event,
        // UT.popupOpened/Closed) plus a MutationObserver backstop on `open`
        // and on removed nodes. Shown iff the set of open, connected,
        // non-modal, not-closing popups is non-empty; every recompute
        // prunes the set first, so an htmx swap that removes an open
        // dialog, `dialog.open = false` or removeAttribute('open') can
        // never leave it stuck on. data-ut-no-scrim: a panel the cashier
        // keeps working beside (never tracked). data-ut-scrim-dismiss: a
        // tap on the scrim closes the topmost popup, only if IT opted in.
        // Nothing here may throw into the page or cost a dialog its open.
        var scrimSet = [];
        var scrimRaf = 0;
        function scrimShown(el) {
          if (!el || !el.isConnected || el.hasAttribute('data-ut-closing')) return false;
          if (el.tagName === 'DIALOG') {
            if (!el.open) return false;
            try { if (el.matches(':modal')) return false; } catch (e) { /* no :modal */ }
            return true;
          }
          return !el.hidden && el.getClientRects().length > 0;
        }
        function scrimSync() {
          scrimRaf = 0;
          try {
            scrimSet = scrimSet.filter(scrimShown);
            var on = scrimSet.length > 0;
            var sc = document.getElementById('ut-scrim');
            if (sc && sc.hidden === on) sc.hidden = !on;
            document.documentElement.classList.toggle('ut-scrim-on', on && !!sc);
          } catch (e) { /* scrim only */ }
        }
        function scrimSoon() {
          try {
            if (scrimRaf) return;
            scrimRaf = window.requestAnimationFrame ? requestAnimationFrame(scrimSync) : setTimeout(scrimSync, 16);
          } catch (e) { /* scrim only */ }
        }
        function scrimTrack(el) {
          try {
            if (el && el.nodeType === 1 && !el.hasAttribute('data-ut-no-scrim')) {
              var i = scrimSet.indexOf(el);
              if (i >= 0) scrimSet.splice(i, 1);
              scrimSet.push(el); // last = topmost (most recently opened)
            }
            scrimSync();
          } catch (e) { /* scrim only */ }
        }
        UT.scrimSync = scrimSync;
        function scrimInit() {
          var sc = document.getElementById('ut-scrim');
          if (!sc || sc.utScrim) return;
          sc.utScrim = true;
          // Keep focus (and the on-screen keyboard) in the popup's field:
          // a tap on the scrim is not a tap on the page.
          sc.addEventListener('mousedown', function (e) { e.preventDefault(); });
          sc.addEventListener('click', function (e) {
            e.preventDefault();
            try {
              scrimSync();
              var top = scrimSet[scrimSet.length - 1];
              if (top && top.tagName === 'DIALOG' && top.hasAttribute('data-ut-scrim-dismiss')) top.close();
            } catch (err) { /* scrim only */ }
          });
          // Backstop for every open/close path the hooks do not see: a
          // server-rendered or setAttribute('open') dialog, `open = false`,
          // an htmx swap removing an open popup.
          try {
            new MutationObserver(function (records) {
              for (var i = 0; i < records.length; i++) {
                var t = records[i].target;
                if (records[i].type === 'attributes' && t.tagName === 'DIALOG' && t.open && scrimSet.indexOf(t) < 0) {
                  var modal = false;
                  try { modal = t.matches(':modal'); } catch (e) { /* no :modal */ }
                  if (!modal && !t.hasAttribute('data-ut-no-scrim')) { scrimSet.push(t); }
                }
              }
              scrimSoon();
            }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['open', 'hidden'] });
          } catch (e) { /* scrim only: the hooks still drive it */ }
          scrimSync();
        }
        if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', scrimInit);
        else scrimInit();
        // ut-docs#2097: Escape for a NON-modal dialog. Only a showModal()
        // dialog gets the browser's own Escape-closes, and those made the
        // status bar and Lock inert -- so the till's former showModal()
        // dialogs now open with .show() and opt in here with
        // data-ut-escape-close. Escape closes the TOPMOST scrim popup, and
        // only if IT opted in (a dialog opened over it keeps Escape for
        // itself, or ignores it). Same sequence as the native one: a
        // cancelable `cancel` event first (the motion's measureLive reads
        // it; preventDefault keeps the dialog open), then close() -- whose
        // `close` event is what every existing listener (the popup motion,
        // the scrim, app.js's order-type prompt treating a no-choice close
        // as Cancel) already handles. Bubble phase on window: a page's own
        // handler that consumed Escape (preventDefault) wins.
        window.addEventListener('keydown', function (e) {
          if (e.key !== 'Escape' || e.defaultPrevented || e.isComposing) return;
          try {
            scrimSync();
            var top = scrimSet[scrimSet.length - 1];
            if (!top || top.tagName !== 'DIALOG' || !top.hasAttribute('data-ut-escape-close')) return;
            e.preventDefault();
            // ut-docs#5288: a prompt whose own request is in flight can't be
            // cancelled -- the action may already be done and its answer
            // must still show. htmx marks the requesting form .htmx-request
            // until the request ends, then Escape works again.
            if (top.hasAttribute('data-ut-prompt') && top.querySelector('form.htmx-request')) return;
            var cancel = new Event('cancel', { cancelable: true });
            if (top.dispatchEvent(cancel)) top.close();
          } catch (err) { /* escape only */ }
        });
        // ut-docs#2097 (review): showModal() also made the page behind
        // inert for the KEYBOARD; the scrim blocks pointer only. So focus
        // stays in the topmost opted-in dialog: Tab/Shift+Tab wrap inside
        // it, and focus that lands on the page behind is pulled back. The
        // status bar, the rail's Lock and the on-screen keyboard stay
        // focusable (the rule this card enforces). The wedge scanner's
        // window keydown buffer never depended on focus.
        // ut-docs#3212: a dialog that wants the trap but NOT Escape-to-close
        // opts in with data-ut-focus-trap instead (data-ut-escape-close
        // implies the trap).
        function escTop() {
          scrimSync();
          var top = scrimSet[scrimSet.length - 1];
          return top && top.tagName === 'DIALOG' &&
            (top.hasAttribute('data-ut-escape-close') || top.hasAttribute('data-ut-focus-trap')) ? top : null;
        }
        function escFocusables(d) {
          return Array.prototype.filter.call(
            d.querySelectorAll('a[href], button, input, select, textarea, summary, [tabindex]'),
            function (el) { return !el.disabled && el.tabIndex >= 0 && el.getClientRects().length > 0; });
        }
        function escAllowed(t) {
          // #bugreport-panel (data-ut-no-scrim) stays usable while the
          // operator reproduces the problem (ut-docs#3212 review). The phone ☰
          // stays reachable over a popup so Lock can be (ut-docs#6118).
          return !!(t && t.closest && t.closest('.statusbar, .session-lock, .nav-drawer-toggle, #osk, #bugreport-panel'));
        }
        // record-dialog.js's focusin backstop uses the same allow-list (ut-docs#4078).
        UT.focusTrapExempt = escAllowed;
        window.addEventListener('keydown', function (e) {
          if (e.key !== 'Tab' || e.defaultPrevented) return;
          try {
            var top = escTop();
            if (!top) return;
            var list = escFocusables(top);
            if (!list.length) { e.preventDefault(); return; }
            var idx = list.indexOf(document.activeElement);
            if (e.shiftKey) {
              if (idx <= 0) { e.preventDefault(); list[list.length - 1].focus(); }
            } else if (idx === -1 || idx === list.length - 1) {
              e.preventDefault(); list[0].focus();
            }
          } catch (err) { /* focus only */ }
        });
        document.addEventListener('focusin', function (e) {
          try {
            var top = escTop();
            var t = e.target;
            if (!top || !t || top.contains(t) || escAllowed(t)) return;
            if (UT.dialogOpening && UT.dialogOpening.contains(t)) return;
            // ut-docs#4077: a dialog may name a neutral home for stray
            // focus (the age-check sheet's prompt line), so it never lands
            // on a link or an action a stray Enter would trigger.
            var home = top.querySelector('[data-ut-focus-home]');
            if (home && home.getClientRects().length > 0) { home.focus(); return; }
            var list = escFocusables(top);
            if (list.length) list[0].focus();
          } catch (err) { /* focus only */ }
        });
        // A modal dialog opted in with data-ut-scrim-dismiss closes on a tap
        // on its ::backdrop: the event's target is the dialog itself and
        // the point lies outside its box (a press that started inside and
        // was dragged out does not count).
        var backdropDown = null;
        function onBackdrop(e) {
          var d = e.target;
          if (!d || d.tagName !== 'DIALOG' || !d.open || !d.hasAttribute('data-ut-scrim-dismiss')) return false;
          try { if (!d.matches(':modal')) return false; } catch (err) { return false; }
          var r = d.getBoundingClientRect();
          return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
        }
        document.addEventListener('pointerdown', function (e) {
          try { backdropDown = onBackdrop(e) ? e.target : null; } catch (err) { backdropDown = null; }
        }, true);
        document.addEventListener('click', function (e) {
          try {
            var d = backdropDown;
            backdropDown = null;
            if (d && d === e.target && onBackdrop(e)) d.close();
          } catch (err) { /* scrim only */ }
        }, true);
        UT.popupOpened = function (el) {
          try { endClosing(el); UT.finishZooms(); popupOpen(el); } catch (e) { /* motion only */ }
          scrimTrack(el);
        };
        UT.popupClosed = function (el) {
          try { popupClose(el); } catch (e) { /* motion only */ }
          // ut-docs#2972: a dialog that opts in with data-ut-clear-on-close
          // (the self-order kiosk's picker shell) is emptied AFTER the
          // closing shrink -- never at the close site, which would hand the
          // shrink a blank element. No shrink running (Light, reduced
          // motion, no box): empty it now.
          try {
            if (el && el.hasAttribute && el.hasAttribute('data-ut-clear-on-close') && !el.open) {
              var cl = popupState(el).closing;
              if (cl) cl.clearKids = Array.prototype.slice.call(el.childNodes);
              else el.replaceChildren();
            }
          } catch (e) { /* clearing only */ }
          try { var i = scrimSet.indexOf(el); if (i >= 0) scrimSet.splice(i, 1); } catch (e) { /* scrim only */ }
          scrimSync();
        };
        // `close` does not bubble: capture phase on document sees every
        // dialog's. A dialog reopened before the event was delivered stays.
        document.addEventListener('close', function (e) {
          var el = e.target;
          if (!el || el.tagName !== 'DIALOG' || el.open) return;
          UT.popupClosed(el);
        }, true);
        // Opening: one wrapper, installed once. It first ends this popup's
        // own closing shrink and finishes a running pane zoom (ADR-0122 §4:
        // no position:fixed dialog is ever shown inside a transformed pane),
        // then runs the native method FIRST and unchanged -- the dialog is
        // open and interactive at once, and a motion bug can only cost the
        // motion, never the dialog -- then starts the zoom.
        if (window.HTMLDialogElement) ['showModal', 'show'].forEach(function (m) {
          var native = HTMLDialogElement.prototype[m];
          if (typeof native !== 'function' || native.utWrapped) return;
          var wrapped = function () {
            var was = this.open;
            try { endClosing(this); UT.finishZooms(); } catch (e) { /* motion only */ }
            // ut-docs#3212 (review): native show() focuses the new dialog
            // BEFORE scrimTrack makes it topmost; the focus trap's focusin
            // pull-back must not drag that focus back into the trapped
            // dialog underneath.
            var prevOpening = UT.dialogOpening;
            UT.dialogOpening = this;
            try {
              var ret = native.apply(this, arguments);
            } finally { UT.dialogOpening = prevOpening; }
            if (!was) { try { popupOpen(this); } catch (e) { /* motion only */ } }
            if (m === 'show') scrimTrack(this);
            return ret;
          };
          wrapped.utWrapped = true;
          HTMLDialogElement.prototype[m] = wrapped;
        });
        // end ADR-0122 §5 popup motion
        UT.vtWatchdog = function (vt) {
          if (!vt || !vt.skipTransition) return;
          var guard = null;
          activeVT = vt;
          function skip() { vt.skipTransition(); }
          function clear() { clearTimeout(backstop); clearTimeout(guard); if (activeVT === vt) activeVT = null; }
          var backstop = setTimeout(skip, 2000);
          if (vt.ready && vt.ready.then) vt.ready.then(function () { clearTimeout(backstop); guard = setTimeout(skip, 1000); }, clear);
          if (vt.finished && vt.finished.then) vt.finished.then(clear, clear);
        };
        // The page-zoom code that stayed in base.html needs these two.
        UT.viewport = viewport;
        UT.onScreen = onScreen;
})();
