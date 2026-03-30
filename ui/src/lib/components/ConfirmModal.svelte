<script lang="ts">
  import { confirmState, resolveConfirm } from '$lib/stores/confirm';

  let state = $state({
    open: false, title: '', message: '', confirmLabel: 'Confirm',
    danger: false, inputMode: false, inputPlaceholder: '', inputValue: '',
    resolve: null as any,
  });

  let inputEl: HTMLInputElement | undefined;

  confirmState.subscribe((s) => {
    state = s;
    // Auto-focus input when prompt opens
    if (s.open && s.inputMode) {
      requestAnimationFrame(() => inputEl?.focus());
    }
  });

  function submit() {
    if (state.inputMode && !state.inputValue.trim()) return;
    resolveConfirm(true, state.inputValue);
  }

  function handleKeydown(e: KeyboardEvent) {
    if (!state.open) return;
    if (e.key === 'Escape') resolveConfirm(false);
    if (e.key === 'Enter') submit();
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if state.open}
  <div class="backdrop" onclick={() => resolveConfirm(false)} role="presentation">
    <div class="modal" onclick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
      <div class="modal-title">{state.title}</div>
      {#if state.message}
        <div class="modal-message">{state.message}</div>
      {/if}
      {#if state.inputMode}
        <input
          class="modal-input"
          type="text"
          placeholder={state.inputPlaceholder}
          bind:value={state.inputValue}
          bind:this={inputEl}
        >
      {/if}
      <div class="modal-actions">
        <button class="btn-cancel" onclick={() => resolveConfirm(false)}>Cancel</button>
        <button
          class="btn-confirm"
          class:danger={state.danger}
          disabled={state.inputMode && !state.inputValue.trim()}
          onclick={submit}
        >{state.confirmLabel}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(4px);
    display: grid; place-items: center;
    z-index: 100;
    animation: fade-in 0.15s ease-out;
  }
  @keyframes fade-in { from { opacity: 0; } to { opacity: 1; } }

  .modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 24px;
    min-width: 340px;
    max-width: 440px;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    animation: modal-in 0.2s cubic-bezier(0.16, 1, 0.3, 1);
    will-change: transform, opacity;
  }
  @keyframes modal-in {
    from { opacity: 0; transform: scale(0.96) translateY(8px); }
    to { opacity: 1; transform: scale(1) translateY(0); }
  }

  .modal-title {
    font-size: 1rem; font-weight: 600;
    letter-spacing: -0.01em;
    margin-bottom: 8px;
  }
  .modal-message {
    font-size: 0.84rem; color: var(--text-secondary);
    line-height: 1.5;
    margin-bottom: 16px;
    white-space: pre-line;
  }

  .modal-input {
    width: 100%;
    padding: 9px 14px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    color: var(--text-primary);
    font-family: var(--font-body);
    font-size: 0.85rem;
    outline: none;
    transition: border-color 0.2s;
    margin-bottom: 16px;
  }
  .modal-input::placeholder { color: var(--text-tertiary); opacity: 0.6; }
  .modal-input:focus { border-color: var(--accent-border); }

  .modal-actions {
    display: flex; justify-content: flex-end; gap: 8px;
  }

  .btn-cancel {
    padding: 8px 16px; border-radius: var(--radius-sm);
    background: none; border: 1px solid var(--border-dim);
    color: var(--text-secondary); font-family: var(--font-body);
    font-size: 0.84rem; font-weight: 450; cursor: pointer;
    transition: border-color 0.15s, color 0.15s;
  }
  .btn-cancel:hover { border-color: var(--border); color: var(--text-primary); }

  .btn-confirm {
    padding: 8px 16px; border-radius: var(--radius-sm);
    background: var(--accent); border: none;
    color: #fff; font-family: var(--font-body);
    font-size: 0.84rem; font-weight: 520; cursor: pointer;
    transition: background 0.15s;
    box-shadow: 0 0 12px rgba(232, 114, 42, 0.2);
  }
  .btn-confirm:hover { background: var(--accent-hover); }
  .btn-confirm:disabled { opacity: 0.4; pointer-events: none; }
  .btn-confirm.danger {
    background: var(--danger);
    box-shadow: 0 0 12px rgba(239, 68, 68, 0.2);
  }
  .btn-confirm.danger:hover { background: #dc2626; }
</style>
