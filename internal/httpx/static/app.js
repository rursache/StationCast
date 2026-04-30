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

  player.volume = (parseInt(vol.value, 10) || 80) / 100;
  vol.addEventListener('input', () => { player.volume = vol.value / 100; });

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

  let es;
  function connectSSE() {
    es = new EventSource('/now-playing/sse');
    es.onmessage = (e) => { try { applyNowPlaying(JSON.parse(e.data)); } catch {} };
    es.onerror = () => { es.close(); setTimeout(connectSSE, 3000); };
  }
  connectSSE();

  // Copy-to-clipboard for tune-in URLs
  document.querySelectorAll('button.copy').forEach(b => {
    b.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(b.dataset.copy);
        const orig = b.textContent;
        b.textContent = 'copied';
        b.classList.add('text-emerald-400');
        setTimeout(() => { b.textContent = orig; b.classList.remove('text-emerald-400'); }, 1200);
      } catch {}
    });
  });
})();
