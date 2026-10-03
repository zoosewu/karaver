<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import Marquee from '../components/Marquee.svelte'
  import { api } from '../lib/api'
  import { t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'
  import { connectRoom } from '../lib/ws'
  import type { QueueItem, RoomState } from '../lib/types'

  let { roomId }: { roomId: string } = $props()

  const INTRO_MS = 5000
  const MAX_DRIFT_S = 0.3

  type Phase = 'checking' | 'playing' | 'kicked' | 'superseded' | 'notFound'
  let phase = $state<Phase>('checking')
  let room = $state<RoomState | null>(null)
  let roomUrl = $state('')
  let video = $state<HTMLVideoElement>()
  let alt = $state<HTMLAudioElement>()

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
  const rate = $derived((room?.player.rate ?? 100) / 100)
  const seekNonce = $derived(room?.player.seekNonce ?? 0)
  const key = $derived(active ? (room?.player.key ?? 0) : 0)
  // Keys of the current song the server has rendered (see server/internal/keys).
  const keyReady = $derived(key !== 0 && (room?.player.keysReady ?? []).includes(key))
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

  // One ID per browser tab, kept in sessionStorage so reconnects and reloads
  // reclaim the same place in the player line (see Room.addPlayerLocked).
  function instanceId(): string {
    const key = 'zkaraver.playerInstance'
    try {
      let id = sessionStorage.getItem(key)
      if (!id) {
        id = crypto.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
        sessionStorage.setItem(key, id)
      }
      return id
    } catch {
      return '' // no storage: behave like before (queue on every connect)
    }
  }

  function reconnect() {
    room = null
    phase = 'playing'
    unlock()
  }

  $effect(() => {
    if (phase !== 'playing') return
    return connectRoom(`/api/rooms/${roomId}/player/ws?instance=${encodeURIComponent(instanceId())}`, {
      onState: (s) => (room = s),
      onTerminal: (reason) =>
        (phase = reason === 'kicked' ? 'kicked' : reason === 'superseded' ? 'superseded' : 'notFound'),
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

  // TV remotes send key events (arrows / OK), not clicks, so any key also unlocks.
  $effect(() => {
    if (phase !== 'playing') return
    addEventListener('keydown', unlock)
    return () => removeEventListener('keydown', unlock)
  })

  function unlock() {
    if (!document.fullscreenElement) document.documentElement.requestFullscreen?.().catch(() => {})
    if (blocked) tryPlay()
    syncAlt()
  }

  // ---- key change ----
  // The server renders every queued song at ±1..±6 ahead of time; the TV plays the
  // rendered track (see altSrc). If a key is asked for before it is ready, the TV
  // says so briefly and keeps the original key, switching over once it arrives.
  let keyNotice = $state(0)
  $effect(() => {
    const k = key
    if (!loaded || k === 0 || untrack(() => vocal || keyReady)) return
    keyNotice = k
    const timer = setTimeout(() => (keyNotice = 0), 3000)
    return () => {
      clearTimeout(timer)
      keyNotice = 0
    }
  })

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
    if (alt) alt.volume = v
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

  // Speed: the browser time-stretches and keeps the pitch (preservesPitch is on by
  // default). A new src resets playbackRate, so re-apply when the song changes.
  $effect(() => {
    void loaded
    const r = rate
    if (video) video.playbackRate = r
    if (alt) alt.playbackRate = r
  })

  // Relative seek (±3 s from the admin page). The original-vocal track follows
  // through the video's "seeked" event.
  let lastSeek: number | null = null
  $effect(() => {
    const n = seekNonce
    if (lastSeek !== null && n !== lastSeek && video && loaded) {
      const delta = untrack(() => room?.player.seekDelta ?? 0)
      const end = Number.isFinite(video.duration) ? video.duration - 0.5 : Infinity
      video.currentTime = Math.min(Math.max(0, video.currentTime + delta), end)
    }
    lastSeek = n
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

  // ---- alternate audio: the karaoke video plays muted under another track ----
  // Used for original vocals (audio from the original-vocal file, never key-shifted)
  // and for key changes (audio rendered at another key by the server).
  // Files are assumed to share a timeline. The track follows the video's clock;
  // the video is muted only once the track is actually audible.
  let altPlaying = $state(false)
  let altFailed = $state('') // src that failed to load: fall back to the video's audio

  const altSrc = $derived.by(() => {
    if (!loaded) return ''
    if (vocal && loaded.hasOriginal) return `/media/${loaded.songId}/original`
    return keyReady ? `/media/${loaded.songId}/key/${key}` : ''
  })
  const altActive = $derived(altSrc !== '' && altSrc !== altFailed)

  function syncAlt() {
    if (!alt || !video) return
    if (!altActive || video.paused || video.ended) {
      if (!alt.paused) alt.pause()
      return
    }
    if (Math.abs(alt.currentTime - video.currentTime) > MAX_DRIFT_S) alt.currentTime = video.currentTime
    if (alt.paused) alt.play().catch(() => {})
  }

  $effect(() => {
    void altActive
    void alt
    syncAlt()
  })

  $effect(() => {
    if (video) video.muted = altActive && altPlaying
  })

  function altError(src: string) {
    altPlaying = false
    altFailed = src
  }

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
{:else if phase === 'superseded'}
  <main class="center-screen">
    <div class="narrow">
      <h2>{t('player.superseded')}</h2>
      <button class="primary" onclick={reconnect}>{t('player.useThisTab')}</button>
    </div>
  </main>
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
            syncAlt()
            report(false)
          }}
          onerror={() => report(true)}
          onplay={syncAlt}
          onpause={syncAlt}
          onseeked={syncAlt}
          ontimeupdate={syncAlt}
        ></video>
        {#if altSrc}
          {#key altSrc}
            <audio
              bind:this={alt}
              src={altSrc}
              preload="metadata"
              onloadedmetadata={syncAlt}
              onplaying={() => (altPlaying = true)}
              onpause={() => (altPlaying = false)}
              onwaiting={() => (altPlaying = false)}
              onerror={() => altError(altSrc)}
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
              <Marquee text={upNext.title} />
              <div class="ellipsis small">{upNext.nickname}</div>
            </div>
          {/if}
        </aside>
      {/if}
    {/if}

    {#if !fullscreen}
      <div class="tap-hint">{t('player.tapForFullscreen')}</div>
    {/if}
    {#if keyNotice}
      <div class="key-notice">{t('player.keyNotReady', { n: keyNotice > 0 ? `+${keyNotice}` : `${keyNotice}` })}</div>
    {/if}
    {#if key !== 0}
      <div class="key-badge">
        {t('player.key', { n: key > 0 ? `+${key}` : `${key}` })}{#if vocal && loaded?.hasOriginal}{t('room.keyVocal')}{/if}
      </div>
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
  .key-notice {
    position: absolute;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    background: rgba(0, 0, 0, 0.75);
    border: 1px solid rgba(255, 255, 255, 0.25);
    padding: 1.6vh 3vw;
    border-radius: 16px;
    font-size: 3vh;
    pointer-events: none;
  }
  .key-badge {
    position: absolute;
    top: 2vh;
    right: 2vw;
    background: rgba(0, 0, 0, 0.6);
    border: 1px solid var(--accent);
    color: #fff;
    padding: 0.6vh 1.4vw;
    border-radius: 99px;
    font-size: 2.4vh;
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
  /* Middle of the left edge: karaoke lyrics sit along the bottom, so keep clear of them. */
  .overlay {
    position: absolute;
    left: 2vw;
    top: 50%;
    transform: translateY(-50%);
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 1.5vh;
    width: 22vw;
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
    max-width: 100%;
  }
  .next .small {
    font-size: 2vh;
    color: #bbb;
  }
</style>
