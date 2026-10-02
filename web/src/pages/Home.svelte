<script lang="ts">
  import { t } from '../lib/i18n'
  import { router } from '../lib/router.svelte'

  let code = $state('')

  function go(e: SubmitEvent) {
    e.preventDefault()
    const id = code.trim()
    if (id) router.navigate(`/r/${encodeURIComponent(id)}`)
  }
</script>

<main class="center-screen">
  <div class="narrow">
    <h1>{t('home.title')}</h1>
    <form class="row" onsubmit={go}>
      <input bind:value={code} placeholder={t('home.enterCode')} autocapitalize="off" autocomplete="off" />
      <button class="primary" type="submit">{t('home.go')}</button>
    </form>
    <p class="muted">{t('home.scanHint')}</p>
    <p class="muted">{t('home.tvHint', { url: `${location.origin}/tv` })}</p>
    <a href="/admin" onclick={(e) => (e.preventDefault(), router.navigate('/admin'))}>{t('home.admin')}</a>
  </div>
</main>
