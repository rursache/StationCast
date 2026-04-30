(function() {
  const player = document.getElementById('player');
  const playBtn = document.getElementById('play');
  const vol = document.getElementById('vol');
  const titleEl = document.getElementById('title');
  const artistEl = document.getElementById('artist');
  const nextEl = document.getElementById('next');
  const artEl = document.getElementById('art');
  const artFallback = document.getElementById('art-fallback');
  const listenersEl = document.getElementById('listeners');

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
    playBtn.innerHTML = playing ? '&#10074;&#10074;' : '&#9654;';
    playBtn.setAttribute('aria-label', playing ? 'Pause' : 'Play');
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

  function applyNowPlaying(np) {
    titleEl.textContent = np.title || 'Idle';
    artistEl.textContent = np.artist || '';
    if (np.next_title) {
      nextEl.textContent = 'Up next: ' + (np.next_artist ? np.next_artist + ' - ' : '') + np.next_title;
    } else {
      nextEl.textContent = '';
    }
    if (np.has_art && np.art_url) {
      artEl.src = np.art_url + '?v=' + encodeURIComponent(np.title || '');
      artEl.classList.remove('hidden');
      artFallback.classList.add('hidden');
    } else {
      artEl.classList.add('hidden');
      artFallback.classList.remove('hidden');
    }
    listenersEl.textContent = (np.listeners || 0) + ' listeners';
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
    es.onmessage = (e) => {
      try { applyNowPlaying(JSON.parse(e.data)); } catch {}
    };
    es.onerror = () => {
      es.close();
      setTimeout(connectSSE, 3000);
    };
  }
  connectSSE();
})();
