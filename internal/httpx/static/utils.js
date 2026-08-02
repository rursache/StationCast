// Shared frontend helpers used by both the public player and the admin
// dashboard. Loaded as a plain script before page-specific code so its
// declarations land on the global scope

function escapeHTML(s) {
  return (s || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

// Watch an <audio> element's currentTime advancement while playing. If it
// freezes for >6s, call reload() to force a reconnect by reassigning src.
// Also re-arms on error/stalled events. Returns nothing; the watchdog
// runs for the lifetime of the page
function attachAudioWatchdog(audio, reload) {
  let lastT = 0;
  let stuckSince = 0;
  setInterval(() => {
    if (audio.paused) { stuckSince = 0; lastT = audio.currentTime; return; }
    if (audio.currentTime !== lastT) {
      lastT = audio.currentTime;
      stuckSince = 0;
      return;
    }
    if (stuckSince === 0) stuckSince = Date.now();
    else if (Date.now() - stuckSince > 6000) { stuckSince = 0; reload(); }
  }, 1000);
  audio.addEventListener('error', () => { if (!audio.paused) reload(); });
  audio.addEventListener('stalled', () => { stuckSince = stuckSince || Date.now(); });
}

// Click-to-copy. Bind once on any container; descendants matching
// `button.copy[data-copy]` copy to clipboard and animate a `.copy-icon`
// child from clipboard to check for ~1.2s
const COPY_CHECK_SVG = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="w-4 h-4"><path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/></svg>`;

function bindCopyButtons(root) {
  (root || document).querySelectorAll('button.copy:not([data-copy-bound])').forEach(b => {
    b.dataset.copyBound = '1';
    b.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(b.dataset.copy);
        const icon = b.querySelector('.copy-icon');
        if (!icon || icon.dataset.busy) return;
        const orig = icon.innerHTML;
        icon.dataset.busy = '1';
        icon.innerHTML = COPY_CHECK_SVG;
        icon.classList.add('text-emerald-400');
        setTimeout(() => {
          icon.innerHTML = orig;
          icon.classList.remove('text-emerald-400');
          delete icon.dataset.busy;
        }, 1200);
      } catch {}
    });
  });
}

// Modal open/close. Modal wrappers toggle .hidden + .flex
function openModal(el) {
  if (!el) return;
  el.classList.remove('hidden');
  el.classList.add('flex');
  el.setAttribute('aria-hidden', 'false');
}
function closeModal(el) {
  if (!el) return;
  el.classList.add('hidden');
  el.classList.remove('flex');
  el.setAttribute('aria-hidden', 'true');
}
function bindModalDismiss() {
  document.querySelectorAll('[id^="modal-"]').forEach(m => {
    m.querySelectorAll('.modal-close, .modal-backdrop').forEach(el => {
      el.addEventListener('click', () => closeModal(m));
    });
  });
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      document.querySelectorAll('[id^="modal-"]').forEach(closeModal);
    }
  });
}

// SSE reconnect wrapper. Calls onMessage(parsed) on every data: payload and
// auto-reconnects 3s after any error
function connectSSE(url, onMessage) {
  let es;
  (function connect() {
    es = new EventSource(url);
    es.onmessage = (e) => { try { onMessage(JSON.parse(e.data)); } catch {} };
    es.onerror = () => { es.close(); setTimeout(connect, 3000); };
  })();
}

// --- Lyrics -----------------------------------------------------------
// Lyrics arrive as LRC when a synced version exists and as plain text
// otherwise, so the format is detected rather than declared.

// parseLyrics turns a body into ordered lines. `synced` is false when no
// timestamp was found, in which case the lines are just text to scroll.
// A line may carry several timestamps (a repeated chorus), which becomes one
// entry per occurrence. Metadata tags like [ar:...] are dropped
function parseLyrics(text) {
  const lines = [];
  let synced = false;
  // Normalise line endings first. A trailing \r would defeat the `$` anchor
  // below and silently turn every line of a CRLF file into plain text
  for (const raw of (text || '').replace(/\r\n?/g, '\n').split('\n')) {
    const m = raw.match(/^((?:\[\d+:\d+(?:[.:]\d+)?\])+)(.*)$/);
    if (m) {
      synced = true;
      const body = m[2].trim();
      for (const ts of m[1].matchAll(/\[(\d+):(\d+(?:[.:]\d+)?)\]/g)) {
        lines.push({ t: parseInt(ts[1], 10) * 60 + parseFloat(ts[2].replace(':', '.')), text: body });
      }
    } else if (!/^\[(ti|ar|al|au|by|re|ve|length|offset|tool|encoder):/i.test(raw)) {
      // Blank lines are kept so stanza breaks survive in plain lyrics
      lines.push({ t: null, text: raw.trim() });
    }
  }
  if (synced) {
    // Mixing untimed lines into a synced document breaks the binary search
    // invariant, so they are dropped rather than sorted to an arbitrary spot
    const timed = lines.filter(l => l.t !== null);
    timed.sort((a, b) => a.t - b.t);
    return { synced, lines: timed };
  }
  return { synced, lines };
}

// activeLyricIndex finds the last line due at or before `pos`, by binary
// search so it stays cheap when called every second
function activeLyricIndex(lines, pos) {
  let lo = 0, hi = lines.length - 1, found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (lines[mid].t !== null && lines[mid].t <= pos) { found = mid; lo = mid + 1; }
    else { hi = mid - 1; }
  }
  return found;
}

// A listener hears audio several seconds after the server sent it: the
// connect burst, the browser's own jitter buffer, and for HLS the segment
// length all stack up, and the total differs per listener and per device.
// There is no browser API that reports it, so the default is an estimate and
// the listener can correct it. Kept per browser, like the volume setting
const LYRICS_OFFSET_KEY = 'stationcast.lyricsOffset';
// Measured against real playback rather than derived from the buffer sizes,
// which over-predict the lag badly
const LYRICS_OFFSET_DEFAULT = -0.5;
const LYRICS_OFFSET_MIN = -5;
const LYRICS_OFFSET_MAX = 30;

function getLyricsOffset() {
  // Reading storage can throw outright, not just return null, in a sandboxed
  // frame or with storage disabled. This runs during page setup, so an
  // exception here would take everything wired after it down with it
  try {
    const v = parseFloat(localStorage.getItem(LYRICS_OFFSET_KEY));
    return Number.isFinite(v) ? Math.min(LYRICS_OFFSET_MAX, Math.max(LYRICS_OFFSET_MIN, v)) : LYRICS_OFFSET_DEFAULT;
  } catch {
    return LYRICS_OFFSET_DEFAULT;
  }
}
function setLyricsOffset(v) {
  const c = Math.min(LYRICS_OFFSET_MAX, Math.max(LYRICS_OFFSET_MIN, v));
  try { localStorage.setItem(LYRICS_OFFSET_KEY, String(c)); } catch {}
  return c;
}

// initLyrics wires a lyrics button, modal and offset control. `getNowPlaying`
// returns the latest now-playing payload so the view can follow track changes
// and compute elapsed time from started_at
function initLyrics(els, getNowPlaying) {
  if (!els.button || !els.modal || !els.body) return null;

  let parsed = null;
  let loadedUrl = null;
  let activeIdx = -1;
  let offset = getLyricsOffset();

  function renderOffset() {
    if (els.offsetLabel) els.offsetLabel.textContent = offset.toFixed(1) + 's';
  }

  function render() {
    if (!parsed) return;
    els.body.innerHTML = parsed.lines
      .map((l, i) => `<p class="lyric-line${l.text ? '' : ' lyric-blank'}" data-i="${i}">${escapeHTML(l.text)}</p>`)
      .join('');
    activeIdx = -1;
    if (els.syncControls) els.syncControls.classList.toggle('hidden', !parsed.synced);
  }

  function highlight() {
    if (!parsed || !parsed.synced) return;
    const np = getNowPlaying();
    if (!np || !np.started_at) return;
    const pos = Date.now() / 1000 - np.started_at - offset;
    const idx = activeLyricIndex(parsed.lines, pos);
    if (idx === activeIdx) return;
    activeIdx = idx;
    const nodes = els.body.querySelectorAll('.lyric-line');
    nodes.forEach((n, i) => n.classList.toggle('lyric-active', i === idx));
    const cur = nodes[idx];
    if (cur && els.modal.classList.contains('flex')) {
      cur.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }

  // Guards against a slow request for a previous track resolving after a
  // faster one for the current track and overwriting it
  let loadSeq = 0;

  async function load(url) {
    if (!url || url === loadedUrl) return;
    const seq = ++loadSeq;
    try {
      const r = await fetch(url, { credentials: 'same-origin' });
      if (!r.ok) throw new Error('no lyrics');
      const text = await r.text();
      if (seq !== loadSeq) return; // a newer track won, discard this one
      parsed = parseLyrics(text);
      loadedUrl = url;
      render();
    } catch {
      if (seq !== loadSeq) return;
      parsed = null;
      loadedUrl = null;
      els.body.innerHTML = `<p class="text-neutral-500 italic">Could not load lyrics</p>`;
    }
  }

  // Only offered when the server actually has lyrics for this track
  function sync(np) {
    const has = !!(np && np.has_lyrics && np.lyrics_url);
    els.button.classList.toggle('hidden', !has);
    if (!has) {
      closeModal(els.modal);
      parsed = null;
      loadedUrl = null;
      return;
    }
    if (np.lyrics_url !== loadedUrl) load(np.lyrics_url);
  }

  els.button.addEventListener('click', async () => {
    const np = getNowPlaying();
    if (np && np.lyrics_url) await load(np.lyrics_url);
    openModal(els.modal);
    highlight();
  });

  const nudge = d => () => { offset = setLyricsOffset(offset + d); renderOffset(); activeIdx = -1; highlight(); };
  els.minus?.addEventListener('click', nudge(-0.5));
  els.plus?.addEventListener('click', nudge(0.5));

  renderOffset();
  setInterval(highlight, 500);
  return { sync };
}

// --- Sortable list ----------------------------------------------------
// Pointer events rather than HTML5 drag and drop, which does not fire on
// touch at all. One implementation covers mouse, trackpad and finger.
//
// The row being dragged follows the pointer directly, and the rows it passes
// slide out of its way. Reordering the DOM mid-drag instead would make the
// row jump between slots rather than track the finger, which reads as the
// list flickering rather than as picking something up.
//
// onMove(from, to, el) fires once on release, with the original and final
// index. isBusy()/dragging() lets the caller hold off a re-render, since
// replacing the list mid-drag would pull the row out from under the pointer
function makeSortable(list, onMove) {
  if (!list) return { dragging: () => false };

  let el = null;          // the row being dragged
  let startIndex = -1;
  let targetIndex = -1;
  let startY = 0;
  let step = 0;           // row pitch, height plus the gap between rows
  let pointerId = null;
  let others = [];

  const rows = () => [...list.children];

  function begin(handle, e) {
    if (el) return;
    const row = handle.closest('[data-sortable-item]');
    if (!row) return;
    const all = rows();
    startIndex = all.indexOf(row);
    if (startIndex < 0) return;

    el = row;
    targetIndex = startIndex;
    startY = e.clientY;
    pointerId = e.pointerId;

    // Pitch measured from the live layout rather than assumed, so the gap
    // between rows does not have to be hard coded here
    const a = all[0].getBoundingClientRect();
    step = all.length > 1 ? all[1].getBoundingClientRect().top - a.top : a.height;

    others = all.filter(r => r !== el);
    others.forEach(r => r.classList.add('sort-shifting'));
    el.classList.add('sorting');

    try { handle.setPointerCapture(pointerId); } catch {}
    e.preventDefault();
  }

  function move(e) {
    if (!el || e.pointerId !== pointerId) return;
    e.preventDefault();

    const dy = e.clientY - startY;
    el.style.transform = 'translateY(' + dy + 'px)';

    // Which slot the row would land in if released now
    const n = rows().length;
    let idx = startIndex + (step ? Math.round(dy / step) : 0);
    idx = Math.max(0, Math.min(n - 1, idx));
    if (idx === targetIndex) return;
    targetIndex = idx;

    // Rows between the origin and the target slide one place to make the gap
    others.forEach(r => {
      const i = rows().indexOf(r);
      let shift = 0;
      if (startIndex < targetIndex && i > startIndex && i <= targetIndex) shift = -step;
      else if (startIndex > targetIndex && i >= targetIndex && i < startIndex) shift = step;
      r.style.transform = shift ? 'translateY(' + shift + 'px)' : '';
    });
  }

  // apply is false when the browser took the gesture away rather than the
  // user finishing it, in which case the row goes back where it started
  function finish(e, apply) {
    if (!el || (e && e.pointerId !== pointerId)) return;
    const row = el;
    const from = startIndex;
    const to = targetIndex;

    row.classList.remove('sorting');
    row.style.transform = '';
    others.forEach(r => { r.classList.remove('sort-shifting'); r.style.transform = ''; });

    el = null;
    others = [];
    startIndex = -1;
    targetIndex = -1;
    pointerId = null;

    if (!apply || to < 0 || to === from) return;
    // The DOM moves once, on release, so the row lands where it was dropped
    const all = rows();
    if (to > from) list.insertBefore(row, all[to].nextSibling);
    else list.insertBefore(row, all[to]);
    onMove(from, to, row);
  }

  list.addEventListener('pointerdown', e => {
    const handle = e.target.closest('[data-sort-handle]');
    if (handle && list.contains(handle)) begin(handle, e);
  });
  list.addEventListener('pointermove', move);
  list.addEventListener('pointerup', e => finish(e, true));
  list.addEventListener('pointercancel', e => finish(e, false));
  // The only event guaranteed to fire whenever capture ends. Without it a
  // capture that goes away without a pointerup wedges the drag on forever,
  // and a wedged drag holds off every later refresh of the pane
  list.addEventListener('lostpointercapture', e => finish(e, false));

  // Keyboard equivalent, so reordering does not require a pointer at all
  list.addEventListener('keydown', e => {
    const handle = e.target.closest('[data-sort-handle]');
    if (!handle) return;
    const dir = e.key === 'ArrowUp' ? -1 : e.key === 'ArrowDown' ? 1 : 0;
    if (!dir) return;
    e.preventDefault();
    const row = handle.closest('[data-sortable-item]');
    const all = rows();
    const from = all.indexOf(row);
    const to = from + dir;
    if (to < 0 || to >= all.length) return;
    if (dir < 0) list.insertBefore(row, all[to]);
    else list.insertBefore(row, all[to].nextSibling);
    handle.focus();
    onMove(from, to, row);
  });

  return { dragging: () => el !== null };
}
