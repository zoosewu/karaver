<script lang="ts">
  import { closeDialog, dialog, type SheetAction } from '../lib/dialog.svelte'
  import { t } from '../lib/i18n'

  const d = $derived(dialog.current)
  const actions = $derived(d?.kind === 'sheet' ? (typeof d.actions === 'function' ? d.actions() : d.actions) : [])
  const subtitle = $derived(d?.kind === 'sheet' ? (typeof d.subtitle === 'function' ? d.subtitle() : d.subtitle) : undefined)

  function choose(a: SheetAction) {
    if (a.disabled) return
    if (d?.kind === 'sheet' && d.keepOpen) {
      a.run()
      return
    }
    closeDialog()
    a.run()
  }

  function onKey(e: KeyboardEvent) {
    if (d && e.key === 'Escape') closeDialog(false)
  }
</script>

<svelte:window onkeydown={onKey} />

{#if d}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="backdrop" onclick={() => closeDialog(false)}>
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="panel" role="dialog" aria-modal="true" tabindex="-1" onclick={(e) => e.stopPropagation()}>
      {#if d.kind === 'confirm'}
        <p class="message">{d.message}</p>
        <div class="buttons">
          <button onclick={() => closeDialog(false)}>{t('common.cancel')}</button>
          <!-- svelte-ignore a11y_autofocus -->
          <button class={d.danger ? 'danger-fill' : 'primary'} onclick={() => closeDialog(true)} autofocus>
            {d.confirmText}
          </button>
        </div>
      {:else}
        <div class="sheet-head">
          <div class="title">{d.title}</div>
          {#if subtitle}<div class="muted sub">{subtitle}</div>{/if}
        </div>
        <div class="actions">
          {#each actions as a, i (i)}
            <button class="action" class:danger={a.danger} disabled={a.disabled} onclick={() => choose(a)}>
              {a.label}
              {#if a.hint}<span class="hint">{a.hint}</span>{/if}
            </button>
          {/each}
          <button class="action cancel" onclick={() => closeDialog()}>{d.keepOpen ? t('common.close') : t('common.cancel')}</button>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 90;
    background: rgba(0, 0, 0, 0.55);
    display: grid;
    align-items: end;
    justify-items: center;
    padding: 12px 12px calc(12px + env(safe-area-inset-bottom));
  }
  @media (min-width: 640px) {
    .backdrop {
      align-items: center;
    }
  }
  .panel {
    width: min(100%, 420px);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
    padding: 16px;
    display: grid;
    gap: 14px;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
  }
  .message {
    margin: 0;
    font-size: 1.05rem;
    white-space: pre-line;
    overflow-wrap: anywhere;
  }
  .buttons {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .danger-fill {
    background: var(--danger);
    border-color: var(--danger);
    color: #fff;
    font-weight: 600;
  }
  .sheet-head .title {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .sheet-head .sub {
    font-size: 0.85rem;
    overflow-wrap: anywhere;
  }
  .actions {
    display: grid;
    gap: 6px;
  }
  .action {
    min-height: 48px;
    text-align: left;
    display: grid;
    gap: 2px;
    white-space: normal;
  }
  .action.danger {
    color: var(--danger);
  }
  .action .hint {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .action.cancel {
    text-align: center;
    background: transparent;
  }
</style>
