<script lang="ts">
  import { api, errorCode } from '../lib/api'
  import { errorText, t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'

  // endpoint: member or admin pairing URL for the room.
  let { endpoint, admin = false }: { endpoint: string; admin?: boolean } = $props()

  const tvUrl = `${location.origin}/tv`
  let code = $state('')
  let busy = $state(false)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    try {
      await api('POST', endpoint, { code: code.trim() })
      code = ''
      toast(t('pair.done'))
    } catch (err) {
      toast(errorText(errorCode(err)), 'error')
    } finally {
      busy = false
    }
  }
</script>

<div class="pair">
  <h3>{admin ? t('pair.adminTitle') : t('pair.title')}</h3>
  <p class="muted">
    {admin ? t('pair.adminInstructions', { url: tvUrl }) : t('pair.instructions', { url: tvUrl })}
  </p>
  <form class="row" onsubmit={submit}>
    <input
      bind:value={code}
      inputmode="numeric"
      pattern="[0-9]*"
      maxlength="6"
      autocomplete="off"
      placeholder={t('pair.placeholder')}
    />
    <button class="primary" type="submit" disabled={busy || code.trim().length < 4}>{t('pair.submit')}</button>
  </form>
</div>

<style>
  .pair {
    display: grid;
    gap: 8px;
  }
  .pair p {
    margin: 0;
    font-size: 0.9rem;
    word-break: break-all;
  }
  input {
    font-size: 1.25rem;
    letter-spacing: 0.3em;
    text-align: center;
    font-variant-numeric: tabular-nums;
  }
</style>
