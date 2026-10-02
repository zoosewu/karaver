<script lang="ts">
  import { onMount, tick } from 'svelte'
  import Marquee from '../components/Marquee.svelte'
  import PairTV from '../components/PairTV.svelte'
  import { api, ensureSession, errorCode, searchSongs, userToken } from '../lib/api'
  import { ask, askTwice, sheet, type SheetAction } from '../lib/dialog.svelte'
  import { errorText, t } from '../lib/i18n'
  import { router } from '../lib/router.svelte'
  import { storage } from '../lib/storage'
  import { toast } from '../lib/toast.svelte'
  import { connectRoom } from '../lib/ws'
  import type { HistoryEntry, QueueItem, RoomState, Song } from '../lib/types'

  let { roomId }: { roomId: string } = $props()

  type Phase = 'loading' | 'notFound' | 'nickname' | 'ready' | 'kicked' | 'deleted' | 'error'
  let phase = $state<Phase>('loading')
  let userId = $state('')
  let nickname = $state(storage.get('karaver.nickname') ?? '')
  let nicknameError = $state('')
  let joining = $state(false)
  let room = $state<RoomState | null>(null)
  let roomUrl = $state('')
  let connected = $state(true)
  let tab = $state<'search' | 'favorites' | 'queue'>('search')
  let showQRCode = $state(false)

  const me = $derived(room?.members.find((m) => m.userId === userId))
  const queuedSongIds = $derived(new Set([room?.current?.songId, ...(room?.queue.map((i) => i.songId) ?? [])]))
  const myCount = $derived(room?.queue.filter((i) => i.userId === userId).length ?? 0)
  const isMyTurn = $derived(!!room?.current && room.current.userId === userId)
  const currentId = $derived(room?.current?.id ?? null)

  onMount(async () => {
    try {
      userId = await ensureSession()
      const info = await api<{ id: string; url: string }>('GET', `/api/rooms/${roomId}`)
      roomUrl = info.url
    } catch (e) {
      phase = errorCode(e) === 'not_found' ? 'notFound' : 'error'
      return
    }
    if (nickname) await join()
    else phase = 'nickname'
  })

  async function join() {
    joining = true
    nicknameError = ''
    try {
      await api('POST', `/api/rooms/${roomId}/join`, { nickname: nickname.trim() })
      storage.set('karaver.nickname', nickname.trim())
      phase = 'ready'
    } catch (e) {
      const code = errorCode(e)
      if (code === 'banned') phase = 'kicked'
      else {
        nicknameError = errorText(code)
        phase = 'nickname'
      }
    } finally {
      joining = false
    }
  }

  $effect(() => {
    if (phase !== 'ready') return
    return connectRoom(`/api/rooms/${roomId}/ws?token=${encodeURIComponent(userToken())}`, {
      onState: (s) => (room = s),
      onConnection: (c) => (connected = c),
      onTerminal: (reason, code) => {
        if (reason === 'kicked' || code === 'banned') phase = 'kicked'
        else if (reason === 'deleted' || code === 'not_found') phase = 'deleted'
        else if (code === 'not_member') phase = 'nickname'
        else phase = 'error'
      },
    })
  })

  // ---- search ----
  let query = $state('')
  let results = $state<Song[]>([])
  let more = $state(false)
  let searching = $state(false)
  let searchSeq = 0

  async function runSearch(q: string, offset: number) {
    const seq = ++searchSeq
    searching = true
    try {
      const r = await searchSongs(q, offset)
      if (seq !== searchSeq) return
      results = offset ? [...results, ...r.items] : r.items
      more = r.more
    } catch (e) {
      if (seq === searchSeq) toast(errorText(errorCode(e)), 'error')
    } finally {
      if (seq === searchSeq) searching = false
    }
  }

  $effect(() => {
    if (phase !== 'ready') return
    const q = query
    const timer = setTimeout(() => runSearch(q, 0), q ? 250 : 0)
    return () => clearTimeout(timer)
  })

  // ---- history (oldest first, shown above the current song) ----
  let history = $state<HistoryEntry[]>([])

  async function loadHistory() {
    try {
      history = (await api<HistoryEntry[]>('GET', `/api/rooms/${roomId}/history`)).reverse()
    } catch {
      /* keep the old list */
    }
  }

  // A song ending is what adds to the history, so refresh whenever the current song changes.
  $effect(() => {
    void currentId
    if (phase === 'ready') loadHistory()
  })

  // ---- actions ----
  async function act(fn: () => Promise<unknown>, success?: string) {
    try {
      await fn()
      if (success) toast(success)
      return true
    } catch (e) {
      toast(errorText(errorCode(e)), 'error')
      return false
    }
  }

  const control = (action: string, value = 0) => act(() => api('POST', `/api/rooms/${roomId}/control`, { action, value }))

  const enqueue = (s: { id: number; title: string }) =>
    act(() => api('POST', `/api/rooms/${roomId}/queue`, { songId: s.id }), t('room.added', { title: s.title }))

  // Pause, restart and skip interrupt whoever is singing, so all three ask first.
  async function togglePause() {
    const cur = room?.current
    if (!cur) return
    if (room!.player.paused) control('play')
    else if (await ask(t('room.pauseConfirm', { title: cur.title }))) control('pause')
  }

  async function restart() {
    const cur = room?.current
    if (cur && (await ask(t('room.restartConfirm', { title: cur.title })))) control('restart')
  }

  async function skip() {
    const cur = room?.current
    if (cur && (await ask(t('room.skipConfirm', { title: cur.title }), { danger: true })))
      act(() => api('POST', `/api/rooms/${roomId}/skip`, { itemId: cur.id }))
  }

  // Moving: in FIFO anyone may move any song; in round-robin only your own songs,
  // and only among themselves (the server enforces the same rules).
  function canMoveUp(item: QueueItem, index: number): boolean {
    if (!room) return false
    if (room.settings.mode === 'fifo') return index > 0
    if (item.userId !== userId) return false
    return room.queue.filter((q) => q.userId === userId).findIndex((q) => q.id === item.id) > 0
  }

  function queueMenu(item: QueueItem, index: number) {
    const movable = canMoveUp(item, index)
    const actions: SheetAction[] = [
      {
        label: `↑ ${t('room.moveUp')}`,
        disabled: !movable,
        run: () => act(() => api('POST', `/api/rooms/${roomId}/queue/${item.id}/move`, { to: 'up' })),
      },
      {
        label: `⤒ ${t('room.moveTop')}`,
        disabled: !movable,
        run: () => act(() => api('POST', `/api/rooms/${roomId}/queue/${item.id}/move`, { to: 'top' })),
      },
    ]
    if (item.userId === userId) {
      actions.push({
        label: `✕ ${t('room.removeSong')}`,
        danger: true,
        run: async () => {
          if (await askTwice(t('room.removeConfirm', { title: item.title }), t('room.removeConfirm2', { title: item.title })))
            act(() => api('DELETE', `/api/rooms/${roomId}/queue/${item.id}`))
        },
      })
    }
    sheet(item.title, actions, `${item.artist || t('common.unknownArtist')} · ${item.nickname}`)
  }

  function historyMenu(h: HistoryEntry) {
    sheet(
      h.title,
      [
        {
          label: `⟲ ${t('room.replaySong')}`,
          disabled: !h.present || queuedSongIds.has(h.songId),
          hint: !h.present ? t('error.song_not_found') : queuedSongIds.has(h.songId) ? t('error.duplicate_song') : undefined,
          run: () => enqueue({ id: h.songId, title: h.title }),
        },
      ],
      `${h.artist || t('common.unknownArtist')} · ${h.nickname}`,
    )
  }

  function roomMenu() {
    const available = history.length > 0 && (room?.queue.length ?? 0) === 0
    sheet(t('room.settings'), [
      {
        label: `⟲ ${t('room.replayAll')}`,
        disabled: !available,
        hint: available ? t('room.replayAllHint') : t('room.replayAllUnavailable'),
        run: async () => {
          if (
            await askTwice(
              t('room.replayAllConfirm', { n: history.length }),
              t('room.replayAllConfirm2'),
              t('room.replayAll'),
            )
          ) {
            try {
              const r = await api<{ queued: number }>('POST', `/api/rooms/${roomId}/replay-all`)
              toast(t('room.replayAllDone', { n: r.queued }))
            } catch (e) {
              toast(errorText(errorCode(e)), 'error')
            }
          }
        },
      },
    ])
  }

  // ---- favorites (stored per nickname on the server) ----
  let favorites = $state<Song[]>([])
  const favoriteIds = $derived(new Set(favorites.map((s) => s.id)))

  $effect(() => {
    if (phase !== 'ready') return
    api<Song[]>('GET', `/api/rooms/${roomId}/favorites`)
      .then((f) => (favorites = f))
      .catch(() => {})
  })

  async function toggleFavorite(s: Song) {
    const on = !favoriteIds.has(s.id)
    // Optimistic: the star flips immediately and rolls back on failure.
    const before = favorites
    favorites = on ? [s, ...favorites] : favorites.filter((f) => f.id !== s.id)
    try {
      await api(on ? 'PUT' : 'DELETE', `/api/rooms/${roomId}/favorites/${s.id}`)
    } catch (e) {
      favorites = before
      toast(errorText(errorCode(e)), 'error')
    }
  }

  // ---- queue tab: open with the current song at the top; history is above it ----
  let nowEl = $state<HTMLElement>()
  async function openQueue() {
    tab = 'queue'
    await tick()
    nowEl?.scrollIntoView({ block: 'start' })
  }
</script>

{#snippet star(s: Song)}
  <button
    class="ghost small star"
    class:on={favoriteIds.has(s.id)}
    onclick={() => toggleFavorite(s)}
    aria-label={favoriteIds.has(s.id) ? t('room.favoriteRemove') : t('room.favoriteAdd')}
  >
    {favoriteIds.has(s.id) ? '★' : '☆'}
  </button>
{/snippet}

{#snippet songRow(s: Song)}
  <li>
    {@render star(s)}
    <div class="song">
      <Marquee class="title" text={s.title} />
      <Marquee class="sub" text={s.artist || t('common.unknownArtist')} />
    </div>
    <button class="primary small" disabled={queuedSongIds.has(s.id)} onclick={() => enqueue(s)}>
      {t('room.add')}
    </button>
  </li>
{/snippet}

{#if phase === 'loading'}
  <main class="center-screen"><p class="muted">{t('common.loading')}</p></main>
{:else if phase === 'notFound' || phase === 'kicked' || phase === 'deleted' || phase === 'error'}
  <main class="center-screen">
    <div class="narrow">
      <h2>
        {phase === 'notFound'
          ? t('room.notFound')
          : phase === 'kicked'
            ? t('room.kicked')
            : phase === 'deleted'
              ? t('room.deleted')
              : t('error.internal')}
      </h2>
      <button onclick={() => router.navigate('/')}>{t('room.backHome')}</button>
    </div>
  </main>
{:else if phase === 'nickname'}
  <main class="center-screen">
    <form
      class="narrow"
      onsubmit={(e) => {
        e.preventDefault()
        join()
      }}
    >
      <h2>{t('room.nicknameTitle')}</h2>
      <p class="muted">{t('room.nicknameHint')}</p>
      <!-- svelte-ignore a11y_autofocus -->
      <input bind:value={nickname} maxlength="20" placeholder={t('room.nicknamePlaceholder')} autofocus />
      {#if nicknameError}<p class="error">{nicknameError}</p>{/if}
      <button class="primary" type="submit" disabled={joining || !nickname.trim()}>{t('room.join')}</button>
    </form>
  </main>
{:else if room}
  <div class="page">
    <header class="row">
      <div class="room-name"><Marquee text={room.settings.name} /></div>
      <span class="badge">{room.settings.mode === 'rr' ? t('room.modeRr') : t('room.modeFifo')}</span>
      <button class="ghost small icon" onclick={() => (showQRCode = true)} aria-label={t('room.showQR')}>QR</button>
      <button class="ghost small icon" onclick={roomMenu} aria-label={t('room.settings')}>⚙</button>
      <button class="ghost small nick" onclick={() => (phase = 'nickname')} title={t('room.changeNickname')}>
        <Marquee text={me?.nickname ?? nickname} />
      </button>
    </header>

    {#if !connected}<div class="banner">{t('common.offline')}</div>{/if}

    <section class="card now">
      <div class="label muted">{t('room.nowPlaying')}</div>
      {#if room.current}
        <div class="row">
          <div class="song">
            <Marquee class="title" text={room.current.title} />
            <Marquee
              class="sub"
              text={`${room.current.artist || t('common.unknownArtist')} · ${t('room.sungBy', { name: room.current.nickname })}`}
            />
          </div>
          {#if room.current.hasOriginal}
            <span class="badge" class:accent={room.player.vocal}>
              {room.player.vocal ? t('room.vocalOn') : t('room.vocalOff')}
            </span>
          {/if}
        </div>
        <div class="transport" class:single={!isMyTurn}>
          {#if isMyTurn}
            <button onclick={togglePause}>
              {room.player.paused ? `▶ ${t('room.play')}` : `⏸ ${t('room.pause')}`}
            </button>
            <button onclick={restart}>⟲ {t('room.restart')}</button>
          {/if}
          <button onclick={skip}>⏭ {t('room.skip')}</button>
        </div>
        {#if isMyTurn}
          <div class="controls">
            {#if room.current.hasOriginal}
              <button class="small" onclick={() => control('vocal', room!.player.vocal ? 0 : 1)}>
                🎤 {room.player.vocal ? t('room.vocalSwitchOff') : t('room.vocalSwitchOn')}
              </button>
            {/if}
            <button class="small" onclick={() => control('qr', room!.player.showQR ? 0 : 1)}>
              {room.player.showQR ? t('room.qrHide') : t('room.qrShow')}
            </button>
            <label class="volume row">
              <span class="muted">{t('room.volume')}</span>
              <input
                type="range"
                min="0"
                max="100"
                step="5"
                value={room.player.volume}
                onchange={(e) => control('volume', +e.currentTarget.value)}
              />
            </label>
          </div>
        {/if}
      {:else}
        <div class="muted">{t('room.nothingPlaying')}</div>
      {/if}
    </section>

    {#if !room.player.online}
      <section class="card"><PairTV endpoint={`/api/rooms/${roomId}/tv/pair`} /></section>
    {/if}

    <nav class="tabs">
      <button class:active={tab === 'search'} onclick={() => (tab = 'search')}>{t('room.tabSearch')}</button>
      <button class:active={tab === 'favorites'} onclick={() => (tab = 'favorites')}>
        ★ {t('room.tabFavorites')}
      </button>
      <button class:active={tab === 'queue'} onclick={openQueue}>
        {t('room.tabQueue')}
        <span class="badge" class:accent={myCount > 0}>{room.queue.length}</span>
      </button>
    </nav>

    {#if tab === 'search'}
      <section>
        <input type="search" bind:value={query} placeholder={t('room.searchPlaceholder')} enterkeyhint="search" />
        <ul class="list">
          {#each results as s (s.id)}
            {@render songRow(s)}
          {/each}
        </ul>
        {#if !searching && results.length === 0}
          <p class="muted empty">{t('room.noResults')}</p>
        {/if}
        {#if more}
          <button class="load-more" disabled={searching} onclick={() => runSearch(query, results.length)}>
            {t('room.loadMore')}
          </button>
        {/if}
      </section>
    {:else if tab === 'favorites'}
      <section>
        {#if favorites.length === 0}
          <p class="muted empty">{t('room.favoritesEmpty')}</p>
        {:else}
          <ul class="list">
            {#each favorites as s (s.id)}
              {@render songRow(s)}
            {/each}
          </ul>
        {/if}
        <p class="muted hint">{t('room.favoritesHint')}</p>
      </section>
    {:else}
      <section class="timeline">
        {#if history.length > 0}
          <div class="divider">{t('room.history')}</div>
          <ul class="list history">
            {#each history as h (h.id)}
              <li>
                <div class="song">
                  <Marquee class="title" text={h.title} />
                  <Marquee
                    class="sub"
                    text={`${h.artist || t('common.unknownArtist')}${h.status !== 'done' ? ' · ' + t(`admin.status.${h.status}`) : ''}`}
                  />
                </div>
                <span class="who">{h.nickname}</span>
                {@render star({ id: h.songId, title: h.title, artist: h.artist })}
                <button class="ghost small icon" onclick={() => historyMenu(h)} aria-label={t('room.songMenu')}>⋯</button>
              </li>
            {/each}
          </ul>
        {/if}

        <div class="now-row" bind:this={nowEl}>
          <div class="divider">{t('room.nowPlaying')}</div>
          {#if room.current}
            <ul class="list">
              <li class="playing" class:mine={room.current.userId === userId}>
                <span class="idx">♪</span>
                <div class="song">
                  <Marquee class="title" text={room.current.title} />
                  <Marquee class="sub" text={room.current.artist || t('common.unknownArtist')} />
                </div>
                <span class="who">{room.current.nickname}</span>
                {@render star({ id: room.current.songId, title: room.current.title, artist: room.current.artist })}
              </li>
            </ul>
          {:else}
            <p class="muted">{t('room.nothingPlaying')}</p>
          {/if}
        </div>

        <div class="divider">{t('room.upNext')}</div>
        {#if room.queue.length === 0}
          <p class="muted empty">{t('room.queueEmpty')}</p>
        {:else}
          <ol class="list">
            {#each room.queue as item, i (item.id)}
              <li class:mine={item.userId === userId}>
                <span class="idx">{i + 1}</span>
                <div class="song">
                  <Marquee class="title" text={item.title} />
                  <Marquee class="sub" text={item.artist || t('common.unknownArtist')} />
                </div>
                <span class="who">{item.nickname}</span>
                {@render star({ id: item.songId, title: item.title, artist: item.artist })}
                <button class="ghost small icon" onclick={() => queueMenu(item, i)} aria-label={t('room.songMenu')}>⋯</button>
              </li>
            {/each}
          </ol>
        {/if}
        <!-- Room to scroll the current song to the top even when little follows it. -->
        <div class="tail"></div>
      </section>
    {/if}
  </div>

  {#if showQRCode}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="qr-backdrop" onclick={() => (showQRCode = false)}>
      <div class="qr-panel">
        <div class="qr-title"><Marquee text={t('room.qrTitle', { name: room.settings.name })} /></div>
        <img src={`/api/rooms/${roomId}/qr.png`} alt="QR code" />
        <div class="muted url">{roomUrl}</div>
        <button onclick={() => (showQRCode = false)}>{t('common.close')}</button>
      </div>
    </div>
  {/if}
{/if}

<style>
  .page {
    max-width: 640px;
    margin: 0 auto;
    padding: 12px var(--gutter) calc(80px + env(safe-area-inset-bottom));
    display: grid;
    /* minmax(0, …) lets long, non-wrapping titles shrink instead of widening the page. */
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
  }
  header {
    gap: 4px;
  }
  .room-name {
    flex: 1;
    min-width: 0;
    font-size: 1.25rem;
    font-weight: 700;
  }
  .icon {
    min-width: 36px;
    font-weight: 700;
  }
  .nick {
    max-width: 30%;
    min-width: 0;
    display: block;
  }
  .banner {
    background: var(--accent-soft);
    color: var(--accent);
    border-radius: 10px;
    padding: 8px 12px;
    font-size: 0.9rem;
  }
  .error {
    color: var(--danger);
    margin: 0;
  }
  .now {
    display: grid;
    gap: 8px;
    min-width: 0;
  }
  .now .label {
    font-size: 0.8rem;
  }
  /* Pause, restart and skip: same size, same weight, side by side. */
  .transport {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 8px;
  }
  .transport.single {
    grid-template-columns: minmax(0, 1fr);
  }
  .controls {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .volume {
    flex: 1 1 160px;
    font-size: 0.85rem;
  }
  .volume span {
    white-space: nowrap;
  }
  .volume input {
    min-height: 32px;
    padding: 0;
    border: none;
    background: none;
    accent-color: var(--accent);
  }
  .star {
    font-size: 1.2rem;
    color: var(--muted);
    padding: 4px 6px;
    flex: none;
  }
  .star.on {
    color: var(--accent);
  }
  .hint {
    font-size: 0.8rem;
    text-align: center;
  }
  .tabs {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 4px;
    background: var(--surface);
    border-radius: var(--radius);
    padding: 4px;
    position: sticky;
    top: 0;
    z-index: 5;
    /* Hide list content scrolling underneath, including the gap above the bar. */
    box-shadow: 0 -12px 0 var(--bg), 0 8px 12px var(--bg);
  }
  .tabs button {
    border: none;
    background: transparent;
  }
  .tabs button.active {
    background: var(--surface-2);
    font-weight: 600;
  }
  section {
    min-width: 0;
  }
  section > input {
    margin-bottom: 4px;
  }
  .list > li > :global(*) {
    flex-shrink: 0;
  }
  .list > li > .song {
    flex: 1 1 auto;
    min-width: 0;
  }
  .song :global(.title) {
    font-weight: 600;
  }
  .song :global(.sub) {
    font-size: 0.85rem;
    color: var(--muted);
  }
  .idx {
    width: 1.5rem;
    text-align: right;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .who {
    max-width: 5.5em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8rem;
    color: var(--muted);
  }
  li.mine :global(.title) {
    color: var(--accent);
  }
  .history li {
    opacity: 0.65;
  }
  li.playing {
    background: var(--accent-soft);
    border-radius: 10px;
    padding-left: 8px;
    padding-right: 8px;
  }
  .divider {
    font-size: 0.75rem;
    letter-spacing: 0.1em;
    color: var(--muted);
    padding: 12px 0 2px;
  }
  .now-row {
    /* Keep the sticky tabs from covering it after scrollIntoView. */
    scroll-margin-top: 64px;
  }
  .tail {
    min-height: calc(100dvh - 160px);
  }
  .empty {
    text-align: center;
    padding: 24px 0;
  }
  .load-more {
    width: 100%;
    margin-top: 8px;
  }
  .qr-backdrop {
    position: fixed;
    inset: 0;
    z-index: 80;
    background: rgba(0, 0, 0, 0.7);
    display: grid;
    place-items: center;
    padding: var(--gutter);
  }
  .qr-panel {
    width: min(100%, 360px);
    background: var(--surface);
    border-radius: 16px;
    padding: 20px;
    display: grid;
    gap: 12px;
    justify-items: center;
    text-align: center;
  }
  .qr-title {
    width: 100%;
    font-weight: 600;
  }
  .qr-panel img {
    width: 100%;
    height: auto;
    aspect-ratio: 1;
    background: #fff;
    border-radius: 12px;
    padding: 10px;
  }
  .url {
    font-size: 0.85rem;
    overflow-wrap: anywhere;
  }
  .qr-panel button {
    width: 100%;
  }
</style>
