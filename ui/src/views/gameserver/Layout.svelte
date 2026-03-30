<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/api';
  import { gameserverStore, formatUptime, phaseLabels, toast } from '$lib/stores';
  import { GameIcon, StatusPill } from '$lib/components';
  import { getRoute, navigate, isActive } from '$lib/router';
  import { embedded } from '$lib/base';

  const connAddr = $derived(gameserverStore.connectionAddress(id));

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

  // Mods tab visibility: fetch config to check if game supports mods
  let hasModsSupport = $state(false);
  $effect(() => {
    if (!gameserver) return;
    api.mods.config(id).then(cfg => {
      hasModsSupport = (cfg?.categories?.length ?? 0) > 0 || !!cfg?.loader || !!cfg?.version;
    }).catch(() => { hasModsSupport = false; });
  });

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
                <span class="srv-addr">{connAddr}</span>
              {/if}
            </div>
          </div>
        </div>
        <div class="srv-id-right">
          {#if !embedded && gameserver.node_id}
            <span class="node-badge">{gameserver.node_id}</span>
          {/if}
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
              <button class="btn-action stop" onclick={() => handleAction('stop')} disabled={isTransitioning() || !!operation}>
                <svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
                Stop
              </button>
            {/if}
            {#if can('gameserver.restart')}
              <button class="btn-action restart" onclick={() => handleAction('restart')} disabled={isTransitioning() || !!operation}>
                <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
                Restart
              </button>
            {/if}
          {/if}
        </div>

        {#if uptime}
          <div class="uptime">
            <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 3.5a.5.5 0 0 0-1 0V8a.5.5 0 0 0 .252.434l3.5 2a.5.5 0 0 0 .496-.868L8 7.71V3.5z"/><path d="M8 16A8 8 0 1 0 8 0a8 8 0 0 0 0 16zm7-8A7 7 0 1 1 1 8a7 7 0 0 1 14 0z"/></svg>
            {uptime}
          </div>
        {/if}
      </div>
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
  .srv-addr { font-size: 0.78rem; font-family: var(--font-mono); color: var(--text-tertiary); }
  .srv-id-right { display: flex; align-items: center; gap: 8px; }
  .node-badge {
    font-size: 0.66rem; font-family: var(--font-mono);
    color: var(--text-tertiary); opacity: 0.7;
    padding: 3px 8px; border-radius: 4px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }

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

  .uptime {
    font-size: 0.74rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
    display: flex; align-items: center; gap: 5px;
  }
  .uptime svg { width: 13px; height: 13px; }

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
