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
