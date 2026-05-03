(function() {
  const player = document.getElementById('player');
  const playBtn = document.getElementById('play');
  const playIcon = document.getElementById('play-icon');
  const pauseIcon = document.getElementById('pause-icon');
  const vol = document.getElementById('vol');
  const titleEl = document.getElementById('title');
  const artistEl = document.getElementById('artist');
  const nextEl = document.getElementById('next');
  const artEl = document.getElementById('art');
  const artFallback = document.getElementById('art-fallback');
  const listenersEl = document.getElementById('listeners');
  const backdrop = document.getElementById('backdrop');
  const extLinks = document.getElementById('external-links');
  const linkApple = document.getElementById('link-apple');
  const linkSpotify = document.getElementById('link-spotify');
  const linkYTM = document.getElementById('link-ytm');

  const ua = navigator.userAgent;
  const isApple = /iPhone|iPad|iPod|Macintosh/.test(ua) && /Safari/.test(ua) && !/Chrome|CriOS|FxiOS/.test(ua);
  const isiOS = /iPhone|iPad|iPod/.test(ua);

  function setSource() {
    if (isiOS || (isApple && player.canPlayType('application/vnd.apple.mpegurl'))) {
      player.src = '/hls.m3u8';
    } else {
      player.src = '/stream';
    }
  }
  setSource();

  const VOL_KEY = 'stationcast.volume';
  const savedVol = parseInt(localStorage.getItem(VOL_KEY), 10);
  if (Number.isFinite(savedVol) && savedVol >= 0 && savedVol <= 100) {
    vol.value = String(savedVol);
  }
  player.volume = (parseInt(vol.value, 10) || 100) / 100;
  vol.addEventListener('input', () => {
    player.volume = vol.value / 100;
    try { localStorage.setItem(VOL_KEY, vol.value); } catch {}
  });

  function setPlayState(playing) {
    if (playing) {
      playIcon.classList.add('hidden');
      pauseIcon.classList.remove('hidden');
      playBtn.setAttribute('aria-label', 'Pause');
    } else {
      pauseIcon.classList.add('hidden');
      playIcon.classList.remove('hidden');
      playBtn.setAttribute('aria-label', 'Play');
    }
  }
  setPlayState(false);

  playBtn.addEventListener('click', async () => {
    if (player.paused) {
      try {
        setSource();
        player.load();
        await player.play();
        setPlayState(true);
      } catch (e) {
        setPlayState(false);
      }
    } else {
      player.pause();
      setPlayState(false);
    }
  });
  player.addEventListener('play', () => setPlayState(true));
  player.addEventListener('pause', () => setPlayState(false));
  player.addEventListener('ended', () => setPlayState(false));

  attachAudioWatchdog(player, () => {
    try { setSource(); player.load(); player.play().catch(() => {}); } catch {}
  });

  let lastArtURL = '';
  function applyNowPlaying(np) {
    titleEl.textContent = np.title || 'Off air';
    artistEl.textContent = np.artist || '';
    if (np.next_title) {
      nextEl.textContent = 'Up next  ·  ' + (np.next_artist ? np.next_artist + ' — ' : '') + np.next_title;
    } else {
      nextEl.textContent = '';
    }
    if (np.has_art && np.art_url) {
      const url = np.art_url + '?v=' + encodeURIComponent(np.title || '');
      if (url !== lastArtURL) {
        artEl.src = url;
        artEl.classList.remove('hidden');
        artFallback.classList.add('hidden');
        if (backdrop) {
          backdrop.style.backgroundImage = `url(${url})`;
          backdrop.style.backgroundSize = 'cover';
          backdrop.style.backgroundPosition = 'center';
          backdrop.style.filter = 'blur(80px) saturate(1.2) brightness(0.5)';
          backdrop.style.opacity = '0.65';
        }
        lastArtURL = url;
      }
    } else {
      artEl.classList.add('hidden');
      artFallback.classList.remove('hidden');
      if (backdrop) {
        backdrop.style.opacity = '0';
        backdrop.style.backgroundImage = '';
      }
      lastArtURL = '';
    }
    if (extLinks) {
      const q = [np.artist, np.title].filter(Boolean).join(' ').trim();
      if (q && np.title) {
        const enc = encodeURIComponent(q);
        if (linkApple) linkApple.href = 'https://music.apple.com/search?term=' + enc;
        if (linkSpotify) linkSpotify.href = 'https://open.spotify.com/search/' + enc;
        if (linkYTM) linkYTM.href = 'https://music.youtube.com/search?q=' + enc;
        extLinks.classList.remove('hidden');
        extLinks.classList.add('flex');
      } else {
        extLinks.classList.add('hidden');
        extLinks.classList.remove('flex');
      }
    }
    if (listenersEl) listenersEl.textContent = (np.listeners || 0);
    if ('mediaSession' in navigator) {
      navigator.mediaSession.metadata = new MediaMetadata({
        title: np.title || np.station_name,
        artist: np.artist || '',
        album: np.album || np.station_name,
        artwork: np.has_art && np.art_url ? [{ src: np.art_url, sizes: '512x512', type: 'image/jpeg' }] : []
      });
    }
  }

  fetch('/now-playing').then(r => r.json()).then(applyNowPlaying).catch(() => {});
  connectSSE('/now-playing/sse', applyNowPlaying);

  bindCopyButtons();
  bindModalDismiss();

  document.getElementById('open-streams')?.addEventListener('click', () => {
    openModal(document.getElementById('modal-streams'));
  });

  document.getElementById('open-history')?.addEventListener('click', async () => {
    const modal = document.getElementById('modal-history');
    const list = document.getElementById('history-list');
    if (list) list.innerHTML = `<li class="text-neutral-500 italic px-2 py-2">Loading…</li>`;
    openModal(modal);
    try {
      const r = await fetch('/history', { cache: 'no-store' });
      if (!r.ok) throw new Error('fetch failed');
      const items = await r.json();
      if (!Array.isArray(items) || items.length === 0) {
        if (list) list.innerHTML = `<li class="text-neutral-500 italic px-2 py-2">No tracks have played yet</li>`;
        return;
      }
      if (list) {
        list.innerHTML = items.map(t => `
          <li class="flex items-center gap-3 bg-neutral-950/40 rounded-lg px-3 py-2">
            <div class="w-9 h-9 rounded bg-neutral-800 flex-shrink-0 overflow-hidden flex items-center justify-center">
              ${t.has_art && t.art_url
                ? `<img src="${escapeHTML(t.art_url)}" alt="" class="w-full h-full object-cover">`
                : `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="w-4 h-4 text-neutral-600"><path stroke-linecap="round" stroke-linejoin="round" d="M9 19V5l12-2v14"/><circle cx="6" cy="19" r="3" stroke-linecap="round" stroke-linejoin="round"/><circle cx="18" cy="16" r="3" stroke-linecap="round" stroke-linejoin="round"/></svg>`}
            </div>
            <div class="min-w-0 flex-1">
              <div class="overflow-x-auto whitespace-nowrap no-scrollbar">${escapeHTML(t.title) || '<span class="text-neutral-500">untitled</span>'}</div>
              <div class="text-xs text-neutral-500 overflow-x-auto whitespace-nowrap no-scrollbar">${escapeHTML(t.artist || '')}</div>
            </div>
          </li>`).join('');
      }
    } catch {
      if (list) list.innerHTML = `<li class="text-rose-400 italic px-2 py-2">Could not load history</li>`;
    }
  });
})();
