<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api } from '../lib/api'
  import { t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'
  import { connectRoom } from '../lib/ws'
  import type { QueueItem, RoomState } from '../lib/types'

  let { roomId }: { roomId: string } = $props()

  const INTRO_MS = 5000
  const MAX_DRIFT_S = 0.3

  type Phase = 'checking' | 'playing' | 'kicked' | 'notFound'
  let phase = $state<Phase>('checking')
  let room = $state<RoomState | null>(null)
  let roomUrl = $state('')
  let video = $state<HTMLVideoElement>()
  let original = $state<HTMLAudioElement>()

  // The item whose video is loaded, and whether its "up next" intro is showing.
  let loaded = $state<QueueItem | null>(null)
  let intro = $state<QueueItem | null>(null)

  // Primitive deriveds only notify on real changes, not on every snapshot.
  // First connected player is active; the others wait in line and play nothing.
  const position = $derived(room?.self?.position ?? -1)
  const active = $derived(position === 0)
  const currentId = $derived(active ? (room?.current?.id ?? null) : null)
  const paused = $derived(room?.player.paused ?? false)
  const volume = $derived(room?.player.volume ?? 100)
  const restartNonce = $derived(room?.player.restartNonce ?? 0)
  const showQR = $derived(room?.player.showQR ?? true)
  const vocal = $derived(room?.player.vocal ?? false)
  const upNext = $derived(room?.queue[0] ?? null)
  const qrSrc = $derived(`/api/rooms/${roomId}/qr.png`)

  // No start screen: connect right away. Browsers only allow sound and fullscreen
  // after a user gesture, so the first tap anywhere unlocks both (see unlock()).
  // Kiosk-mode browsers (see README) need no tap at all.
  onMount(async () => {
    try {
      const info = await api<{ url: string }>('GET', `/api/rooms/${roomId}`)
      roomUrl = info.url
      phase = 'playing'
    } catch {
      phase = 'notFound'
    }
  })

  function reconnect() {
    room = null
    phase = 'playing'
    unlock()
  }

  $effect(() => {
    if (phase !== 'playing') return
    return connectRoom(`/api/rooms/${roomId}/player/ws`, {
      onState: (s) => (room = s),
      onTerminal: (reason) => (phase = reason === 'kicked' ? 'kicked' : 'notFound'),
    })
  })

  // ---- fullscreen ----
  // Kiosk mode fills the screen without the Fullscreen API, so also compare sizes.
  const isFullscreen = () =>
    !!document.fullscreenElement || (innerWidth >= screen.width - 1 && innerHeight >= screen.height - 1)
  let fullscreen = $state(isFullscreen())
  $effect(() => {
    const update = () => (fullscreen = isFullscreen())
    document.addEventListener('fullscreenchange', update)
    addEventListener('resize', update)
    return () => {
      document.removeEventListener('fullscreenchange', update)
      removeEventListener('resize', update)
    }
  })

  function unlock() {
    if (!document.fullscreenElement) document.documentElement.requestFullscreen?.().catch(() => {})
    if (blocked) tryPlay()
    syncOriginal()
  }

  // ---- keep the screen awake ----
  // The Wake Lock API needs HTTPS (or localhost). Elsewhere, fall back to a tiny
  // muted looping video, which browsers treat as active playback.
  let wakeFallback = $state(!('wakeLock' in navigator))
  $effect(() => {
    if (phase !== 'playing' || wakeFallback) return
    let lock: WakeLockSentinel | null = null
    const acquire = () => {
      if (document.visibilityState !== 'visible') return
      navigator.wakeLock
        .request('screen')
        .then((l) => (lock = l))
        .catch(() => (wakeFallback = true))
    }
    acquire()
    document.addEventListener('visibilitychange', acquire)
    return () => {
      document.removeEventListener('visibilitychange', acquire)
      lock?.release()
    }
  })

  // ---- playback ----

  // New current song: show the intro card, then load the video.
  $effect(() => {
    const id = currentId
    if (phase !== 'playing') return
    if (id === null) {
      loaded = null
      intro = null
      return
    }
    if (untrack(() => loaded?.id) === id) return
    const item = untrack(() => room!.current!)
    loaded = null
    intro = item
    const timer = setTimeout(() => {
      intro = null
      loaded = item
    }, INTRO_MS)
    return () => clearTimeout(timer)
  })

  $effect(() => {
    if (!video || !loaded) return
    if (paused) video.pause()
    else tryPlay()
  })

  $effect(() => {
    const v = volume / 100
    if (video) video.volume = v
    if (original) original.volume = v
  })

  let lastNonce: number | null = null
  $effect(() => {
    const n = restartNonce
    if (lastNonce !== null && n !== lastNonce && video && loaded) {
      video.currentTime = 0
      tryPlay()
    }
    lastNonce = n
  })

  // Autoplay can still be refused (no gesture yet); a tap anywhere resumes.
  let blocked = $state(false)
  function tryPlay() {
    video
      ?.play()
      .then(() => (blocked = false))
      .catch((e: DOMException) => {
        if (e.name === 'NotAllowedError') blocked = true
      })
  }

  // ---- original vocals: karaoke video + audio from the original-vocal file ----
  // Both files are assumed to share a timeline. The original's audio follows the
  // video's clock; the video is muted only once the original is actually audible.
  let originalPlaying = $state(false)

  function syncOriginal() {
    if (!original || !video) return
    if (!vocal || video.paused || video.ended) {
      if (!original.paused) original.pause()
      return
    }
    if (Math.abs(original.currentTime - video.currentTime) > MAX_DRIFT_S) original.currentTime = video.currentTime
    if (original.paused) original.play().catch(() => {})
  }

  $effect(() => {
    void vocal
    void original
    syncOriginal()
  })

  $effect(() => {
    if (video) video.muted = vocal && originalPlaying
  })

  function report(failed: boolean) {
    const item = loaded
    const secret = room?.self?.secret
    if (!item || !secret) return
    if (failed) toast(t('player.playError'), 'error')
    api('POST', `/api/rooms/${roomId}/player/ended`, { playerSecret: secret, itemId: item.id, failed }).catch(() => {})
  }
</script>

{#if phase === 'checking'}
  <main class="center-screen"><p class="muted">{t('common.loading')}</p></main>
{:else if phase === 'notFound'}
  <main class="center-screen"><h2>{t('room.notFound')}</h2></main>
{:else if phase === 'kicked'}
  <main class="center-screen">
    <div class="narrow">
      <h2>{t('player.kicked')}</h2>
      <button class="primary" onclick={reconnect}>{t('player.reconnect')}</button>
    </div>
  </main>
{:else}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="stage" onclick={unlock}>
    {#if room && !active}
      <div class="idle">
        <h1>{room.settings.name}</h1>
        <p class="idle-hint">{t('player.waiting', { n: position })}</p>
        <p class="url">{t('player.waitingHint')}</p>
      </div>
    {:else}
      {#if loaded}
        <!-- svelte-ignore a11y_media_has_caption -->
        <video
          bind:this={video}
          src={`/media/${loaded.songId}`}
          autoplay
          playsinline
          onended={() => {
            syncOriginal()
            report(false)
          }}
          onerror={() => report(true)}
          onplay={syncOriginal}
          onpause={syncOriginal}
          onseeked={syncOriginal}
          ontimeupdate={syncOriginal}
        ></video>
        {#if loaded.hasOriginal}
          {#key loaded.id}
            <audio
              bind:this={original}
              src={`/media/${loaded.songId}/original`}
              preload="metadata"
              onloadedmetadata={syncOriginal}
              onplaying={() => (originalPlaying = true)}
              onpause={() => (originalPlaying = false)}
              onwaiting={() => (originalPlaying = false)}
              onerror={() => (originalPlaying = false)}
            ></audio>
          {/key}
        {/if}
      {/if}

      {#if intro}
        <div class="intro">
          <div class="kicker">{t('player.upNext')}</div>
          <div class="intro-title">{intro.title}</div>
          <div class="intro-artist">{intro.artist}</div>
          <div class="intro-singer">{t('player.singer', { name: intro.nickname })}</div>
        </div>
      {:else if !room?.current}
        <div class="idle">
          <h1>{room?.settings.name ?? ''}</h1>
          <img src={qrSrc} alt="QR code" width="320" height="320" />
          <p class="idle-hint">{t('player.idleTitle')}</p>
          <p class="url">{roomUrl}</p>
        </div>
      {/if}

      {#if loaded && blocked}
        <button class="primary start resume" onclick={tryPlay}>▶ {t('player.start')}</button>
      {/if}

      {#if loaded && showQR}
        <aside class="overlay">
          <img src={qrSrc} alt="QR code" width="120" height="120" />
          {#if upNext}
            <div class="next">
              <div class="kicker">{t('player.comingUp')}</div>
              <div class="ellipsis">{upNext.title}</div>
              <div class="ellipsis small">{upNext.nickname}</div>
            </div>
          {/if}
        </aside>
      {/if}
    {/if}

    {#if !fullscreen}
      <div class="tap-hint">{t('player.tapForFullscreen')}</div>
    {/if}

    {#if wakeFallback}
      <video class="nosleep" src="/nosleep.mp4" muted loop autoplay playsinline aria-hidden="true"></video>
    {/if}
  </div>
{/if}

<style>
  .start {
    display: grid;
    gap: 6px;
    padding: 24px 36px;
    font-size: 1.5rem;
    border-radius: 18px;
  }
  .resume {
    position: absolute;
    left: 50%;
    top: 50%;
    transform: translate(-50%, -50%);
    cursor: pointer;
  }
  .tap-hint {
    position: absolute;
    top: 2vh;
    left: 50%;
    transform: translateX(-50%);
    background: rgba(0, 0, 0, 0.65);
    border: 1px solid rgba(255, 255, 255, 0.2);
    padding: 0.8vh 2vw;
    border-radius: 99px;
    font-size: 2.2vh;
    pointer-events: none;
  }
  .nosleep {
    position: absolute;
    width: 2px;
    height: 2px;
    opacity: 0.01;
    pointer-events: none;
    bottom: 0;
    left: 0;
  }
  .stage {
    position: fixed;
    inset: 0;
    background: #000;
    color: #fff;
    cursor: none;
    overflow: hidden;
  }
  video {
    width: 100%;
    height: 100%;
    object-fit: contain;
    background: #000;
  }
  .intro,
  .idle {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    justify-items: center;
    text-align: center;
    gap: 1.5vh;
    padding: 5vw;
    background: radial-gradient(ellipse at center, #2a1630 0%, #000 75%);
  }
  .kicker {
    color: var(--accent);
    font-size: 2.2vh;
    letter-spacing: 0.2em;
  }
  .intro-title {
    font-size: 8vh;
    font-weight: 700;
    line-height: 1.15;
  }
  .intro-artist {
    font-size: 4vh;
    color: #ccc;
  }
  .intro-singer {
    font-size: 4vh;
    margin-top: 3vh;
    color: var(--accent);
  }
  .idle h1 {
    font-size: 6vh;
  }
  .idle img {
    width: 40vh;
    height: 40vh;
    background: #fff;
    border-radius: 2vh;
    padding: 1.5vh;
  }
  .idle-hint {
    font-size: 3.5vh;
    margin: 0;
  }
  .url {
    font-size: 2.2vh;
    color: #aaa;
    margin: 0;
  }
  .overlay {
    position: absolute;
    right: 2vw;
    bottom: 3vh;
    display: flex;
    align-items: flex-end;
    gap: 1.5vw;
    max-width: 40vw;
    flex-direction: row-reverse;
  }
  .overlay img {
    width: 14vh;
    height: 14vh;
    background: #fff;
    padding: 0.8vh;
    border-radius: 1vh;
    opacity: 0.92;
  }
  .next {
    background: rgba(0, 0, 0, 0.6);
    padding: 1vh 1.5vw;
    border-radius: 1vh;
    font-size: 2.4vh;
    min-width: 0;
  }
  .next .small {
    font-size: 2vh;
    color: #bbb;
  }
</style>
