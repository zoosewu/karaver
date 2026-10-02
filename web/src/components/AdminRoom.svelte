<script lang="ts">
  import Marquee from './Marquee.svelte'
  import PairTV from './PairTV.svelte'
  import { ask, askTwice } from '../lib/dialog.svelte'
  import { api, errorCode } from '../lib/api'
  import { errorText, t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'
  import { connectRoom } from '../lib/ws'
  import type { HistoryEntry, RoomSettings, RoomState } from '../lib/types'

  let { roomId, onclose, onchange }: { roomId: string; onclose: () => void; onchange: () => void } = $props()

  let room = $state<RoomState | null>(null)
  let connected = $state(true)
  let form = $state<RoomSettings | null>(null)
  let history = $state<HistoryEntry[] | null>(null)

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

  async function save(e: SubmitEvent) {
    e.preventDefault()
    if (!form) return
    if (await act(() => api('PUT', base, form))) {
      toast(t('common.saved'))
      onchange()
    }
  }

  const move = (itemId: number, index: number) => act(() => api('POST', `${base}/move`, { itemId, index }))
  async function remove(itemId: number, title: string) {
    if (await askTwice(t('room.removeConfirm', { title }), t('room.removeConfirm2', { title })))
      act(() => api('DELETE', `/api/rooms/${roomId}/queue/${itemId}`))
  }
  async function skip(itemId: number, title: string) {
    if (await ask(t('room.skipConfirm', { title }), { danger: true }))
      act(() => api('POST', `/api/rooms/${roomId}/skip`, { itemId }))
  }
  async function confirmControl(action: 'pause' | 'restart', title: string) {
    const msg = action === 'pause' ? t('room.pauseConfirm', { title }) : t('room.restartConfirm', { title })
    if (await ask(msg)) control(action)
  }
  const control = (action: string, value = 0) => act(() => api('POST', `/api/rooms/${roomId}/control`, { action, value }))
  const unban = (userId: string) => act(() => api('POST', `${base}/unban`, { userId }))

  async function kick(userId: string, name: string) {
    if (await ask(t('admin.kickConfirm', { name }), { danger: true })) act(() => api('POST', `${base}/kick`, { userId }))
  }

  async function kickPlayer(playerId: string) {
    if (await ask(t('admin.kickPlayerConfirm'), { danger: true })) act(() => api('POST', `${base}/players/kick`, { playerId }))
  }

  // Just enough to tell a TV browser from a laptop; not meant to be exact.
  function browserName(ua: string): string {
    const os = /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Windows/.test(ua) ? 'Windows'
      : /Mac OS/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : ''
    const br = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox'
      : /Safari\//.test(ua) ? 'Safari' : ''
    return [br, os].filter(Boolean).join(' / ') || ua.slice(0, 40)
  }

  async function clearQueue() {
    if (await askTwice(t('admin.clearConfirm'), t('admin.clearConfirm2'))) act(() => api('POST', `${base}/clear`))
  }

  async function deleteRoom() {
    if (!room || !(await askTwice(t('admin.deleteConfirm', { name: room.settings.name }), t('admin.deleteConfirm2')))) return
    if (await act(() => api('DELETE', base))) {
      onchange()
      onclose()
    }
  }

  async function loadHistory() {
    await act(async () => (history = await api<HistoryEntry[]>('GET', `${base}/history`)))
  }

  const fmtTime = (ms: number) =>
    new Date(ms).toLocaleString(undefined, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
</script>

{#if room && form}
  <div class="grid">
    <header class="row head">
      <button class="ghost small back" onclick={onclose}>←</button>
      <h2 class="ellipsis">{room.settings.name}</h2>
      <span class="badge" class:ok={room.player.online}>
        {room.player.online ? t('admin.playerOnline') : t('admin.playerOffline')}
      </span>
      {#if !connected}<span class="badge accent">{t('common.offline')}</span>{/if}
    </header>

    <section class="card">
      <h3>{t('room.nowPlaying')}</h3>
      {#if room.current}
        <div class="row">
          <div class="song">
            <Marquee class="title" text={room.current.title} />
            <Marquee class="sub" text={`${room.current.artist} · ${room.current.nickname}`} />
          </div>
          <button class="danger" onclick={() => skip(room!.current!.id, room!.current!.title)}>{t('room.skip')}</button>
        </div>
        <div class="row wrap">
          {#if room.player.paused}
            <button class="small" onclick={() => control('play')}>▶ {t('room.play')}</button>
          {:else}
            <button class="small" onclick={() => confirmControl('pause', room!.current!.title)}>⏸ {t('room.pause')}</button>
          {/if}
          <button class="small" onclick={() => confirmControl('restart', room!.current!.title)}>⟲ {t('room.restart')}</button>
          {#if room.current.hasOriginal}
            <button class="small" onclick={() => control('vocal', room!.player.vocal ? 0 : 1)}>
              🎤 {room.player.vocal ? t('room.vocalSwitchOff') : t('room.vocalSwitchOn')}
            </button>
          {/if}
          <label class="row volume">
            <span class="muted">{t('room.volume')} {room.player.volume}</span>
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
      {:else}
        <p class="muted">{t('room.nothingPlaying')}</p>
      {/if}
      <label class="row toggle">
        <input type="checkbox" checked={room.player.showQR} onchange={(e) => control('qr', e.currentTarget.checked ? 1 : 0)} />
        {t('room.qrShow')}
      </label>
    </section>

    <section class="card">
      <h3>{t('admin.players')} <span class="badge">{room.players?.length ?? 0}</span></h3>
      {#if !room.players?.length}
        <p class="muted">{t('admin.noPlayers')}</p>
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
    </section>

    <section class="card">
      <div class="row">
        <h3>{t('admin.queue')}<span class="badge">{room.queue.length}</span></h3>
        <span class="spacer"></span>
        <button class="small danger" disabled={room.queue.length === 0} onclick={clearQueue}>{t('admin.clearQueue')}</button>
      </div>
      {#if room.settings.mode === 'rr'}<p class="muted small">{t('admin.reorderHint')}</p>{/if}
      {#if room.queue.length === 0}
        <p class="muted">{t('room.queueEmpty')}</p>
      {:else}
        <ol class="list">
          {#each room.queue as item, i (item.id)}
            <li>
              <span class="idx">{i + 1}</span>
              <div class="song">
                <Marquee class="title" text={item.title} />
                <Marquee class="sub" text={`${item.artist} · ${item.nickname}`} />
              </div>
              {#if room.settings.mode === 'fifo'}
                <button class="ghost small" disabled={i === 0} onclick={() => move(item.id, 0)} title={t('admin.moveTop')}>⤒</button>
                <button class="ghost small" disabled={i === 0} onclick={() => move(item.id, i - 1)} title={t('admin.moveUp')}>↑</button>
                <button
                  class="ghost small"
                  disabled={i === room.queue.length - 1}
                  onclick={() => move(item.id, i + 1)}
                  title={t('admin.moveDown')}>↓</button
                >
              {/if}
              <button class="ghost small danger" onclick={() => remove(item.id, item.title)} title={t('common.delete')}>✕</button>
            </li>
          {/each}
        </ol>
      {/if}
    </section>

    <section class="card">
      <h3>{t('admin.settings')}</h3>
      <form class="settings" onsubmit={save}>
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
    </section>

    <section class="card">
      <PairTV endpoint={`${base}/tv/pair`} admin />
    </section>

    <section class="card">
      <h3>{t('admin.links')}</h3>
      <div class="links">
        <img src={`/api/rooms/${roomId}/qr.png`} alt="QR code" width="140" height="140" />
        <div class="grid-tight">
          <a href={`${roomPath}/player`} target="_blank" rel="noopener">{t('admin.openPlayer')} ↗</a>
          <a href={roomPath} target="_blank" rel="noopener">{t('admin.openRoom')} ↗</a>
          <code class="muted">{roomId}</code>
        </div>
      </div>
    </section>

    <section class="card">
      <h3>{t('admin.members')} <span class="badge">{room.members.length}</span></h3>
      {#if room.members.length === 0}
        <p class="muted">{t('admin.noMembers')}</p>
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
    </section>

    <section class="card">
      <div class="row">
        <h3>{t('admin.history')}</h3>
        <span class="spacer"></span>
        <button class="small" onclick={loadHistory}>{history ? t('common.retry') : t('admin.loadHistory')}</button>
      </div>
      {#if history}
        {#if history.length === 0}
          <p class="muted">{t('admin.noHistory')}</p>
        {:else}
          <ul class="list">
            {#each history as h, i (i)}
              <li>
                <span class="muted small time">{fmtTime(h.startedAt)}</span>
                <div class="song">
                  <Marquee class="title" text={h.title} />
                  <Marquee class="sub" text={`${h.artist} · ${h.nickname}`} />
                </div>
                <span class="badge">{t(`admin.status.${h.status}`)}</span>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}
    </section>

    <section class="danger-zone">
      <button class="danger" onclick={deleteRoom}>{t('admin.deleteRoom')}</button>
    </section>
  </div>
{:else}
  <p class="muted">{t('common.loading')}</p>
{/if}

<style>
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
  }
  .head h2 {
    font-size: 1.3rem;
  }
  .back {
    display: none;
  }
  .card {
    display: grid;
    gap: 10px;
  }
  .spacer,
  .grow {
    flex: 1;
  }
  .wrap {
    flex-wrap: wrap;
  }
  .small {
    font-size: 0.85rem;
  }
  .volume {
    flex: 1 1 200px;
    font-size: 0.85rem;
  }
  .volume input,
  .toggle input {
    width: auto;
    min-height: 0;
    padding: 0;
    accent-color: var(--accent);
  }
  .volume input {
    flex: 1;
  }
  .toggle {
    font-size: 0.9rem;
  }
  .idx {
    width: 1.75rem;
    text-align: right;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .settings {
    display: grid;
    gap: 12px;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
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
  .grid-tight {
    display: grid;
    gap: 6px;
  }
  .time {
    white-space: nowrap;
  }
  .danger-zone {
    display: flex;
    justify-content: flex-end;
  }
  @media (max-width: 800px) {
    .back {
      display: inline-flex;
    }
  }
</style>
