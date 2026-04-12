<script lang="ts">
  let { status }: { status: string } = $props();

  const statusClass = $derived(
    status === 'running' ? 'running' :
    status === 'stopped' ? 'stopped' :
    status === 'archived' ? 'archived' :
    status === 'error' ? 'error' :
    status === 'unreachable' ? 'unreachable' :
    'starting' // installing, starting, stopping
  );
</script>

<span class="pill {statusClass}">
  <span class="dot"></span>
  {status.charAt(0).toUpperCase() + status.slice(1)}
</span>

<style>
  .pill {
    display: inline-flex; align-items: center; gap: 7px;
    padding: 5px 13px 5px 10px; border-radius: 100px;
    font-size: 0.72rem; font-weight: 500; font-family: var(--font-mono);
    letter-spacing: 0.02em;
  }
  .dot { width: 7px; height: 7px; border-radius: 50%; }

  .running { background: var(--live-dim); color: var(--live); border: 1px solid rgba(34,197,94,0.12); }
  .running .dot { background: var(--live); box-shadow: 0 0 8px var(--live); animation: pulse 2s ease-in-out infinite; }

  .stopped { background: rgba(82,82,74,0.08); color: var(--idle); border: 1px solid rgba(82,82,74,0.1); }
  .stopped .dot { background: var(--idle); }

  .starting { background: rgba(245,158,11,0.08); color: var(--caution); border: 1px solid rgba(245,158,11,0.12); }
  .starting .dot { background: var(--caution); animation: pulse 1.5s ease-in-out infinite; }

  .error { background: rgba(239,68,68,0.08); color: var(--danger); border: 1px solid rgba(239,68,68,0.12); }
  .error .dot { background: var(--danger); }

  .archived { background: rgba(139,92,246,0.08); color: rgb(167,139,250); border: 1px solid rgba(139,92,246,0.12); }
  .archived .dot { background: rgb(167,139,250); }

  .unreachable { background: rgba(239,68,68,0.08); color: var(--danger); border: 1px solid rgba(239,68,68,0.12); }
  .unreachable .dot { background: var(--danger); animation: pulse 2s ease-in-out infinite; }

  @keyframes pulse {
    0%, 100% { transform: scale(1); opacity: 1; }
    50% { transform: scale(0.85); opacity: 0.4; }
  }
</style>
