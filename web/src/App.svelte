<script lang="ts">
  import { router } from './lib/router.svelte'
  import Toasts from './components/Toasts.svelte'
  import Home from './pages/Home.svelte'
  import Room from './pages/Room.svelte'
  import Player from './pages/Player.svelte'
  import Admin from './pages/Admin.svelte'

  const route = $derived(router.route)
</script>

{#if route.name === 'room'}
  {#key route.id}<Room roomId={route.id} />{/key}
{:else if route.name === 'player'}
  {#key route.id}<Player roomId={route.id} />{/key}
{:else if route.name === 'admin'}
  <Admin />
{:else}
  <Home />
{/if}

<Toasts />
