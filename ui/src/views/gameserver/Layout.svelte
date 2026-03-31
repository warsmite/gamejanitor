<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/api';
  import { gameserverStore, formatUptime, phaseLabels, toast } from '$lib/stores';
  import { GameIcon, StatusPill } from '$lib/components';
  import { getRoute, navigate, isActive } from '$lib/router';
  import { embedded, basePath } from '$lib/base';
  import { parseLine } from '$lib/logparse';

  const connAddr = $derived(gameserverStore.connectionAddress(id));

  let addrCopied = $state(false);
  function copyAddr() {
    if (!connAddr) return;
    navigator.clipboard.writeText(connAddr).then(() => {
      addrCopied = true;
      setTimeout(() => addrCopied = false, 1500);
    });
  }

  function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1048576) return `${(bytes / 1024).toFixed(0)} KB`;
    if (bytes < 1073741824) return `${(bytes / 1048576).toFixed(0)} MB`;
    return `${(bytes / 1073741824).toFixed(1)} GB`;
  }

  const can = (p: string) => gameserverStore.can(p);
  import type { Snippet } from 'svelte';

  let { id, children }: { id: string; children: Snippet } = $props();

  const route = $derived(getRoute());
  const currentPath = $derived(route.path);

  const gsState = $derived(gameserverStore.getState(id));
  const gameserver = $derived(gsState?.gameserver ?? null);
  const game = $derived(gameserverStore.gameFor(gameserver?.game_id ?? ''));
  const operation = $derived(gameserver?.operation ?? null);

  // Mods tab visibility: fetch config once to check if game supports mods
  let hasModsSupport = $state(false);
  let modsConfigChecked = false;
  $effect(() => {
    if (!gameserver || modsConfigChecked) return;
    modsConfigChecked = true;
    api.mods.config(id).then(cfg => {
      hasModsSupport = (cfg?.categories?.length ?? 0) > 0 || !!cfg?.loader || !!cfg?.version;
    }).catch(() => { hasModsSupport = false; });
  });

  // Operation progress stream — connects to dedicated SSE endpoint when
  // an operation is active on this gameserver. Provides real-time progress
  // without using the main event bus.
  let operationSource: EventSource | null = null;

  $effect(() => {
    const hasOperation = !!operation;

    if (hasOperation && !operationSource) {
      const url = `${basePath}/api/gameservers/${id}/operation`;
      const es = new EventSource(url);
      es.onmessage = (e) => {
        try {
          const op = JSON.parse(e.data);
          const state = gameserverStore.getState(id);
          if (state) {
            state.gameserver = { ...state.gameserver, operation: op };
          }
        } catch {}
      };
      es.onerror = () => {
        es.close();
        operationSource = null;
      };
      operationSource = es;
    }

    if (!hasOperation && operationSource) {
      operationSource.close();
      operationSource = null;
    }
  });

  onDestroy(() => {
    operationSource?.close();
  });

  const stats = $derived(gsState?.stats ?? null);
  const query = $derived(gsState?.query ?? null);
  const logLines = $derived(gsState?.logLines ?? []);
  const isRunning = $derived(gameserverStore.isRunning(id));
  const isStopped = $derived(gameserverStore.isStopped(id));
  const isTransitioning = $derived(() => {
    const s = gameserver?.status;
    return s === 'starting' || s === 'installing' || s === 'stopping';
  });

  const storeLoading = $derived(gameserverStore.loading);

  // Edge case: gs not in store (direct URL nav, or deleted)
  let notFound = $state(false);
  $effect(() => {
    if (gameserverStore.initialized && !gsState && !notFound) {
      // Try fetching directly
      api.gameservers.get(id).then(() => {
        // Shouldn't normally happen — store should have it
        // But if it's there, the store's SSE will pick it up
      }).catch((e) => {
        console.warn('Layout: gameserver not found', id, e);
        notFound = true;
      });
    }
    if (gsState) notFound = false;
  });

  // Uptime ticker — local to this component
  let uptime = $state('');
  let uptimeInterval: ReturnType<typeof setInterval>;

  onMount(() => {
    uptimeInterval = setInterval(() => {
      uptime = gsState?.containerStartedAt ? formatUptime(gsState.containerStartedAt) : '';
    }, 1000);
  });

  onDestroy(() => {
    if (uptimeInterval) clearInterval(uptimeInterval);
  });

  const tabs = $derived([
    { label: 'Overview', path: '' },
    ...(can('gameserver.logs') ? [{ label: 'Console', path: '/console' }] : []),
    ...(can('gameserver.files.read') ? [{ label: 'Files', path: '/files' }] : []),
    ...(can('backup.read') ? [{ label: 'Backups', path: '/backups' }] : []),
    ...(can('schedule.read') ? [{ label: 'Schedules', path: '/schedules' }] : []),
    ...(hasModsSupport && can('gameserver.mods.read') ? [{ label: 'Mods', path: '/mods' }] : []),
    { label: 'Settings', path: '/settings' },
  ]);

  function tabHref(tab: typeof tabs[0]) {
    if (embedded) return tab.path || '/';
    return `/gameservers/${id}${tab.path}`;
  }

  function isActiveTab(tab: typeof tabs[0]) {
    if (embedded) {
      if (tab.path === '') return currentPath === '/' || currentPath === '';
      return currentPath.startsWith(tab.path);
    }
    const base = `/gameservers/${id}`;
    if (tab.path === '') {
      return currentPath === base || currentPath === base + '/';
    }
    return currentPath.startsWith(base + tab.path);
  }

  async function handleAction(action: string) {
    if (!gameserver) return;
    try {
      if (action === 'start') await api.gameservers.start(id);
      else if (action === 'stop') await api.gameservers.stop(id);
      else if (action === 'restart') await api.gameservers.restart(id);
    } catch (e: any) {
      toast(`Failed to ${action}: ${e.message}`, 'error');
    }
  }
</script>

<main>
  {#if !embedded}
    <a href="/" class="breadcrumb">
      <svg viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M11.354 1.646a.5.5 0 0 1 0 .708L5.707 8l5.647 5.646a.5.5 0 0 1-.708.708l-6-6a.5.5 0 0 1 0-.708l6-6a.5.5 0 0 1 .708 0z"/></svg>
      Gameservers
    </a>
  {/if}

  {#if storeLoading}
    <p class="loading-text">Loading...</p>
  {:else if notFound}
    <div class="error-text">
      <p>Gameserver not found.</p>
      {#if !embedded}
        <a href="/" class="btn-accent">Back to Dashboard</a>
      {/if}
    </div>
  {:else if gameserver}
    <!-- Server Header -->
    <div class="srv-header" class:running={isRunning} class:stopped={isStopped}>
      <div class="srv-identity">
        <div class="srv-id-left">
          <GameIcon src={game?.icon_path || ''} name={game?.name || gameserver.game_id} size={50} />
          <div>
            <div class="srv-name">{gameserver.name}</div>
            <div class="srv-meta">
              <span class="srv-game">{game?.name || gameserver.game_id}</span>
              {#if connAddr}
                <span class="srv-meta-sep">·</span>
                <button class="srv-addr" onclick={(e) => { e.stopPropagation(); copyAddr(); }}>
                  {addrCopied ? 'Copied!' : connAddr}
                </button>
              {/if}
            </div>
          </div>
        </div>
        <div class="srv-id-right">
          {#if operation}
            <span class="op-badge">
              <span class="op-dot"></span>
              {#if operation.progress && operation.progress.total_bytes}
                {phaseLabels[operation.phase] || operation.phase} {operation.progress.percent.toFixed(0)}% ({formatBytes(operation.progress.completed_bytes ?? 0)} / {formatBytes(operation.progress.total_bytes)})
              {:else}
                {phaseLabels[operation.phase] || operation.phase}
              {/if}
            </span>
          {/if}
          <StatusPill status={gameserver.status} />
        </div>
      </div>

      <div class="srv-actions">
        <div class="srv-actions-left">
          {#if isStopped}
            {#if can('gameserver.start')}
              <button class="btn-action start" onclick={() => handleAction('start')} disabled={isTransitioning() || !!operation}>
                <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.596 8.697l-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z"/></svg>
                Start
              </button>
            {/if}
          {:else}
            {#if can('gameserver.stop')}
              <button class="btn-action stop" onclick={() => handleAction('stop')} disabled={gameserver?.status === 'stopping'}>
                <svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
                Stop
              </button>
            {/if}
            {#if can('gameserver.restart')}
              <button class="btn-action restart" onclick={() => handleAction('restart')} disabled={gameserver?.status === 'stopping'}>
                <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
                Restart
              </button>
            {/if}
          {/if}
        </div>

        <div class="srv-stats">
          {#if query}
            <span class="stat players">{query.players_online}<span class="stat-dim">/{query.max_players}</span></span>
            <span class="stat-sep">·</span>
          {/if}
          {#if stats}
            <span class="stat mem">{(stats.memory_usage_mb / 1024).toFixed(1)}<span class="stat-dim">/{(stats.memory_limit_mb / 1024).toFixed(0)} GB</span></span>
            <span class="stat-sep">·</span>
            <span class="stat cpu">{Math.round(stats.cpu_percent)}% <span class="stat-dim">CPU</span></span>
            <span class="stat-sep">·</span>
            {@const storageMB = Math.round(stats.volume_size_bytes / (1024 * 1024))}
            <span class="stat">{storageMB < 1024 ? `${storageMB} MB` : `${(storageMB / 1024).toFixed(1)} GB`} <span class="stat-dim">disk</span></span>
          {/if}
          {#if uptime}
            {#if stats}<span class="stat-sep">·</span>{/if}
            <span class="stat uptime">
              <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 3.5a.5.5 0 0 0-1 0V8a.5.5 0 0 0 .252.434l3.5 2a.5.5 0 0 0 .496-.868L8 7.71V3.5z"/><path d="M8 16A8 8 0 1 0 8 0a8 8 0 0 0 0 16zm7-8A7 7 0 1 1 1 8a7 7 0 0 1 14 0z"/></svg>
              {uptime}
            </span>
          {/if}
        </div>
      </div>

      {#if !isStopped && logLines.length > 0}
        <a href={tabHref({ label: 'Console', path: '/console' })} class="log-strip">
          {#each logLines as line}
            <div class="log-strip-line">{#each parseLine(line) as seg}<span class={seg.cls}>{seg.text}</span>{/each}</div>
          {/each}
        </a>
      {/if}
    </div>

    <!-- Tab Bar -->
    <nav class="tabs">
      {#each tabs as tab}
        <a
          class="tab"
          class:active={isActiveTab(tab)}
          href={tabHref(tab)}
        >{tab.label}</a>
      {/each}
    </nav>

    <!-- Tab Content -->
    {@render children()}
  {/if}
</main>

<style>
  .breadcrumb {
    display: inline-flex; align-items: center; gap: 6px;
    font-size: 0.84rem; color: var(--text-tertiary);
    text-decoration: none; margin-bottom: 20px;
    transition: color 0.15s;
  }
  .breadcrumb:hover { color: var(--accent); }
  .breadcrumb svg { width: 16px; height: 16px; }

  .srv-header {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 12px;
    overflow: hidden; position: relative;
    margin-bottom: 0;
    animation: enter 0.5s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes enter {
    from { opacity: 0; transform: translateY(12px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .srv-header::before {
    content: ''; position: absolute; inset: 0;
    background: radial-gradient(ellipse 80% 50% at 50% 20%, rgba(232,114,42,0.02) 0%, transparent 60%);
    pointer-events: none;
  }
  .srv-header::after {
    content: ''; position: absolute; top: 0; left: 8%; right: 8%; height: 1px;
    opacity: 0.4;
  }
  .srv-header.running::after {
    background: linear-gradient(90deg, transparent, var(--live), transparent);
    animation: pulse-line 3s ease-in-out infinite;
  }
  .srv-header.running {
    box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2), 0 0 50px -18px var(--live-glow);
    animation: enter 0.5s cubic-bezier(0.16,1,0.3,1), breathe 5s ease-in-out 0.5s infinite;
  }
  .srv-header.stopped {
    box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2);
  }
  .srv-header.stopped::after { display: none; }

  @keyframes pulse-line { 0%,100% { opacity: 0.25; } 50% { opacity: 0.5; } }
  @keyframes breathe {
    0%,100% { box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2), 0 0 40px -18px var(--live-glow); }
    50% { box-shadow: 0 1px 2px rgba(0,0,0,0.3), 0 4px 16px rgba(0,0,0,0.2), 0 0 60px -12px var(--live-glow); }
  }

  .srv-identity {
    display: flex; align-items: center; justify-content: space-between;
    padding: 20px 24px;
    position: relative; z-index: 1;
  }
  .srv-id-left { display: flex; align-items: center; gap: 14px; }
  .srv-name { font-weight: 600; font-size: 1.2rem; letter-spacing: -0.02em; }
  .srv-meta { display: flex; align-items: center; gap: 6px; margin-top: 2px; }
  .srv-meta-sep { color: var(--text-tertiary); opacity: 0.3; font-size: 0.78rem; }
  .srv-game { font-size: 0.8rem; color: var(--text-tertiary); }
  .srv-addr {
    font-size: 0.78rem; font-family: var(--font-mono); color: var(--text-tertiary);
    background: none; border: none; padding: 0; cursor: pointer;
    transition: color 0.15s;
  }
  .srv-addr:hover { color: var(--accent); }
  .srv-id-right { display: flex; align-items: center; gap: 8px; }
  .op-badge {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 4px 11px 4px 8px; border-radius: 100px;
    font-size: 0.68rem; font-weight: 500; font-family: var(--font-mono);
    letter-spacing: 0.02em;
    background: var(--accent-dim);
    color: var(--accent);
    border: 1px solid var(--accent-border);
    animation: op-in 0.25s ease-out;
  }
  .op-dot {
    width: 6px; height: 6px; border-radius: 50%;
    background: var(--accent);
    animation: op-pulse 1.5s ease-in-out infinite;
  }
  @keyframes op-in { from { opacity: 0; transform: scale(0.9); } to { opacity: 1; transform: scale(1); } }
  @keyframes op-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }

  .srv-actions {
    display: flex; align-items: center; justify-content: space-between;
    padding: 14px 24px;
    border-top: 1px solid var(--border-dim);
    position: relative; z-index: 1;
  }
  .srv-actions-left { display: flex; gap: 6px; align-items: center; }
  .btn-action:disabled { opacity: 0.4; pointer-events: none; }

  .log-strip {
    display: block;
    padding: 8px 24px;
    border-top: 1px solid var(--border-dim);
    background: rgba(0, 0, 0, 0.15);
    text-decoration: none;
    position: relative; z-index: 1;
    transition: background 0.15s;
    overflow: hidden;
  }
  .log-strip:hover { background: rgba(0, 0, 0, 0.25); }
  .log-strip-line {
    font-family: var(--font-mono);
    font-size: 0.66rem;
    line-height: 1.55;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .srv-stats {
    display: flex; align-items: center; gap: 6px;
    font-size: 0.74rem; font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
  }
  .stat { color: var(--text-secondary); }
  .stat.players { color: var(--live); }
  .stat.mem { color: #8b5cf6; }
  .stat.cpu { color: var(--accent); }
  .stat-dim { color: var(--text-tertiary); font-weight: 400; }
  .stat-sep { color: var(--text-tertiary); opacity: 0.3; }
  .stat.uptime {
    color: var(--text-tertiary);
    display: flex; align-items: center; gap: 5px;
  }
  .stat.uptime svg { width: 13px; height: 13px; }

  .tabs {
    display: flex; gap: 2px;
    padding: 0;
    margin-top: 12px; margin-bottom: 18px;
    border-bottom: 1px solid var(--border-dim);
  }
  .tab {
    padding: 10px 16px;
    font-size: 0.84rem; font-weight: 450;
    color: var(--text-tertiary);
    text-decoration: none; cursor: pointer;
    border-bottom: 2px solid transparent;
    transition: color 0.15s, border-color 0.2s;
    margin-bottom: -1px;
  }
  .tab:hover { color: var(--text-secondary); }
  .tab.active {
    color: var(--accent);
    border-bottom-color: var(--accent);
  }

  .loading-text { color: var(--text-tertiary); font-size: 0.85rem; padding: 40px 0; text-align: center; }
  .error-text { text-align: center; padding: 60px 24px; color: var(--text-tertiary); }
  .error-text p { margin-bottom: 16px; }

  @media (max-width: 700px) {
    .srv-identity { flex-direction: column; align-items: flex-start; gap: 12px; }
    .srv-actions { flex-direction: column; gap: 10px; }
    .tabs { overflow-x: auto; }
  }
</style>
