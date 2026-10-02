<script lang="ts">
  import Marquee from './Marquee.svelte'
  import PairTV from './PairTV.svelte'
  import { ask, askTwice, sheet } from '../lib/dialog.svelte'
  import { api, errorCode } from '../lib/api'
  import { errorText, t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'
  import { keyActions, keyLabel } from '../lib/controls'
  import { connectRoom } from '../lib/ws'
  import type { HistoryEntry, RoomSettings, RoomState } from '../lib/types'

  let { roomId, onclose, onchange }: { roomId: string; onclose: () => void; onchange: () => void } = $props()

  type Tab = 'queue' | 'players' | 'members' | 'history' | 'settings'
  let tab = $state<Tab>('queue')
  let room = $state<RoomState | null>(null)
  let connected = $state(true)
  let form = $state<RoomSettings | null>(null)

  const base = $derived(`/api/admin/rooms/${roomId}`)
  const roomPath = $derived(`/r/${roomId}`)
  const dirty = $derived(!!form && !!room && JSON.stringify(form) !== JSON.stringify(room.settings))

  $effect(() =>
    connectRoom(`/api/admin/rooms/${roomId}/ws`, {
      onState: (s) => {
        room = s
        form ??= { ...s.settings }
      },
      onConnection: (c) => (connected = c),
      onTerminal: () => onclose(),
    }),
  )

  async function act(fn: () => Promise<unknown>) {
    try {
      await fn()
      return true
    } catch (e) {
      toast(errorText(errorCode(e)), 'error')
      return false
    }
  }

  const control = (action: string, value = 0) => act(() => api('POST', `/api/rooms/${roomId}/control`, { action, value }))

  // Speed in percent, 50–150 in steps of 5; the pitch stays the same.
  const rate = $derived(room?.player.rate ?? 100)
  const setRate = (pct: number) => control('rate', Math.min(150, Math.max(50, pct)))

  // ---- now playing: same rules as the phone page (destructive actions ask first) ----
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
  function moreControls() {
    const p = room?.player
    if (!p) return
    sheet(
      t('room.moreControls'),
      [
        ...(room?.current?.hasOriginal
          ? [{ label: `🎤 ${p.vocal ? t('room.vocalSwitchOff') : t('room.vocalSwitchOn')}`, run: () => control('vocal', p.vocal ? 0 : 1) }]
          : []),
        { label: `🔊 ${t('room.volumeUp')}`, disabled: p.volume >= 100, run: () => control('volume', Math.min(100, p.volume + 10)) },
        { label: `🔉 ${t('room.volumeDown')}`, disabled: p.volume <= 0, run: () => control('volume', Math.max(0, p.volume - 10)) },
        { label: `▦ ${p.showQR ? t('room.qrHide') : t('room.qrShow')}`, run: () => control('qr', p.showQR ? 0 : 1) },
        ...keyActions(p.key, control),
      ],
      `${t('room.volumeNow', { n: p.volume })} · ${keyLabel(p.key)}`,
    )
  }

  // ---- queue ----
  const move = (itemId: number, index: number) => act(() => api('POST', `${base}/move`, { itemId, index }))
  async function remove(itemId: number, title: string) {
    if (await askTwice(t('room.removeConfirm', { title }), t('room.removeConfirm2', { title })))
      act(() => api('DELETE', `/api/rooms/${roomId}/queue/${itemId}`))
  }
  async function clearQueue() {
    if (await askTwice(t('admin.clearConfirm'), t('admin.clearConfirm2'))) act(() => api('POST', `${base}/clear`))
  }

  // ---- players & members ----
  async function kickPlayer(playerId: string) {
    if (await ask(t('admin.kickPlayerConfirm'), { danger: true })) act(() => api('POST', `${base}/players/kick`, { playerId }))
  }
  async function kick(userId: string, name: string) {
    if (await ask(t('admin.kickConfirm', { name }), { danger: true })) act(() => api('POST', `${base}/kick`, { userId }))
  }
  const unban = (userId: string) => act(() => api('POST', `${base}/unban`, { userId }))

  // Just enough to tell a TV browser from a laptop; not meant to be exact.
  function browserName(ua: string): string {
    const os = /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Windows/.test(ua) ? 'Windows'
      : /Mac OS/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : ''
    const br = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox'
      : /Safari\//.test(ua) ? 'Safari' : ''
    return [br, os].filter(Boolean).join(' / ') || ua.slice(0, 40)
  }

  // ---- history: newest first, next page loads when the end comes into view ----
  const HISTORY_PAGE = 50
  let history = $state<HistoryEntry[]>([])
  let historyMore = $state(true)
  let historyLoading = $state(false)
  let historyEnd = $state<HTMLElement>()

  async function loadHistoryPage() {
    if (historyLoading || !historyMore) return
    historyLoading = true
    const last = history.at(-1)
    const cursor = last ? `&before_started=${last.startedAt}&before_id=${last.id}` : ''
    try {
      const page = await api<HistoryEntry[]>('GET', `${base}/history?limit=${HISTORY_PAGE}${cursor}`)
      history = [...history, ...page]
      historyMore = page.length === HISTORY_PAGE
    } catch (e) {
      toast(errorText(errorCode(e)), 'error')
    } finally {
      historyLoading = false
    }
  }

  $effect(() => {
    if (tab !== 'history' || !historyEnd) return
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadHistoryPage()
    })
    io.observe(historyEnd)
    return () => io.disconnect()
  })

  function reloadHistory() {
    history = []
    historyMore = true
    loadHistoryPage()
  }

  function openTab(next: Tab) {
    tab = next
    // Start fresh each time so newly finished songs show up.
    if (next === 'history') reloadHistory()
  }

  // History was cleared (here or on a phone): reload the list if it is open.
  let seenHistoryRev = 0
  $effect(() => {
    const rev = room?.historyRev ?? 0
    if (rev === seenHistoryRev) return
    seenHistoryRev = rev
    if (tab === 'history') reloadHistory()
  })

  // Same rule as on the phone: only while nothing is waiting in the queue.
  const canClearHistory = $derived(history.length > 0 && (room?.queue.length ?? 0) === 0)
  async function clearHistory() {
    if (!(await askTwice(t('room.clearHistoryConfirm'), t('room.clearHistoryConfirm2'), t('room.clearHistory')))) return
    try {
      const r = await api<{ deleted: number }>('POST', `/api/rooms/${roomId}/clear-history`)
      toast(t('room.clearHistoryDone', { n: r.deleted }))
    } catch (e) {
      toast(errorText(errorCode(e)), 'error')
    }
  }

  // ---- settings ----
  async function save(e: SubmitEvent) {
    e.preventDefault()
    if (!form) return
    if (await act(() => api('PUT', base, form))) {
      toast(t('common.saved'))
      onchange()
    }
  }
  async function deleteRoom() {
    if (!room || !(await askTwice(t('admin.deleteConfirm', { name: room.settings.name }), t('admin.deleteConfirm2')))) return
    if (await act(() => api('DELETE', base))) {
      onchange()
      onclose()
    }
  }

  const fmtTime = (ms: number) =>
    new Date(ms).toLocaleString(undefined, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
</script>

{#if room && form}
  <div class="admin-room">
    <!-- Sticky top: which room, what is playing, the controls and the tabs. -->
    <div class="top">
      <header class="row">
        <button class="ghost small back" onclick={onclose} aria-label={t('admin.rooms')}>←</button>
        <h2 class="name"><Marquee text={room.settings.name} /></h2>
        <span class="badge" class:ok={room.player.online}>
          {room.player.online ? t('admin.playerOnline') : t('admin.playerOffline')}
        </span>
        {#if !connected}<span class="badge accent">{t('common.offline')}</span>{/if}
      </header>

      <section class="now">
        {#if room.current}
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
          </div>
          <div class="transport">
            <button onclick={togglePause}>{room.player.paused ? `▶ ${t('room.play')}` : `⏸ ${t('room.pause')}`}</button>
            <button onclick={restart}>⟲ {t('room.restart')}</button>
            <button onclick={skip}>⏭ {t('room.skip')}</button>
            <button class="more" onclick={moreControls} aria-label={t('room.moreControls')}>⋯</button>
          </div>
          <!-- Seek and speed: admin-only conveniences, not destructive, so no confirmation. -->
          <div class="seekbar">
            <button onclick={() => control('seek', -3)} aria-label={t('admin.seekBack')}>⏪ 3s</button>
            <button onclick={() => setRate(rate - 5)} disabled={rate <= 50} aria-label={t('admin.slower')}>−</button>
            <button class="rate" class:changed={rate !== 100} onclick={() => setRate(100)} title={t('admin.rateReset')}>
              {(rate / 100).toFixed(2)}×
            </button>
            <button onclick={() => setRate(rate + 5)} disabled={rate >= 150} aria-label={t('admin.faster')}>+</button>
            <button onclick={() => control('seek', 3)} aria-label={t('admin.seekForward')}>3s ⏩</button>
          </div>
        {:else}
          <div class="muted nothing">{t('room.nothingPlaying')}</div>
        {/if}
      </section>

      <nav class="tabs">
        <button class:active={tab === 'queue'} onclick={() => openTab('queue')}>
          {t('admin.tabQueue')} <span class="badge">{room.queue.length}</span>
        </button>
        <button class:active={tab === 'players'} onclick={() => openTab('players')}>
          {t('admin.tabPlayers')}
        </button>
        <button class:active={tab === 'members'} onclick={() => openTab('members')}>{t('admin.tabMembers')}</button>
        <button class:active={tab === 'history'} onclick={() => openTab('history')}>{t('admin.tabHistory')}</button>
        <button class:active={tab === 'settings'} onclick={() => openTab('settings')}>{t('admin.tabSettings')}</button>
      </nav>
    </div>

    <div class="body">
      {#if tab === 'queue'}
        <div class="row bar">
          {#if room.settings.mode === 'rr'}<p class="muted small grow">{t('admin.reorderHint')}</p>{:else}<span class="grow"></span>{/if}
          <button class="small danger" disabled={room.queue.length === 0} onclick={clearQueue}>{t('admin.clearQueue')}</button>
        </div>
        {#if room.queue.length === 0}
          <p class="muted empty">{t('room.queueEmpty')}</p>
        {:else}
          <ol class="list">
            {#each room.queue as item, i (item.id)}
              <li>
                <span class="idx">{i + 1}</span>
                <div class="song">
                  <Marquee class="title" text={item.title} />
                  <Marquee class="sub" text={`${item.artist || t('common.unknownArtist')} · ${item.nickname}`} />
                </div>
                {#if room.settings.mode === 'fifo'}
                  <button class="ghost small icon" disabled={i === 0} onclick={() => move(item.id, 0)} title={t('admin.moveTop')}>⤒</button>
                  <button class="ghost small icon" disabled={i === 0} onclick={() => move(item.id, i - 1)} title={t('admin.moveUp')}>↑</button>
                  <button
                    class="ghost small icon"
                    disabled={i === room.queue.length - 1}
                    onclick={() => move(item.id, i + 1)}
                    title={t('admin.moveDown')}>↓</button
                  >
                {/if}
                <button class="ghost small icon danger" onclick={() => remove(item.id, item.title)} title={t('common.delete')}>✕</button>
              </li>
            {/each}
          </ol>
        {/if}
      {:else if tab === 'players'}
        {#if !room.players?.length}
          <p class="muted empty">{t('admin.noPlayers')}</p>
        {:else}
          <ul class="list">
            {#each room.players as p, i (p.id)}
              <li>
                <span class="badge" class:ok={p.active}>
                  {p.active ? t('admin.playerActive') : t('admin.playerWaiting', { n: i })}
                </span>
                <div class="song">
                  <div class="ellipsis">{p.remoteAddr} · {browserName(p.userAgent)}</div>
                  <div class="sub ellipsis">{fmtTime(p.connectedAt)}</div>
                </div>
                <button class="small danger" onclick={() => kickPlayer(p.id)}>{t('admin.kickPlayer')}</button>
              </li>
            {/each}
          </ul>
        {/if}
        <section class="card"><PairTV endpoint={`${base}/tv/pair`} admin /></section>
        <section class="card links">
          <img src={`/api/rooms/${roomId}/qr.png`} alt="QR code" width="120" height="120" />
          <div class="link-list">
            <a href={`${roomPath}/player`} target="_blank" rel="noopener">{t('admin.openPlayer')} ↗</a>
            <a href={roomPath} target="_blank" rel="noopener">{t('admin.openRoom')} ↗</a>
            <code class="muted">{roomPath}</code>
          </div>
        </section>
      {:else if tab === 'members'}
        {#if room.members.length === 0}
          <p class="muted empty">{t('admin.noMembers')}</p>
        {:else}
          <ul class="list">
            {#each room.members as m (m.userId)}
              <li>
                <span class="ellipsis grow">{m.nickname}</span>
                {#if m.banned}
                  <span class="badge accent">{t('admin.banned')}</span>
                  <button class="small" onclick={() => unban(m.userId)}>{t('admin.unban')}</button>
                {:else}
                  {#if m.online}<span class="badge ok">{t('admin.online')}</span>{/if}
                  <button class="small danger" onclick={() => kick(m.userId, m.nickname)}>{t('admin.kick')}</button>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      {:else if tab === 'history'}
        <div class="row bar">
          <p class="muted small grow">{canClearHistory || history.length === 0 ? '' : t('room.replayAllUnavailable')}</p>
          <button class="small danger" disabled={!canClearHistory} onclick={clearHistory}>🗑 {t('room.clearHistory')}</button>
        </div>
        {#if history.length === 0 && !historyLoading && !historyMore}
          <p class="muted empty">{t('admin.noHistory')}</p>
        {:else}
          <ul class="list">
            {#each history as h (h.id)}
              <li>
                <span class="muted small time">{fmtTime(h.startedAt)}</span>
                <div class="song">
                  <Marquee class="title" text={h.title} />
                  <Marquee class="sub" text={`${h.artist || t('common.unknownArtist')} · ${h.nickname}`} />
                </div>
                <span class="badge">{t(`admin.status.${h.status}`)}</span>
              </li>
            {/each}
          </ul>
        {/if}
        <!-- Scrolling this into view loads the next (older) page. -->
        <p class="muted end" bind:this={historyEnd}>
          {historyLoading ? t('common.loading') : historyMore ? '' : history.length ? t('admin.historyEnd') : ''}
        </p>
      {:else}
        <form class="card settings" onsubmit={save}>
          <label class="field">
            {t('admin.mode')}
            <select bind:value={form.mode}>
              <option value="fifo">{t('room.modeFifo')}</option>
              <option value="rr">{t('room.modeRr')}</option>
            </select>
          </label>
          <label class="field">
            {t('admin.maxPerUser')}
            <input type="number" min="0" max="99" bind:value={form.maxPerUser} />
          </label>
          <label class="field">
            {t('admin.idleClear')}
            <input type="number" min="0" max="1440" bind:value={form.idleClearMinutes} />
          </label>
          <div class="row">
            <button class="primary" type="submit" disabled={!dirty}>{t('common.save')}</button>
            <button type="button" disabled={!dirty} onclick={() => (form = { ...room!.settings })}>{t('common.cancel')}</button>
          </div>
        </form>
        <section class="card danger-zone">
          <div class="grow">
            <b>{t('admin.deleteRoom')}</b>
            <p class="muted small">{t('admin.deleteHint')}</p>
          </div>
          <button class="danger" onclick={deleteRoom}>{t('admin.deleteRoom')}</button>
        </section>
      {/if}
    </div>
  </div>
{:else}
  <p class="muted">{t('common.loading')}</p>
{/if}

<style>
  .admin-room {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
  }
  /* The controls stay at the top while the tab content scrolls under them. */
  .top {
    position: sticky;
    top: 0;
    z-index: 5;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
    padding: 4px 0 8px;
    background: var(--bg);
    border-bottom: 1px solid var(--border);
  }
  .top button {
    min-height: 34px;
    padding-top: 4px;
    padding-bottom: 4px;
  }
  header {
    gap: 6px;
  }
  .name {
    flex: 1;
    min-width: 0;
    font-size: 1.2rem;
  }
  .back {
    display: none;
  }
  .now {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 6px 10px 8px;
  }
  .now-line {
    gap: 8px;
  }
  .note {
    color: var(--accent);
    font-size: 1.1rem;
  }
  .now-text {
    flex: 1;
    min-width: 0;
  }
  .nothing {
    font-size: 0.9rem;
  }
  .transport {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
    gap: 6px;
  }
  .transport button {
    font-size: 0.9rem;
  }
  .transport .more {
    min-width: 40px;
    font-weight: 700;
  }
  .seekbar {
    display: grid;
    grid-template-columns: 1fr 0.7fr 1fr 0.7fr 1fr;
    gap: 6px;
  }
  .seekbar button {
    font-size: 0.85rem;
    padding-left: 4px;
    padding-right: 4px;
    font-variant-numeric: tabular-nums;
  }
  .seekbar .rate.changed {
    color: var(--accent);
    border-color: var(--accent);
  }
  .tabs {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 2px;
    background: var(--surface);
    border-radius: var(--radius);
    padding: 3px;
  }
  .tabs button {
    border: none;
    background: transparent;
    padding-left: 2px;
    padding-right: 2px;
    font-size: 0.85rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tabs button.active {
    background: var(--surface-2);
    font-weight: 600;
  }
  .body {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
    padding-top: 8px;
    min-width: 0;
  }
  .bar {
    gap: 8px;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .small {
    font-size: 0.85rem;
  }
  .icon {
    min-width: 32px;
    padding-left: 4px;
    padding-right: 4px;
  }
  .list > li > :global(*) {
    flex-shrink: 0;
  }
  .list > li > .song,
  .list > li > .grow {
    flex: 1 1 auto;
    min-width: 0;
  }
  .idx {
    width: 1.5rem;
    text-align: right;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .time {
    white-space: nowrap;
  }
  .empty,
  .end {
    text-align: center;
    padding: 16px 0;
    margin: 0;
    min-height: 1px;
  }
  .settings {
    display: grid;
    gap: 12px;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));
  }
  .settings .row {
    grid-column: 1 / -1;
  }
  .links {
    display: flex;
    gap: 16px;
    align-items: center;
    flex-wrap: wrap;
  }
  .links img {
    background: #fff;
    border-radius: 8px;
    padding: 6px;
  }
  .link-list {
    display: grid;
    gap: 6px;
    min-width: 0;
  }
  .danger-zone {
    display: flex;
    gap: 12px;
    align-items: center;
    border-color: var(--danger);
  }
  .danger-zone p {
    margin: 2px 0 0;
  }
  @media (max-width: 800px) {
    .back {
      display: inline-flex;
    }
  }
</style>
