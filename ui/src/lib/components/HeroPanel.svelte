<script lang="ts">
  import type { Gameserver, GameserverStats, QueryData } from '$lib/api';
  import StatusPill from './StatusPill.svelte';
  import CopyBlock from './CopyBlock.svelte';
  import TelemetryCell from './TelemetryCell.svelte';

  let { gameserver, stats, query, connectionAddress, sftpAddress, gameIcon = '🎮', gameName = '', onaction }:
    {
      gameserver: Gameserver;
      stats: GameserverStats | null;
      query: QueryData | null;
      connectionAddress: string;
      sftpAddress: string;
      gameIcon?: string;
      gameName?: string;
      onaction?: (action: string) => void;
    } = $props();

  const isRunning = $derived(gameserver.status === 'running' || gameserver.status === 'started');
  const isStopped = $derived(gameserver.status === 'stopped');

  const memPercent = $derived(
    stats ? Math.round((stats.memory_usage_mb / stats.memory_limit_mb) * 100) : 0
  );
  const cpuPercent = $derived(stats ? Math.round(stats.cpu_percent) : 0);

  const playersText = $derived(
    query ? `${query.players_online}` : '—'
  );
  const playersMax = $derived(
    query ? ` / ${query.max_players}` : ''
  );
  const playerPercent = $derived(
    query && query.max_players > 0 ? Math.round((query.players_online / query.max_players) * 100) : 0
  );
</script>

<div class="hero" class:running={isRunning} class:stopped={isStopped} onclick={() => window.location.href = `/gameservers/${gameserver.id}`} onkeydown={(e) => e.key === 'Enter' && (window.location.href = `/gameservers/${gameserver.id}`)} role="link" tabindex="0">
  <!-- Identity + status -->
  <div class="head">
    <div class="id-left">
      <div class="icon">{gameIcon}</div>
      <div>
        <div class="name">{gameserver.name}</div>
        <div class="game">{gameName || gameserver.game_id}</div>
      </div>
    </div>
    <StatusPill status={gameserver.status} />
  </div>

  <!-- Connection -->
  <div class="connect-row" onclick={(e) => e.preventDefault()}>
    <CopyBlock label="Connect" value={connectionAddress} primary={true} />
    <CopyBlock label="SFTP" value={sftpAddress} />
  </div>

  <!-- Telemetry -->
  <div class="telemetry">
    <TelemetryCell
      label="Players"
      value={playersText}
      unit={playersMax}
      percent={playerPercent}
      color="live"
    />
    <TelemetryCell
      label="Memory"
      value={stats ? `${(stats.memory_usage_mb / 1024).toFixed(1)}` : '—'}
      unit={stats ? ' GB' : ''}
      detail={stats ? `of ${(stats.memory_limit_mb / 1024).toFixed(0)} GB` : ''}
      percent={memPercent}
      color="accent"
    />
    <TelemetryCell
      label="CPU"
      value={stats ? `${cpuPercent}` : '—'}
      unit={stats ? '%' : ''}
      percent={cpuPercent}
      color="accent"
    />
  </div>

  <!-- Actions -->
  <div class="actions" onclick={(e) => e.preventDefault()}>
    <div class="actions-left">
      {#if isStopped}
        <button class="btn-action start" onclick={(e) => { e.stopPropagation(); onaction?.('start'); }}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.596 8.697l-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z"/></svg>
          Start
        </button>
      {:else}
        <button class="btn-action stop" onclick={(e) => { e.stopPropagation(); onaction?.('stop'); }}>
          <svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
          Stop
        </button>
        <button class="btn-action restart" onclick={(e) => { e.stopPropagation(); onaction?.('restart'); }}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
          Restart
        </button>
      {/if}
    </div>
    <div class="shortcuts">
      <a href="/gameservers/{gameserver.id}/console" class="sc" onclick={(e) => e.stopPropagation()}>Console</a>
      <a href="/gameservers/{gameserver.id}/files" class="sc" onclick={(e) => e.stopPropagation()}>Files</a>
      <a href="/gameservers/{gameserver.id}/backups" class="sc" onclick={(e) => e.stopPropagation()}>Backups</a>
      <a href="/gameservers/{gameserver.id}/settings" class="sc" onclick={(e) => e.stopPropagation()}>Settings</a>
    </div>
  </div>
</div>


<style>
  .hero {
    display: block;
    position: relative;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 12px;
    overflow: hidden;
    cursor: pointer;
    transition: border-color 0.2s;
    animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .hero:hover { border-color: var(--border); }

  @keyframes rise {
    from { opacity: 0; transform: translateY(14px) scale(0.99); }
    to { opacity: 1; transform: translateY(0) scale(1); }
  }

  /* Warm inner glow */
  .hero::before {
    content: ''; position: absolute; inset: 0;
    background: radial-gradient(ellipse 90% 70% at 50% 30%, rgba(232,114,42,0.03) 0%, transparent 60%);
    pointer-events: none;
  }

  /* Status line at top */
  .hero::after {
    content: ''; position: absolute; top: 0; left: 8%; right: 8%; height: 1px;
    opacity: 0.4;
  }
  .hero.running::after {
    background: linear-gradient(90deg, transparent, var(--live), transparent);
    animation: pulse-line 3s ease-in-out infinite;
  }
  .hero.running {
    box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2), 0 0 50px -18px var(--live-glow);
    animation: rise 0.6s cubic-bezier(0.16,1,0.3,1), breathe 5s ease-in-out 0.6s infinite;
  }
  .hero.stopped { box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2); }
  .hero.stopped::after { display: none; }

  @keyframes pulse-line { 0%,100% { opacity: 0.25; } 50% { opacity: 0.5; } }
  @keyframes breathe {
    0%,100% { box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2), 0 0 40px -18px var(--live-glow); }
    50% { box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2), 0 0 60px -12px var(--live-glow); }
  }

  .head {
    display: flex; align-items: center; justify-content: space-between;
    padding: 22px 24px 0;
    position: relative; z-index: 1;
  }
  .id-left { display: flex; align-items: center; gap: 14px; }
  .icon {
    width: 52px; height: 52px; border-radius: 11px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    display: grid; place-items: center; font-size: 1.5rem;
    box-shadow: inset 0 1px 4px rgba(232,114,42,0.04);
  }
  .name { font-weight: 600; font-size: 1.15rem; letter-spacing: -0.02em; }
  .game { font-size: 0.8rem; color: var(--text-tertiary); margin-top: 2px; }

  .connect-row {
    display: flex; gap: 12px;
    padding: 18px 24px 0;
    position: relative; z-index: 1;
  }

  .telemetry {
    display: grid; grid-template-columns: repeat(3, 1fr);
    margin: 16px 24px 0;
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    overflow: hidden;
    position: relative; z-index: 1;
  }
  .telemetry > :global(.cell) { padding: 14px 16px; background: var(--bg-inset); }
  .telemetry > :global(.cell + .cell) { border-left: 1px solid var(--border-dim); }
  /* Target TelemetryCell's root div */
  .telemetry > :global(div) { padding: 14px 16px; background: var(--bg-inset); }
  .telemetry > :global(div + div) { border-left: 1px solid var(--border-dim); }

  .actions {
    display: flex; align-items: center; justify-content: space-between;
    padding: 18px 24px 22px;
    margin-top: 16px;
    border-top: 1px solid var(--border-dim);
    position: relative; z-index: 1;
  }
  .actions-left { display: flex; gap: 6px; }

  .shortcuts { display: flex; gap: 2px; }
  .sc {
    display: inline-flex; align-items: center;
    padding: 6px 10px; border-radius: var(--radius-sm);
    font-size: 0.8rem; font-weight: 450;
    color: var(--text-tertiary); text-decoration: none;
    transition: color 0.15s, background 0.15s;
  }
  .sc:hover { color: var(--accent); background: var(--accent-glow); }

  @media (max-width: 620px) {
    .connect-row { flex-direction: column; }
    .telemetry { grid-template-columns: 1fr; }
    .telemetry > :global(div + div) { border-left: none; border-top: 1px solid var(--border-dim); }
    .actions { flex-direction: column; gap: 12px; }
    .head { flex-direction: column; align-items: flex-start; gap: 12px; }
  }
</style>
