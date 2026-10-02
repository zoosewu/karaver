<script lang="ts">
  import { onMount } from 'svelte'
  import AdminLogin from '../components/AdminLogin.svelte'
  import AdminRoom from '../components/AdminRoom.svelte'
  import { api, errorCode } from '../lib/api'
  import { errorText, t } from '../lib/i18n'
  import { storage } from '../lib/storage'
  import { toast } from '../lib/toast.svelte'
  import type { RoomSummary, ScanResult } from '../lib/types'

  let phase = $state<'checking' | 'login' | 'ready'>('checking')
  let rooms = $state<RoomSummary[]>([])
  let selected = $state<string | null>(storage.get('karaver.adminRoom'))
  let newName = $state('')
  let library = $state<{ scanning: boolean; last: ScanResult | null; songs: number } | null>(null)

  onMount(async () => {
    try {
      const me = await api<{ admin: boolean }>('GET', '/api/admin/me')
      if (me.admin) ready()
      else phase = 'login'
    } catch {
      phase = 'login'
    }
  })

  function ready() {
    phase = 'ready'
    refresh()
  }

  async function refresh() {
    try {
      ;[rooms, library] = await Promise.all([
        api<RoomSummary[]>('GET', '/api/admin/rooms'),
        api<typeof library>('GET', '/api/admin/library'),
      ])
      if (selected && !rooms.some((r) => r.id === selected)) select(null)
    } catch (e) {
      if (errorCode(e) === 'unauthorized') phase = 'login'
    }
  }

  // Light polling for the room list and scan progress; room details stream over WebSocket.
  $effect(() => {
    if (phase !== 'ready') return
    const timer = setInterval(refresh, library?.scanning ? 1500 : 10_000)
    return () => clearInterval(timer)
  })

  function select(id: string | null) {
    selected = id
    storage.set('karaver.adminRoom', id ?? '')
  }

  async function createRoom(e: SubmitEvent) {
    e.preventDefault()
    try {
      const r = await api<{ id: string }>('POST', '/api/admin/rooms', { name: newName })
      newName = ''
      await refresh()
      select(r.id)
    } catch (err) {
      toast(errorText(errorCode(err)), 'error')
    }
  }

  async function scan() {
    try {
      await api('POST', '/api/admin/library/scan')
      if (library) library.scanning = true
    } catch (e) {
      toast(errorText(errorCode(e)), 'error')
    }
  }

  async function logout() {
    await api('POST', '/api/admin/logout').catch(() => {})
    phase = 'login'
  }
</script>

{#if phase === 'checking'}
  <main class="center-screen"><p class="muted">{t('common.loading')}</p></main>
{:else if phase === 'login'}
  <AdminLogin onsuccess={ready} />
{:else}
  <div class="layout" class:has-selection={!!selected}>
    <aside class="sidebar">
      <header class="row">
        <h1>{t('admin.title')}</h1>
        <span class="spacer"></span>
        <button class="ghost small" onclick={logout}>{t('admin.logout')}</button>
      </header>

      <section class="card lib">
        <div class="row">
          <h3>{t('admin.library')}</h3>
          <span class="spacer"></span>
          <button class="small" onclick={scan} disabled={library?.scanning}>
            {library?.scanning ? t('admin.scanning') : t('admin.scan')}
          </button>
        </div>
        {#if library}
          <div>{t('admin.songCount', { n: library.songs })}</div>
          {#if library.last}
            <div class="muted small">
              {library.last.error
                ? library.last.error
                : t('admin.lastScan', {
                    added: library.last.added,
                    updated: library.last.updated,
                    removed: library.last.removed,
                    ms: library.last.durationMs,
                  })}
            </div>
          {/if}
        {/if}
      </section>

      <section class="card">
        <h3>{t('admin.rooms')}</h3>
        <form class="row create" onsubmit={createRoom}>
          <input
            bind:value={newName}
            maxlength="32"
            pattern="[A-Za-z0-9_\-]+"
            title={t('admin.newRoomRule')}
            autocapitalize="off"
            autocomplete="off"
            placeholder={t('admin.newRoomPlaceholder')}
          />
          <button class="primary" type="submit" disabled={!newName.trim()}>{t('admin.create')}</button>
        </form>
        {#if rooms.length === 0}
          <p class="muted">{t('admin.noRooms')}</p>
        {:else}
          <ul class="list rooms">
            {#each rooms as r (r.id)}
              <li>
                <button class="room-btn" class:active={r.id === selected} onclick={() => select(r.id)}>
                  <div class="row">
                    <strong class="ellipsis">{r.settings.name}</strong>
                    <span class="spacer"></span>
                    <span class="badge" class:ok={r.playerOnline}>
                      {r.playerOnline ? t('admin.playerOnline') : t('admin.playerOffline')}
                    </span>
                  </div>
                  <div class="muted small ellipsis">
                    {t('admin.roomOnline', { n: r.online })} · {t('admin.queueCount', { n: r.queueLength })}
                    {#if r.playing}· ♪ {r.playing}{/if}
                  </div>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    </aside>

    <main class="detail">
      {#if selected}
        {#key selected}
          <AdminRoom roomId={selected} onclose={() => select(null)} onchange={refresh} />
        {/key}
      {:else}
        <p class="muted placeholder">{t('admin.selectRoom')}</p>
      {/if}
    </main>
  </div>
{/if}

<style>
  .layout {
    display: grid;
    grid-template-columns: minmax(280px, 360px) 1fr;
    gap: 16px;
    max-width: 1280px;
    margin: 0 auto;
    padding: 16px var(--gutter);
    align-items: start;
  }
  .sidebar {
    display: grid;
    gap: 12px;
    position: sticky;
    top: 16px;
  }
  .sidebar h1 {
    font-size: 1.35rem;
  }
  .spacer {
    flex: 1;
  }
  .card {
    display: grid;
    gap: 10px;
  }
  .small {
    font-size: 0.85rem;
  }
  .rooms > li {
    padding: 4px 0;
    border: none;
  }
  .room-btn {
    width: 100%;
    text-align: left;
    display: grid;
    gap: 2px;
    background: transparent;
    border-color: transparent;
    white-space: normal;
  }
  .room-btn.active {
    background: var(--accent-soft);
    border-color: var(--accent);
  }
  .placeholder {
    text-align: center;
    padding-top: 20vh;
  }
  @media (max-width: 800px) {
    .layout {
      grid-template-columns: 1fr;
    }
    .sidebar {
      position: static;
    }
    .layout.has-selection .sidebar {
      display: none;
    }
    .layout:not(.has-selection) .detail {
      display: none;
    }
  }
</style>
