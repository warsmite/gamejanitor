<script lang="ts">
  let { src = '', name = '', size = 48 }: { src?: string; name?: string; size?: number } = $props();
  let failed = $state(false);

  const letter = $derived(name ? name.charAt(0).toUpperCase() : '?');
</script>

<div class="icon" style="width:{size}px; height:{size}px; font-size:{size * 0.4}px;">
  {#if src && !failed}
    <img {src} alt="" onerror={() => failed = true} />
  {:else}
    <span class="fallback">{letter}</span>
  {/if}
</div>

<style>
  .icon {
    border-radius: 10px;
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    display: grid; place-items: center;
    overflow: hidden; flex-shrink: 0;
    box-shadow: inset 0 1px 4px rgba(232, 114, 42, 0.04);
  }
  .icon img { width: 100%; height: 100%; object-fit: cover; }
  .fallback {
    font-family: var(--font-mono);
    font-weight: 600;
    color: var(--text-tertiary);
    user-select: none;
  }
</style>
