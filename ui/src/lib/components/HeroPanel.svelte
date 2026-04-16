<script lang="ts">
  import type { Gameserver, GameserverStats, QueryData } from '$lib/api';
  import { navigate } from '$lib/router';
  import { gameserverStore } from '$lib/stores';
  import { isRunning, isStopped, isArchived, isUnreachable } from '$lib/gameserver';
  import GameIcon from './GameIcon.svelte';
  import StatusPill from './StatusPill.svelte';
  import CopyBlock from './CopyBlock.svelte';
  import TelemetryCell from './TelemetryCell.svelte';

  let { gameserver, stats, query, connectionAddress, iconPath = '', gameName = '', onaction }:
    {
      gameserver: Gameserver;
      stats: GameserverStats | null;
      query: QueryData | null;
      connectionAddress: string;
      iconPath?: string;
      gameName?: string;
      onaction?: (action: string) => void;
    } = $props();

  const can = (p: string) => gameserverStore.canOnGameserver(p, gameserver.id);
  const canConsole = $derived(can('gameserver.logs'));
  const canFiles = $derived(can('gameserver.files.read'));
  const canBackups = $derived(can('backup.read'));
  const canSettings = $derived(
    can('gameserver.configure.name') || can('gameserver.configure.env') ||
    can('gameserver.configure.resources') || can('gameserver.configure.ports') ||
    can('gameserver.configure.auto-restart')
  );

  const memPercent = $derived(
    stats ? Math.round((stats.memory_usage_mb / stats.memory_limit_mb) * 100) : 0
  );
  const cpuPercent = $derived(stats ? Math.round(stats.cpu_percent) : 0);
  const storageMB = $derived(stats ? Math.round(stats.volume_size_bytes / (1024 * 1024)) : 0);
  const storagePercent = $derived(
    stats?.storage_limit_mb ? Math.round((storageMB / stats.storage_limit_mb) * 100) : (stats ? 100 : 0)
  );
</script>

<div class="hero" class:running={isRunning(gameserver)} class:stopped={isStopped(gameserver)} onclick={() => navigate(`/gameservers/${gameserver.id}`)} onkeydown={(e) => e.key === 'Enter' && navigate(`/gameservers/${gameserver.id}`)} role="link" tabindex="0">
  <!-- Identity + status -->
  <div class="head">
    <div class="id-left">
      <GameIcon src={iconPath} name={gameName} size={52} />
      <div>
        <div class="name">{gameserver.name}</div>
        <div class="game">{gameName || gameserver.game_id}</div>
      </div>
    </div>
    <StatusPill {gameserver} />
  </div>

  <!-- Connection -->
  <div class="connect-row" onclick={(e) => e.stopPropagation()}>
    <CopyBlock label="Connect" value={connectionAddress} primary={true} />
    {#if isRunning(gameserver) && query}
      <div class="player-count">
        <svg viewBox="0 0 16 16" fill="currentColor"><path d="M15 14s1 0 1-1-1-4-5-4-5 3-5 4 1 1 1 1h8zm-7.978-1A.261.261 0 0 1 7 12.996c.001-.264.167-1.03.76-1.72C8.312 10.629 9.282 10 11 10c1.717 0 2.687.63 3.24 1.276.593.69.758 1.457.76 1.72l-.008.002a.274.274 0 0 1-.014.002H7.022zM11 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4zm3-2a3 3 0 1 1-6 0 3 3 0 0 1 6 0zM6.936 9.28a5.88 5.88 0 0 0-1.23-.247A7.35 7.35 0 0 0 5 9c-4 0-5 3-5 4 0 .667.333 1 1 1h4.216A2.238 2.238 0 0 1 5 13c0-1.01.377-2.042 1.09-2.904.243-.294.526-.569.846-.816zM4.92 10A5.493 5.493 0 0 0 4 13H1c0-.26.164-1.03.76-1.724.545-.636 1.492-1.256 3.16-1.275zM1.5 5.5a3 3 0 1 1 6 0 3 3 0 0 1-6 0zm3-2a2 2 0 1 0 0 4 2 2 0 0 0 0-4z"/></svg>
        <span class="player-text">{query.players_online}<span class="player-max">/{query.max_players}</span></span>
      </div>
    {/if}
  </div>

  {#if !isStopped(gameserver) && !isArchived(gameserver)}
    <!-- Telemetry -->
    <div class="telemetry">
      <TelemetryCell
        label="Memory"
        value={stats ? `${(stats.memory_usage_mb / 1024).toFixed(1)}` : '—'}
        unit={stats ? ' GB' : ''}
        detail={stats ? `of ${(stats.memory_limit_mb / 1024).toFixed(0)} GB` : ''}
        percent={memPercent}
        color="mem"
      />
      <TelemetryCell
        label="CPU"
        value={stats ? `${cpuPercent}` : '—'}
        unit={stats ? '%' : ''}
        percent={cpuPercent}
        color="accent"
      />
      <TelemetryCell
        label="Storage"
        value={stats ? `${storageMB < 1024 ? storageMB + '' : (storageMB / 1024).toFixed(1)}` : '—'}
        unit={stats ? (storageMB < 1024 ? ' MB' : ' GB') : ''}
        detail={stats?.storage_limit_mb ? `of ${(stats.storage_limit_mb / 1024).toFixed(0)} GB` : ''}
        percent={storagePercent}
        color="accent"
      />
    </div>
  {/if}

  <!-- Actions -->
  <div class="actions" onclick={(e) => e.preventDefault()}>
    <div class="actions-left">
      {#if isArchived(gameserver)}
        <button class="btn-action start" onclick={(e) => { e.stopPropagation(); onaction?.('unarchive'); }}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.596 8.697l-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z"/></svg>
          Unarchive
        </button>
      {:else if isStopped(gameserver)}
        <button class="btn-action start" onclick={(e) => { e.stopPropagation(); onaction?.('start'); }} disabled={isUnreachable(gameserver)}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.596 8.697l-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z"/></svg>
          Start
        </button>
      {:else}
        <button class="btn-action stop" onclick={(e) => { e.stopPropagation(); onaction?.('stop'); }} disabled={isUnreachable(gameserver)}>
          <svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
          Stop
        </button>
        <button class="btn-action restart" onclick={(e) => { e.stopPropagation(); onaction?.('restart'); }} disabled={isUnreachable(gameserver)}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
          Restart
        </button>
      {/if}
    </div>
    {#if !isArchived(gameserver)}
      <div class="shortcuts">
        {#if canConsole}
          <a href="/gameservers/{gameserver.id}/console" class="sc" onclick={(e) => e.stopPropagation()}>Console</a>
        {/if}
        {#if canFiles}
          <a href="/gameservers/{gameserver.id}/files" class="sc" onclick={(e) => e.stopPropagation()}>Files</a>
        {/if}
        {#if canBackups}
          <a href="/gameservers/{gameserver.id}/backups" class="sc" onclick={(e) => e.stopPropagation()}>Backups</a>
        {/if}
        {#if canSettings}
          <a href="/gameservers/{gameserver.id}/settings" class="sc" onclick={(e) => e.stopPropagation()}>Settings</a>
        {/if}
      </div>
    {/if}
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
    will-change: transform, opacity;
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
  .name { font-weight: 600; font-size: 1.15rem; letter-spacing: -0.02em; }
  .game { font-size: 0.8rem; color: var(--text-tertiary); margin-top: 2px; }

  .connect-row {
    display: flex; gap: 12px; align-items: center;
    padding: 18px 24px 0;
    position: relative; z-index: 1;
  }
  .player-count {
    display: flex; align-items: center; gap: 5px;
    padding: 6px 11px;
    background: var(--live-dim);
    border: 1px solid rgba(34,197,94,0.12);
    border-radius: 100px;
    flex-shrink: 0;
  }
  .player-count svg { width: 13px; height: 13px; color: var(--live); opacity: 0.7; }
  .player-text {
    font-family: var(--font-mono); font-size: 0.78rem;
    font-weight: 600; color: var(--live);
  }
  .player-max {
    font-weight: 400; opacity: 0.5;
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
