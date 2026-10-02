<script lang="ts">
  import type { Snippet } from 'svelte'

  // A single line that scrolls back and forth only when it does not fit.
  // Pass plain `text`, or `children` for styled content on one line.
  let { text = '', class: cls = '', children }: { text?: string; class?: string; children?: Snippet } = $props()

  let box = $state<HTMLElement>()
  let inner = $state<HTMLElement>()
  let overflow = $state(0)

  $effect(() => {
    void text
    if (!box || !inner) return
    const b = box
    const i = inner
    const measure = () => (overflow = Math.max(0, Math.ceil(i.scrollWidth - b.clientWidth)))
    measure()
    // Watch both: the box changes with the layout, the content with children.
    const ro = new ResizeObserver(measure)
    ro.observe(b)
    ro.observe(i)
    return () => ro.disconnect()
  })

  // ~40px per second, plus the pauses at both ends built into the keyframes.
  const duration = $derived(Math.max(4, overflow / 40 / 0.7))
</script>

<span class="marquee {cls}" bind:this={box} title={overflow > 0 && text ? text : undefined}>
  <span
    class="inner"
    class:run={overflow > 0}
    bind:this={inner}
    style:--shift={`-${overflow}px`}
    style:--duration={`${duration}s`}
    >{#if children}{@render children()}{:else}{text}{/if}</span
  >
</span>

<style>
  .marquee {
    display: block;
    overflow: hidden;
    white-space: nowrap;
    min-width: 0;
  }
  .inner {
    display: inline-block;
  }
  .run {
    animation: marquee var(--duration) ease-in-out infinite alternate;
  }
  @keyframes marquee {
    0%,
    15% {
      transform: translateX(0);
    }
    85%,
    100% {
      transform: translateX(var(--shift));
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .run {
      animation: none;
    }
  }
</style>
