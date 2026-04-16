<script lang="ts">
  import type { Gameserver } from '$lib/api';
  import { phaseOf, phaseLabel } from '$lib/gameserver';

  // Accepts either a full gameserver (preferred — derives from primary facts)
  // or a raw phase string (for contexts where the full object isn't available,
  // like worker rows that use "online"/"offline").
  let { gameserver, phase }:
    { gameserver?: Gameserver | null; phase?: string } = $props();

  const resolved = $derived<string>(
    gameserver ? (phaseOf(gameserver) || '') : (phase || '')
  );

  const label = $derived(phaseLabel(resolved as any));

  // Group phases into visual classes. The CSS styles five buckets — live,
  // caution, danger, idle, archived — and every phase maps to one of them.
  const bucket = $derived(
    resolved === 'running'            ? 'live' :
    resolved === 'error'              ? 'danger' :
    resolved === 'unreachable'        ? 'danger' :
    resolved === 'deleting'           ? 'danger' :
    resolved === 'archived'           ? 'archived' :
    resolved === 'stopped'            ? 'idle' :
                                        'caution'   // installing/starting/stopping
  );

  // Show a pulsing dot for transitional and dangerous states.
  const pulse = $derived(
    resolved === 'running' ||
    resolved === 'unreachable' ||
    resolved === 'deleting' ||
    bucket === 'caution'
  );
</script>

{#if label}
  <span class="pill {bucket}" class:pulse>
    <span class="dot"></span>
    {label}
  </span>
{/if}

<style>
  .pill {
    display: inline-flex; align-items: center; gap: 7px;
    padding: 5px 13px 5px 10px; border-radius: 100px;
    font-size: 0.72rem; font-weight: 500; font-family: var(--font-mono);
    letter-spacing: 0.02em;
  }
  .dot { width: 7px; height: 7px; border-radius: 50%; }

  .live     { background: var(--live-dim);                color: var(--live);    border: 1px solid rgba(34,197,94,0.12); }
  .live .dot { background: var(--live); box-shadow: 0 0 8px var(--live); }

  .caution  { background: rgba(245,158,11,0.08);          color: var(--caution); border: 1px solid rgba(245,158,11,0.12); }
  .caution .dot { background: var(--caution); }

  .danger   { background: rgba(239,68,68,0.08);           color: var(--danger);  border: 1px solid rgba(239,68,68,0.12); }
  .danger .dot { background: var(--danger); }

  .idle     { background: rgba(82,82,74,0.08);            color: var(--idle);    border: 1px solid rgba(82,82,74,0.1); }
  .idle .dot { background: var(--idle); }

  .archived { background: rgba(139,92,246,0.08);          color: rgb(167,139,250); border: 1px solid rgba(139,92,246,0.12); }
  .archived .dot { background: rgb(167,139,250); }

  .pulse .dot { animation: pulse 2s ease-in-out infinite; }

  @keyframes pulse {
    0%, 100% { transform: scale(1); opacity: 1; }
    50% { transform: scale(0.85); opacity: 0.4; }
  }
</style>
