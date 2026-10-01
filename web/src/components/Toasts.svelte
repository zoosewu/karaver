<script lang="ts">
  import { toasts } from '../lib/toast.svelte'
</script>

<div class="toasts" aria-live="polite">
  {#each toasts as t (t.id)}
    <div class="toast" class:error={t.kind === 'error'}>{t.text}</div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    left: 50%;
    bottom: calc(24px + env(safe-area-inset-bottom));
    transform: translateX(-50%);
    display: grid;
    gap: 8px;
    z-index: 100;
    width: min(92vw, 420px);
    pointer-events: none;
  }
  .toast {
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 10px 14px;
    text-align: center;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  }
  .toast.error {
    border-color: var(--danger);
    color: var(--danger);
  }
</style>
