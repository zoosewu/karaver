<script lang="ts">
  import { api, errorCode } from '../lib/api'
  import { errorText, t } from '../lib/i18n'

  let { onsuccess }: { onsuccess: () => void } = $props()

  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await api('POST', '/api/admin/login', { password })
      onsuccess()
    } catch (err) {
      error = errorText(errorCode(err))
    } finally {
      busy = false
    }
  }
</script>

<main class="center-screen">
  <form class="narrow" onsubmit={submit}>
    <h2>{t('admin.title')}</h2>
    <label class="field">
      {t('admin.password')}
      <!-- svelte-ignore a11y_autofocus -->
      <input type="password" bind:value={password} autocomplete="current-password" autofocus />
    </label>
    {#if error}<p class="error">{error}</p>{/if}
    <button class="primary" type="submit" disabled={busy || !password}>{t('admin.login')}</button>
  </form>
</main>

<style>
  .error {
    color: var(--danger);
    margin: 0;
  }
</style>
