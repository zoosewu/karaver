<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ensureSession, errorCode, searchSongs, userToken } from '../lib/api'
  import { errorText, t } from '../lib/i18n'
  import { router } from '../lib/router.svelte'
  import { storage } from '../lib/storage'
  import { toast } from '../lib/toast.svelte'
  import { connectRoom } from '../lib/ws'
  import type { RoomState, Song } from '../lib/types'

  let { roomId }: { roomId: string } = $props()

  type Phase = 'loading' | 'notFound' | 'nickname' | 'ready' | 'kicked' | 'deleted' | 'error'
  let phase = $state<Phase>('loading')
  let userId = $state('')
  let nickname = $state(storage.get('karaver.nickname') ?? '')
  let nicknameError = $state('')
  let joining = $state(false)
  let room = $state<RoomState | null>(null)
  let connected = $state(true)
  let tab = $state<'search' | 'favorites' | 'queue'>('search')

  const me = $derived(room?.members.find((m) => m.userId === userId))
  const queuedSongIds = $derived(new Set([room?.current?.songId, ...(room?.queue.map((i) => i.songId) ?? [])]))
  const myCount = $derived(room?.queue.filter((i) => i.userId === userId).length ?? 0)
  const isMyTurn = $derived(!!room?.current && room.current.userId === userId)

  onMount(async () => {
    try {
      userId = await ensureSession()
      await api('GET', `/api/rooms/${roomId}`)
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

  // ---- actions ----
  async function act(fn: () => Promise<unknown>, success?: string) {
    try {
      await fn()
      if (success) toast(success)
    } catch (e) {
      toast(errorText(errorCode(e)), 'error')
    }
  }

  const enqueue = (s: Song) =>
    act(() => api('POST', `/api/rooms/${roomId}/queue`, { songId: s.id }), t('room.added', { title: s.title }))

  function remove(id: number, title: string) {
    if (confirm(t('room.removeConfirm', { title }))) act(() => api('DELETE', `/api/rooms/${roomId}/queue/${id}`))
  }

  function skip() {
    const cur = room?.current
    if (cur && confirm(t('room.skipConfirm', { title: cur.title })))
      act(() => api('POST', `/api/rooms/${roomId}/skip`, { itemId: cur.id }))
  }

  const control = (action: string, value = 0) => act(() => api('POST', `/api/rooms/${roomId}/control`, { action, value }))

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
</script>

{#snippet songRow(s: Song)}
  <li>
    <button
      class="ghost small star"
      class:on={favoriteIds.has(s.id)}
      onclick={() => toggleFavorite(s)}
      aria-label={favoriteIds.has(s.id) ? t('room.favoriteRemove') : t('room.favoriteAdd')}
    >
      {favoriteIds.has(s.id) ? '★' : '☆'}
    </button>
    <div class="song">
      <div class="title ellipsis">{s.title}</div>
      <div class="sub ellipsis">{s.artist || t('common.unknownArtist')}</div>
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
      <h1 class="ellipsis">{room.settings.name}</h1>
      <span class="badge">{room.settings.mode === 'rr' ? t('room.modeRr') : t('room.modeFifo')}</span>
      <span class="spacer"></span>
      <button class="ghost small ellipsis nick" onclick={() => (phase = 'nickname')} title={t('room.changeNickname')}>
        {me?.nickname ?? nickname}
      </button>
    </header>

    {#if !connected}<div class="banner">{t('common.offline')}</div>{/if}

    <section class="card now">
      <div class="label muted">{t('room.nowPlaying')}</div>
      {#if room.current}
        <div class="row">
          <div class="song">
            <div class="title ellipsis">{room.current.title}</div>
            <div class="sub ellipsis">
              {room.current.artist || t('common.unknownArtist')} · {t('room.sungBy', { name: room.current.nickname })}
            </div>
          </div>
          {#if room.current.hasOriginal}
            <span class="badge" class:accent={room.player.vocal}>
              {room.player.vocal ? t('room.vocalOn') : t('room.vocalOff')}
            </span>
          {/if}
          <button class="danger" onclick={skip}>{t('room.skip')}</button>
        </div>
        {#if isMyTurn}
          <div class="controls">
            {#if room.player.paused}
              <button class="small" onclick={() => control('play')}>▶ {t('room.play')}</button>
            {:else}
              <button class="small" onclick={() => control('pause')}>⏸ {t('room.pause')}</button>
            {/if}
            <button class="small" onclick={() => control('restart')}>⟲ {t('room.restart')}</button>
            {#if room.current.hasOriginal}
              <button class="small" class:primary={!room.player.vocal} onclick={() => control('vocal', room!.player.vocal ? 0 : 1)}>
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
        {#if !room.player.online}<div class="sub muted">{t('room.playerOffline')}</div>{/if}
      {:else}
        <div class="muted">{t('room.nothingPlaying')}</div>
      {/if}
    </section>

    <nav class="tabs">
      <button class:active={tab === 'search'} onclick={() => (tab = 'search')}>{t('room.tabSearch')}</button>
      <button class:active={tab === 'favorites'} onclick={() => (tab = 'favorites')}>
        ★ {t('room.tabFavorites')}
      </button>
      <button class:active={tab === 'queue'} onclick={() => (tab = 'queue')}>
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
      <section>
        {#if room.queue.length === 0}
          <p class="muted empty">{t('room.queueEmpty')}</p>
        {:else}
          <ol class="list">
            {#each room.queue as item, i (item.id)}
              <li class:mine={item.userId === userId}>
                <span class="idx">{i + 1}</span>
                <div class="song">
                  <div class="title ellipsis">{item.title}</div>
                  <div class="sub ellipsis">{item.artist || t('common.unknownArtist')} · {item.nickname}</div>
                </div>
                {#if item.userId === userId}
                  <span class="badge accent">{t('room.mine')}</span>
                  <button class="ghost small danger" onclick={() => remove(item.id, item.title)} aria-label={t('common.delete')}>
                    ✕
                  </button>
                {/if}
              </li>
            {/each}
          </ol>
        {/if}
      </section>
    {/if}
  </div>
{/if}

<style>
  .page {
    max-width: 640px;
    margin: 0 auto;
    padding: 12px var(--gutter) calc(80px + env(safe-area-inset-bottom));
    display: grid;
    gap: 12px;
  }
  header h1 {
    font-size: 1.25rem;
  }
  .spacer {
    flex: 1;
  }
  .nick {
    max-width: 40%;
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
  }
  .now .label {
    font-size: 0.8rem;
  }
  .controls {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    padding-top: 4px;
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
    grid-template-columns: 1fr 1fr 1fr;
    gap: 4px;
    background: var(--surface);
    border-radius: var(--radius);
    padding: 4px;
    position: sticky;
    top: 8px;
    z-index: 5;
  }
  .tabs button {
    border: none;
    background: transparent;
  }
  .tabs button.active {
    background: var(--surface-2);
    font-weight: 600;
  }
  section > input {
    margin-bottom: 4px;
  }
  .idx {
    width: 1.75rem;
    text-align: right;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  li.mine .title {
    color: var(--accent);
  }
  .empty {
    text-align: center;
    padding: 24px 0;
  }
  .load-more {
    width: 100%;
    margin-top: 8px;
  }
</style>
