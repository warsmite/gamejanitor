<script lang="ts">
  let { label, value, primary = false }: { label: string; value: string; primary?: boolean } = $props();
  let copied = $state(false);

  function copy() {
    navigator.clipboard.writeText(value).then(() => {
      copied = true;
      setTimeout(() => copied = false, 2000);
    });
  }
</script>

<div class="block" class:primary class:copied onclick={copy} onkeydown={(e) => e.key === 'Enter' && copy()} role="button" tabindex="0">
  <div class="info">
    <span class="label">{label}</span>
    <span class="addr">{value}</span>
  </div>
  <button class="btn" class:btn-primary={primary} class:btn-subtle={!primary} class:copied>
    <svg viewBox="0 0 16 16" fill="currentColor"><path d="M0 6.75C0 5.784.784 5 1.75 5h1.5a.75.75 0 0 1 0 1.5h-1.5a.25.25 0 0 0-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 0 0 .25-.25v-1.5a.75.75 0 0 1 1.5 0v1.5A1.75 1.75 0 0 1 9.25 16h-7.5A1.75 1.75 0 0 1 0 14.25v-7.5z"/><path d="M5 1.75C5 .784 5.784 0 6.75 0h7.5C15.216 0 16 .784 16 1.75v7.5A1.75 1.75 0 0 1 14.25 11h-7.5A1.75 1.75 0 0 1 5 9.25v-7.5zm1.75-.25a.25.25 0 0 0-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 0 0 .25-.25v-7.5a.25.25 0 0 0-.25-.25h-7.5z"/></svg>
    {copied ? 'Copied' : 'Copy'}
  </button>
</div>

<style>
  .block {
    flex: 1;
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    padding: 10px 14px;
    display: flex; align-items: center; justify-content: space-between;
    gap: 10px; cursor: pointer;
    transition: border-color 0.2s;
  }
  .block.primary { border-color: var(--accent-border); }
  .block.primary:hover { border-color: rgba(232,114,42,0.35); }
  .block:not(.primary):hover { border-color: var(--border); }

  .info { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .label {
    font-size: 0.6rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
  }
  .primary .label { color: var(--accent); opacity: 0.7; }
  .addr {
    font-size: 0.88rem; font-family: var(--font-mono); font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .block:not(.primary) .addr { font-size: 0.76rem; color: var(--text-secondary); }

  .btn {
    display: flex; align-items: center; gap: 4px;
    padding: 5px 10px; border-radius: 4px;
    font-family: var(--font-mono); font-size: 0.65rem; font-weight: 500;
    cursor: pointer; transition: background 0.15s; flex-shrink: 0; border: none;
  }
  .btn svg { width: 11px; height: 11px; }
  .btn-primary { background: var(--accent); color: #fff; box-shadow: 0 0 8px rgba(232,114,42,0.15); }
  .btn-primary:hover { background: var(--accent-hover); }
  .btn-subtle { background: var(--bg-elevated); border: 1px solid var(--border-dim); color: var(--text-tertiary); }
  .btn-subtle:hover { color: var(--text-secondary); border-color: var(--border); }
  .btn.copied { background: var(--live); color: #fff; box-shadow: 0 0 8px rgba(34,197,94,0.2); }
</style>
