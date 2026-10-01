<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api } from '../lib/api'
  import { t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'
  import { connectRoom } from '../lib/ws'
  import type { QueueItem, RoomState } from '../lib/types'

  let { roomId }: { roomId: string } = $props()

  const INTRO_MS = 5000

  type Phase = 'checking' | 'start' | 'playing' | 'kicked' | 'notFound'
  let phase = $state<Phase>('checking')
  let room = $state<RoomState | null>(null)
  let roomUrl = $state('')
  let video = $state<HTMLVideoElement>()

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
  const upNext = $derived(room?.queue[0] ?? null)
  const qrSrc = $derived(`/api/rooms/${roomId}/qr.png`)

  onMount(async () => {
    try {
      const info = await api<{ url: string }>('GET', `/api/rooms/${roomId}`)
      roomUrl = info.url
      phase = 'start'
    } catch {
      phase = 'notFound'
    }
  })

  function start() {
    room = null
    document.documentElement.requestFullscreen?.().catch(() => {})
    phase = 'playing'
  }

  $effect(() => {
    if (phase !== 'playing') return
    return connectRoom(`/api/rooms/${roomId}/player/ws`, {
      onState: (s) => (room = s),
      onTerminal: (reason) => (phase = reason === 'kicked' ? 'kicked' : 'notFound'),
    })
  })

  // Keep the screen awake while this page is open.
  $effect(() => {
    if (phase !== 'playing' || !('wakeLock' in navigator)) return
    let lock: WakeLockSentinel | null = null
    const acquire = () => {
      if (document.visibilityState === 'visible')
        navigator.wakeLock.request('screen').then((l) => (lock = l)).catch(() => {})
    }
    acquire()
    document.addEventListener('visibilitychange', acquire)
    return () => {
      document.removeEventListener('visibilitychange', acquire)
      lock?.release()
    }
  })

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
    if (video) video.volume = volume / 100
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

  // Autoplay can still be refused (e.g. after the browser restores the tab); offer a click to resume.
  let blocked = $state(false)
  function tryPlay() {
    video
      ?.play()
      .then(() => (blocked = false))
      .catch((e: DOMException) => {
        if (e.name === 'NotAllowedError') blocked = true
      })
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
{:else if phase === 'kicked'}
  <main class="center-screen">
    <div class="narrow">
      <h2>{t('player.kicked')}</h2>
      <button class="primary" onclick={start}>{t('player.reconnect')}</button>
    </div>
  </main>
{:else if phase === 'playing' && room && !active}
  <main class="center-screen waiting">
    <div class="narrow">
      <h2>{room.settings.name}</h2>
      <p>{t('player.waiting', { n: position })}</p>
      <p class="muted">{t('player.waitingHint')}</p>
    </div>
  </main>
{:else if phase === 'start'}
  <main class="center-screen">
    <button class="primary start" onclick={start}>
      ▶ {t('player.start')}
      <span>{t('player.startHint')}</span>
    </button>
  </main>
{:else}
  <div class="stage">
    {#if loaded}
      <!-- svelte-ignore a11y_media_has_caption -->
      <video
        bind:this={video}
        src={`/media/${loaded.songId}`}
        autoplay
        playsinline
        onended={() => report(false)}
        onerror={() => report(true)}
      ></video>
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
  .start span {
    font-size: 0.9rem;
    font-weight: 400;
    opacity: 0.85;
  }
  .resume {
    position: absolute;
    left: 50%;
    top: 50%;
    transform: translate(-50%, -50%);
    cursor: pointer;
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
