<script lang="ts">
  // Honest per-signal state breakdown for a gameserver. Each primary fact gets
  // its own row — worker connection, process state, readiness, active
  // operation, error. The user sees the full picture instead of a compressed
  // status pill. Appeals to power users who want to know which specific signal
  // changed when something's off.

  import type { Gameserver } from '$lib/api';
  import { phaseLabels } from '$lib/stores/gameservers.svelte';

  let { gameserver }: { gameserver: Gameserver } = $props();

  type Tone = 'ok' | 'bad' | 'warn' | 'unknown' | 'na';

  const workerTone = $derived<Tone>(
    gameserver.desired_state === 'archived' ? 'na' :
    gameserver.worker_online ? 'ok' : 'bad'
  );
  const workerLabel = $derived(
    gameserver.desired_state === 'archived' ? 'Archived'
    : gameserver.worker_online ? 'Online'
    : 'Offline'
  );

  const processTone = $derived<Tone>(
    !gameserver.worker_online ? 'unknown' :
    gameserver.process_state === 'running' ? 'ok' :
    gameserver.process_state === 'exited' && !!gameserver.error_reason ? 'bad' :
    gameserver.process_state === 'exited' ? 'na' :
    gameserver.process_state === 'none' ? 'na' :
    'warn'
  );
  const processLabel = $derived(
    !gameserver.worker_online ? 'Unknown (worker offline)' :
    gameserver.process_state === 'running' ? 'Running' :
    gameserver.process_state === 'starting' ? 'Starting' :
    gameserver.process_state === 'creating' ? 'Creating' :
    gameserver.process_state === 'exited'
      ? `Exited${gameserver.exit_code ? ` (code ${gameserver.exit_code})` : ''}`
    : 'Not running'
  );

  // Ready is only meaningful when the process is alive.
  const showReady = $derived(gameserver.process_state === 'running');
  const readyTone = $derived<Tone>(gameserver.ready ? 'ok' : 'warn');
  const readyLabel = $derived(
    gameserver.ready ? 'Accepting connections' : 'Waiting for ready signal'
  );

  const operationLabel = $derived(
    gameserver.operation
      ? (phaseLabels[gameserver.operation.phase] || gameserver.operation.phase)
      : ''
  );
  const operationProgress = $derived(gameserver.operation?.progress);

  function uptimeFrom(iso?: string): string {
    if (!iso) return '';
    const seconds = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = seconds % 60;
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
  }

  // Re-evaluate uptime every second while the process is running.
  let tick = $state(0);
  $effect(() => {
    if (gameserver.process_state === 'running' && gameserver.started_at) {
      const id = setInterval(() => { tick++; }, 1000);
      return () => clearInterval(id);
    }
  });
  const uptime = $derived(tick >= 0 ? uptimeFrom(gameserver.started_at) : '');
</script>

<div class="panel">
  <div class="panel-title">State</div>
  <div class="conditions">
    <div class="row">
      <span class="label">Worker</span>
      <span class="value {workerTone}">
        <span class="dot"></span>
        {workerLabel}
      </span>
    </div>

    <div class="row">
      <span class="label">Process</span>
      <span class="value {processTone}">
        <span class="dot"></span>
        {processLabel}
        {#if gameserver.process_state === 'running' && uptime}
          <span class="aux">· up {uptime}</span>
        {/if}
      </span>
    </div>

    {#if showReady}
      <div class="row">
        <span class="label">Ready</span>
        <span class="value {readyTone}">
          <span class="dot"></span>
          {readyLabel}
        </span>
      </div>
    {/if}

    {#if gameserver.operation}
      <div class="row">
        <span class="label">Operation</span>
        <span class="value warn">
          <span class="dot pulse"></span>
          {operationLabel}
          {#if operationProgress && operationProgress.percent > 0}
            <span class="aux">· {operationProgress.percent.toFixed(0)}%</span>
          {/if}
        </span>
      </div>
    {/if}

    {#if gameserver.error_reason}
      <div class="error">
        <div class="error-title">Error</div>
        <div class="error-text">{gameserver.error_reason}</div>
      </div>
    {/if}
  </div>
</div>

<style>
  .panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .panel-title {
    padding: 14px 18px 0;
    font-size: 0.82rem; font-weight: 550;
    color: var(--text-secondary);
  }

  .conditions { padding: 12px 18px 16px; }

  .row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 7px 0;
  }
  .row + .row { border-top: 1px solid var(--border-dim); }

  .label { font-size: 0.78rem; color: var(--text-tertiary); }
  .value {
    display: inline-flex; align-items: center; gap: 7px;
    font-size: 0.82rem; font-family: var(--font-mono); font-weight: 500;
  }
  .aux { color: var(--text-tertiary); font-weight: 400; margin-left: 4px; }

  .dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }

  .value.ok        { color: var(--live); }
  .value.ok .dot   { background: var(--live); box-shadow: 0 0 6px var(--live-glow); }

  .value.warn      { color: var(--caution); }
  .value.warn .dot { background: var(--caution); }

  .value.bad       { color: var(--danger); }
  .value.bad .dot  { background: var(--danger); }

  .value.unknown      { color: var(--text-tertiary); }
  .value.unknown .dot { background: var(--text-tertiary); opacity: 0.5; }

  .value.na      { color: var(--text-tertiary); }
  .value.na .dot { background: transparent; border: 1px solid var(--text-tertiary); opacity: 0.5; }

  .dot.pulse { animation: pulse 1.5s ease-in-out infinite; }
  @keyframes pulse {
    0%, 100% { opacity: 1; transform: scale(1); }
    50% { opacity: 0.5; transform: scale(0.8); }
  }

  .error {
    margin-top: 10px; padding: 10px 12px;
    background: rgba(239,68,68,0.06);
    border: 1px solid rgba(239,68,68,0.15);
    border-radius: var(--radius-sm);
  }
  .error-title {
    font-size: 0.7rem; font-weight: 600;
    color: var(--danger); text-transform: uppercase;
    letter-spacing: 0.08em; margin-bottom: 4px;
  }
  .error-text {
    font-size: 0.82rem; color: var(--text-primary);
    line-height: 1.4; word-break: break-word;
  }
</style>
