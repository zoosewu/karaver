<script lang="ts">
  import { api, errorCode } from '../lib/api'
  import { errorText, t } from '../lib/i18n'
  import { toast } from '../lib/toast.svelte'

  // endpoint: member or admin pairing URL for the room. compact: one-line form
  // for the room page's fixed header.
  let {
    endpoint,
    admin = false,
    compact = false,
  }: { endpoint: string; admin?: boolean; compact?: boolean } = $props()

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

{#snippet form()}
  <form class="row" onsubmit={submit}>
    {#if compact}<span class="tv" aria-hidden="true">📺</span>{/if}
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
{/snippet}

{#if compact}
  <div class="pair compact">
    {@render form()}
    <p class="muted">{t('pair.compactHint', { url: tvUrl })}</p>
  </div>
{:else}
  <div class="pair">
    <h3>{admin ? t('pair.adminTitle') : t('pair.title')}</h3>
    <p class="muted">
      {admin ? t('pair.adminInstructions', { url: tvUrl }) : t('pair.instructions', { url: tvUrl })}
    </p>
    {@render form()}
  </div>
{/if}

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
  .compact {
    gap: 2px;
  }
  .compact input,
  .compact button {
    min-height: 34px;
    padding-top: 2px;
    padding-bottom: 2px;
  }
  .compact input {
    font-size: 1rem;
  }
  .compact p {
    font-size: 0.75rem;
  }
  .tv {
    font-size: 1.1rem;
  }
</style>
