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
  import { keyActions, keyLabel } from '../lib/controls'
  import { setTheme, themeState } from '../lib/theme.svelte'
  import { connectRoom } from '../lib/ws'
  import type { HistoryEntry, QueueItem, RoomState, Song } from '../lib/types'

  let { roomId }: { roomId: string } = $props()

  type Phase = 'loading' | 'notFound' | 'nickname' | 'ready' | 'kicked' | 'deleted' | 'error'
  let phase = $state<Phase>('loading')
  let userId = $state('')
  let nickname = $state(storage.get('zkaraver.nickname') ?? '')
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
      storage.set('zkaraver.nickname', nickname.trim())
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
    // If the page did not fill the screen, keep going.
    if (seq === searchSeq) {
      await tick()
      maybeLoadMoreResults()
    }
  }

  // Search results load the next page automatically near the bottom of the list.
  function maybeLoadMoreResults() {
    if (tab !== 'search' || !more || searching || !scroller) return
    if (scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 400) runSearch(query, results.length)
  }

  $effect(() => {
    if (phase !== 'ready') return
    const q = query
    const timer = setTimeout(() => runSearch(q, 0), q ? 250 : 0)
    return () => clearTimeout(timer)
  })

  // ---- history: oldest first, shown above the current song, loaded in pages ----
  const HISTORY_PAGE = 50
  let history = $state<HistoryEntry[]>([])
  let historyMore = $state(false) // older entries exist on the server
  let historyLoading = false

  const byTime = (a: HistoryEntry, b: HistoryEntry) => a.startedAt - b.startedAt || a.id - b.id

  function mergeHistory(page: HistoryEntry[]) {
    const all = new Map(history.map((h) => [h.id, h]))
    for (const h of page) all.set(h.id, h)
    history = [...all.values()].sort(byTime)
  }

  // Newest page: on entry, and whenever a song ends (that is what adds history).
  async function loadNewestHistory() {
    try {
      const page = await api<HistoryEntry[]>('GET', `/api/rooms/${roomId}/history?limit=${HISTORY_PAGE}`)
      const first = history.length === 0
      mergeHistory(page)
      if (first) historyMore = page.length === HISTORY_PAGE
    } catch {
      /* keep the old list */
    }
  }

  // Older page, when the user scrolls up near the top of the queue tab.
  async function loadOlderHistory() {
    if (historyLoading || !historyMore || history.length === 0) return
    historyLoading = true
    const oldest = history[0]
    try {
      const page = await api<HistoryEntry[]>(
        'GET',
        `/api/rooms/${roomId}/history?limit=${HISTORY_PAGE}&before_started=${oldest.startedAt}&before_id=${oldest.id}`,
      )
      historyMore = page.length === HISTORY_PAGE
      const before = history.length
      mergeHistory(page)
      // Rows were added above the viewport: keep what the user is looking at in place.
      if (scroller) scroller.scrollTop += (history.length - before) * ROW_H
    } catch {
      /* try again on the next scroll */
    } finally {
      historyLoading = false
    }
  }

  // Someone cleared the history (on any device): drop what this page loaded.
  const historyRev = $derived(room?.historyRev ?? 0)
  let seenHistoryRev = 0

  $effect(() => {
    void currentId
    const rev = historyRev
    if (phase !== 'ready') return
    if (rev !== seenHistoryRev) {
      seenHistoryRev = rev
      history = []
      historyMore = false
    }
    loadNewestHistory()
  })

  // ---- "N more songs until your turn" ----
  const turnText = $derived.by(() => {
    if (!room || isMyTurn) return ''
    const n = room.queue.findIndex((q) => q.userId === userId)
    if (n < 0) return ''
    return n === 0 ? t('room.turnNext') : t('room.turnIn', { n })
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
    const theme = themeState.current
    sheet(t('room.settings'), [
      {
        label: `🌙 ${t('room.themeDark')}${theme === 'dark' ? ' ✓' : ''}`,
        disabled: theme === 'dark',
        hint: t('room.themeHint'),
        run: () => setTheme('dark'),
      },
      {
        label: `☀ ${t('room.themeLight')}${theme === 'light' ? ' ✓' : ''}`,
        disabled: theme === 'light',
        run: () => setTheme('light'),
      },
      {
        label: `⟲ ${t('room.replayAll')}`,
        disabled: !available,
        hint: available ? t('room.replayAllHint') : t('room.replayAllUnavailable'),
        run: async () => {
          if (
            await askTwice(
              t('room.replayAllConfirm'),
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
      {
        label: `🗑 ${t('room.clearHistory')}`,
        danger: true,
        disabled: !available,
        hint: available ? t('room.clearHistoryHint') : t('room.replayAllUnavailable'),
        run: async () => {
          if (await askTwice(t('room.clearHistoryConfirm'), t('room.clearHistoryConfirm2'), t('room.clearHistory'))) {
            try {
              const r = await api<{ deleted: number }>('POST', `/api/rooms/${roomId}/clear-history`)
              toast(t('room.clearHistoryDone', { n: r.deleted }))
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

  // Vocal / TV QR / volume live behind one button to keep the fixed top short.
  function moreControls() {
    const p = room?.player
    const cur = room?.current
    if (!p || !cur) return
    const actions: SheetAction[] = []
    if (cur.hasOriginal)
      actions.push({
        label: `🎤 ${p.vocal ? t('room.vocalSwitchOff') : t('room.vocalSwitchOn')}`,
        run: () => control('vocal', p.vocal ? 0 : 1),
      })
    actions.push(
      {
        label: `🔊 ${t('room.volumeUp')}`,
        disabled: p.volume >= 100,
        run: () => control('volume', Math.min(100, p.volume + 10)),
      },
      {
        label: `🔉 ${t('room.volumeDown')}`,
        disabled: p.volume <= 0,
        run: () => control('volume', Math.max(0, p.volume - 10)),
      },
      { label: `▦ ${p.showQR ? t('room.qrHide') : t('room.qrShow')}`, run: () => control('qr', p.showQR ? 0 : 1) },
      ...keyActions(p.key, control),
    )
    sheet(t('room.moreControls'), actions, `${t('room.volumeNow', { n: p.volume })} · ${keyLabel(p.key)}`)
  }

  // ---- the list area below the fixed top is the only thing that scrolls ----
  let scroller = $state<HTMLElement>()
  let viewHeight = $state(0)
  let scrollTop = $state(0)

  // Queue tab: history + current + upcoming as one virtual list. Every row has
  // the same height, so only the rows in view (plus a margin) are rendered.
  const ROW_H = 60
  const OVERSCAN = 6
  type Row =
    | { kind: 'history'; key: string; h: HistoryEntry }
    | { kind: 'current'; key: string; item: QueueItem }
    | { kind: 'queue'; key: string; item: QueueItem; index: number }

  const rows = $derived.by((): Row[] => {
    if (!room) return []
    const out: Row[] = history.map((h) => ({ kind: 'history', key: `h${h.id}`, h }))
    if (room.current) out.push({ kind: 'current', key: `c${room.current.id}`, item: room.current })
    room.queue.forEach((item, index) => out.push({ kind: 'queue', key: `q${item.id}`, item, index }))
    return out
  })
  // Index of the current song (or of the first upcoming one): where the tab opens.
  const anchorIndex = $derived(history.length)
  // Tall enough that the anchor row can always be scrolled to the very top.
  const listHeight = $derived(Math.max(rows.length * ROW_H, anchorIndex * ROW_H + viewHeight))
  const first = $derived(Math.max(0, Math.floor(scrollTop / ROW_H) - OVERSCAN))
  const last = $derived(Math.min(rows.length, Math.ceil((scrollTop + viewHeight) / ROW_H) + OVERSCAN))
  const visible = $derived(rows.slice(first, last))

  let scrollFrame = 0
  function onScroll() {
    if (scrollFrame) return
    scrollFrame = requestAnimationFrame(() => {
      scrollFrame = 0
      if (!scroller) return
      scrollTop = scroller.scrollTop
      // Lazy-load older history when the top of the list comes into view.
      if (tab === 'queue' && scrollTop < ROW_H * 8) loadOlderHistory()
      maybeLoadMoreResults()
    })
  }

  async function switchTab(next: typeof tab) {
    tab = next
    await tick()
    if (!scroller) return
    // The search box only exists on the search tab, so the list area just changed
    // size; read it now instead of waiting for the resize observer, so the list
    // is tall enough before we position it.
    viewHeight = scroller.clientHeight
    await tick()
    // The queue tab opens on the current song; history sits above it.
    scroller.scrollTop = next === 'queue' ? anchorIndex * ROW_H : 0
    scrollTop = scroller.scrollTop
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
    <button class="tonal small" disabled={queuedSongIds.has(s.id)} onclick={() => enqueue(s)}>
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
  <div class="app">
    <!-- Fixed top: everything you operate. Kept as short as possible. -->
    <div class="top">
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

      <section class="now" class:idle={!room.current}>
        {#if room.current}
          <!-- One line: song · artist · requester; scrolls when it does not fit. -->
          <div class="row now-line">
            <span class="note">{room.player.paused ? '⏸' : '♪'}</span>
            <div class="now-text">
              <Marquee>
                <b>{room.current.title}</b>
                <span class="muted"> · {room.current.artist || t('common.unknownArtist')} · {room.current.nickname}</span>
              </Marquee>
            </div>
            {#if room.current.hasOriginal}
              <span class="badge" class:accent={room.player.vocal}>
                {room.player.vocal ? t('room.vocalOn') : t('room.vocalOff')}
              </span>
            {/if}
            {#if room.player.key !== 0}<span class="badge accent">{keyLabel(room.player.key)}</span>{/if}
            {#if turnText}<span class="badge accent turn">{turnText}</span>{/if}
          </div>
          <div class="transport" class:mine={isMyTurn}>
            {#if isMyTurn}
              <button onclick={togglePause}>
                {room.player.paused ? `▶ ${t('room.play')}` : `⏸ ${t('room.pause')}`}
              </button>
              <button onclick={restart}>⟲ {t('room.restart')}</button>
            {/if}
            <button onclick={skip}>⏭ {t('room.skip')}</button>
            {#if isMyTurn}
              <button class="more" onclick={moreControls} aria-label={t('room.moreControls')}>⋯</button>
            {/if}
          </div>
        {:else}
          <div class="muted nothing">{t('room.nothingPlaying')}</div>
        {/if}
      </section>

      {#if !room.player.online}
        <PairTV endpoint={`/api/rooms/${roomId}/tv/pair`} compact />
      {/if}

      <nav class="tabs">
        <button class:active={tab === 'search'} onclick={() => switchTab('search')}>{t('room.tabSearch')}</button>
        <button class:active={tab === 'favorites'} onclick={() => switchTab('favorites')}>
          ★ {t('room.tabFavorites')}
        </button>
        <button class:active={tab === 'queue'} onclick={() => switchTab('queue')}>
          {t('room.tabQueue')}
          <span class="badge" class:accent={myCount > 0}>{room.queue.length}</span>
        </button>
      </nav>

      {#if tab === 'search'}
        <input type="search" bind:value={query} placeholder={t('room.searchPlaceholder')} enterkeyhint="search" />
      {/if}
    </div>

    <!-- The only scrolling region. -->
    <div class="scroller" bind:this={scroller} bind:clientHeight={viewHeight} onscroll={onScroll}>
      {#if tab === 'search'}
        <ul class="list">
          {#each results as s (s.id)}
            {@render songRow(s)}
          {/each}
        </ul>
        {#if !searching && results.length === 0}
          <p class="muted empty">{t('room.noResults')}</p>
        {/if}
        {#if more}
          <p class="muted loading-more">{t('common.loading')}</p>
        {/if}
      {:else if tab === 'favorites'}
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
      {:else}
        <!-- Virtual list, no section titles: history is dimmed grey, the current
             song is tinted, upcoming songs are plain. Only rows in view render. -->
        <div class="vlist" style:height={`${listHeight}px`}>
          <ol class="list rows" style:transform={`translateY(${first * ROW_H}px)`}>
            {#each visible as row (row.key)}
              {#if row.kind === 'history'}
                {@const h = row.h}
                <li class="history">
                  <span class="idx"></span>
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
              {:else if row.kind === 'current'}
                {@const item = row.item}
                <li class="playing" class:mine={item.userId === userId}>
                  <span class="idx">{room.player.paused ? '⏸' : '♪'}</span>
                  <div class="song">
                    <Marquee class="title" text={item.title} />
                    <Marquee class="sub" text={item.artist || t('common.unknownArtist')} />
                  </div>
                  <span class="who">{item.nickname}</span>
                  {@render star({ id: item.songId, title: item.title, artist: item.artist })}
                  <span class="icon-space"></span>
                </li>
              {:else}
                {@const item = row.item}
                <li class="upcoming" class:mine={item.userId === userId}>
                  <span class="idx">{row.index + 1}</span>
                  <div class="song">
                    <Marquee class="title" text={item.title} />
                    <Marquee class="sub" text={item.artist || t('common.unknownArtist')} />
                  </div>
                  <span class="who">{item.nickname}</span>
                  {@render star({ id: item.songId, title: item.title, artist: item.artist })}
                  <button class="ghost small icon" onclick={() => queueMenu(item, row.index)} aria-label={t('room.songMenu')}>⋯</button>
                </li>
              {/if}
            {/each}
          </ol>
          {#if room.queue.length === 0}
            <p class="muted empty queue-empty" style:top={`${rows.length * ROW_H}px`}>{t('room.queueEmpty')}</p>
          {/if}
        </div>
      {/if}
    </div>
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
  /* Full-height column: fixed top + one scrolling list. */
  .app {
    height: 100dvh;
    max-width: 640px;
    margin: 0 auto;
    display: flex;
    flex-direction: column;
  }
  .top {
    flex: none;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
    padding: 6px var(--gutter) 8px;
    border-bottom: 1px solid var(--border);
  }
  .scroller {
    flex: 1;
    min-height: 0;
    position: relative; /* offsetTop of rows is relative to this */
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 0 var(--gutter) env(safe-area-inset-bottom);
  }
  header {
    gap: 2px;
  }
  .room-name {
    flex: 1;
    min-width: 0;
    font-size: 1.1rem;
    font-weight: 700;
  }
  .top button {
    min-height: 34px;
    padding-top: 4px;
    padding-bottom: 4px;
  }
  .icon {
    min-width: 34px;
    font-weight: 700;
    padding-left: 6px;
    padding-right: 6px;
  }
  .nick {
    max-width: 28%;
    min-width: 0;
    display: block;
  }
  .banner {
    background: var(--accent-soft);
    color: var(--accent);
    border-radius: 8px;
    padding: 4px 10px;
    font-size: 0.85rem;
  }
  .error {
    color: var(--danger);
    margin: 0;
  }
  /* Neutral card: no highlight, just the song on one line plus the controls. */
  .now {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 6px 10px 8px;
  }
  .now-text {
    flex: 1;
    min-width: 0;
  }
  .turn {
    flex: none;
  }
  .now-line {
    gap: 8px;
  }
  .note {
    color: var(--accent);
    font-size: 1.1rem;
  }
  .nothing {
    font-size: 0.9rem;
  }
  /* Pause, restart and skip: same size, same weight, side by side; "⋯" is narrower. */
  .transport {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
  }
  .transport.mine {
    grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
  }
  .transport button {
    font-size: 0.9rem;
  }
  .transport .more {
    min-width: 40px;
    font-weight: 700;
  }
  .tabs {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 4px;
    background: var(--surface);
    border-radius: var(--radius);
    padding: 3px;
  }
  .tabs button {
    border: none;
    background: transparent;
  }
  .tabs button.active {
    background: var(--surface-2);
    font-weight: 600;
  }
  .top input[type='search'] {
    min-height: 38px;
    padding-top: 6px;
    padding-bottom: 6px;
  }
  .star {
    font-size: 1.2rem;
    color: var(--muted);
    padding: 4px 6px;
  }
  .star.on {
    color: var(--accent);
  }
  .hint {
    font-size: 0.8rem;
    text-align: center;
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
  /* Virtual list: fixed-height rows positioned inside a full-height box. */
  .vlist {
    position: relative;
  }
  .rows {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
  }
  .rows > li {
    height: 60px;
    padding-top: 0;
    padding-bottom: 0;
  }
  .icon-space {
    width: 34px;
  }
  .queue-empty {
    position: absolute;
    left: 0;
    right: 0;
  }
  /* History: dimmed and grey, so it reads as "already sung". */
  li.history {
    opacity: 0.5;
  }
  li.history :global(.title) {
    font-weight: 400;
    color: var(--muted);
  }
  /* Current song: accent background, the anchor of the list. */
  li.playing {
    background: var(--accent-soft);
    border-radius: 10px;
    border-bottom: none;
    padding-left: 8px;
    padding-right: 8px;
  }
  li.playing .idx {
    color: var(--accent);
  }
  .empty {
    text-align: center;
    padding: 24px 0;
  }
  .loading-more {
    text-align: center;
    font-size: 0.85rem;
    padding: 12px 0;
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
