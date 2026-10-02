<script lang="ts">
  import { t } from '../lib/i18n'
  import { router } from '../lib/router.svelte'

  // Every visit pairs afresh: the code exists only while this page is connected.
  let code = $state('')
  let paired = $state(false)

  $effect(() => {
    let ws: WebSocket | null = null
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined

    const open = () => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      ws = new WebSocket(`${proto}//${location.host}/api/tv/ws`)
      ws.onmessage = (ev) => {
        const msg = JSON.parse(ev.data)
        if (msg.type === 'code') code = msg.code
        else if (msg.type === 'paired') {
          stopped = true
          paired = true
          router.navigate(`/r/${encodeURIComponent(msg.room)}/player`)
        }
      }
      ws.onclose = () => {
        if (stopped) return
        code = '' // a reconnect gets a new code
        timer = setTimeout(open, 2000)
      }
    }
    open()
    return () => {
      stopped = true
      clearTimeout(timer)
      ws?.close()
    }
  })
</script>

<main class="tv">
  {#if paired}
    <p class="hint">{t('tv.pairing')}</p>
  {:else if code}
    <div class="kicker">{t('tv.title')}</div>
    <div class="code">{code}</div>
    <p class="hint">{t('tv.instructions')}</p>
  {:else}
    <p class="hint">{t('tv.connecting')}</p>
  {/if}
</main>

<style>
  .tv {
    position: fixed;
    inset: 0;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 3vh;
    text-align: center;
    padding: 5vw;
    background: radial-gradient(ellipse at center, #2a1630 0%, #000 75%);
    color: #fff;
    cursor: none;
  }
  .kicker {
    color: var(--accent);
    font-size: 3vh;
    letter-spacing: 0.3em;
  }
  .code {
    font-size: 22vh;
    font-weight: 800;
    letter-spacing: 0.15em;
    line-height: 1;
    font-variant-numeric: tabular-nums;
  }
  .hint {
    font-size: 3.2vh;
    color: #ccc;
    margin: 0;
    max-width: 70vw;
  }
</style>
