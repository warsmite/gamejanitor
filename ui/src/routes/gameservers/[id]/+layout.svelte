<script lang="ts">
  import { page } from '$app/stores';
  import { onMount, onDestroy } from 'svelte';
  import { api, type Gameserver, type Game, type GameserverStatus } from '$lib/api';
  import { onEvent, onGameserverEvent, toast } from '$lib/stores';
  import { GameIcon, StatusPill } from '$lib/components';

  let { children } = $props();

  const gsId = $derived($page.params.id as string);
  const currentPath = $derived($page.url.pathname);

  let gameserver = $state<Gameserver | null>(null);
  let game = $state<Game | null>(null);
  let loading = $state(true);
  let error = $state('');
  let uptime = $state('');
  let containerStartedAt = $state('');


  const isRunning = $derived(gameserver?.status === 'running' || gameserver?.status === 'started');
  const isStopped = $derived(gameserver?.status === 'stopped');
  const isTransitioning = $derived(
    gameserver?.status === 'starting' || gameserver?.status === 'installing' ||
    gameserver?.status === 'stopping' || gameserver?.status === 'reinstalling' ||
    gameserver?.status === 'migrating' || gameserver?.status === 'restoring' ||
    gameserver?.status === 'updating'
  );

  const tabs = [
    { label: 'Overview', path: '' },
    { label: 'Console', path: '/console' },
    { label: 'Files', path: '/files' },
    { label: 'Backups', path: '/backups' },
    { label: 'Schedules', path: '/schedules' },
    { label: 'Settings', path: '/settings' },
  ];

  function tabHref(tab: typeof tabs[0]) {
    return `/gameservers/${gsId}${tab.path}`;
  }

  function isActiveTab(tab: typeof tabs[0]) {
    const href = tabHref(tab);
    if (tab.path === '') {
      return currentPath === `/gameservers/${gsId}` || currentPath === `/gameservers/${gsId}/`;
    }
    return currentPath.startsWith(href);
  }

  let statusUnsub: (() => void) | null = null;
  let gsUnsub: (() => void) | null = null;
  let uptimeInterval: ReturnType<typeof setInterval>;

  onMount(async () => {
    try {
      const [gs, statusData] = await Promise.all([
        api.gameservers.get(gsId),
        api.gameservers.status(gsId).catch(() => null),
      ]);
      gameserver = gs;

      if (statusData?.container?.started_at) {
        containerStartedAt = statusData.container.started_at;
      }

      try {
        game = await api.games.get(gs.game_id);
      } catch {
        // game definition not found — non-fatal
      }
    } catch (e: any) {
      error = e.message || 'Failed to load gameserver';
    } finally {
      loading = false;
    }

    // SSE: real-time status updates
    statusUnsub = onEvent('status_changed', (data: any) => {
      if (data.gameserver_id === gsId && gameserver) {
        gameserver = { ...gameserver, status: data.new_status, error_reason: data.error_reason || '' };
      }
    });

    // SSE: update container started time on start events
    gsUnsub = onGameserverEvent(gsId, (data: any) => {
      if (data.type === 'gameserver.container_started' || data.type === 'gameserver.ready') {
        containerStartedAt = new Date().toISOString();
      }
      if (data.type === 'gameserver.container_stopped' || data.type === 'gameserver.container_exited') {
        containerStartedAt = '';
      }
    });

    // Uptime ticker
    updateUptime();
    uptimeInterval = setInterval(updateUptime, 1000);
  });

  onDestroy(() => {
    statusUnsub?.();
    gsUnsub?.();
    if (uptimeInterval) clearInterval(uptimeInterval);
  });

  function updateUptime() {
    if (!containerStartedAt) {
      uptime = '';
      return;
    }
    const started = new Date(containerStartedAt).getTime();
    const now = Date.now();
    const diff = Math.max(0, Math.floor((now - started) / 1000));

    const days = Math.floor(diff / 86400);
    const hours = Math.floor((diff % 86400) / 3600);
    const mins = Math.floor((diff % 3600) / 60);

    if (days > 0) {
      uptime = `Up ${days}d ${hours}h`;
    } else if (hours > 0) {
      uptime = `Up ${hours}h ${mins}m`;
    } else {
      uptime = `Up ${mins}m`;
    }
  }

  async function handleAction(action: string) {
    if (!gameserver) return;
    try {
      if (action === 'start') await api.gameservers.start(gsId);
      else if (action === 'stop') await api.gameservers.stop(gsId);
      else if (action === 'restart') await api.gameservers.restart(gsId);
    } catch (e: any) {
      toast(`Failed to ${action}: ${e.message}`, 'error');
    }
  }

</script>

<main>
  <a href="/" class="breadcrumb">
    <svg viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M11.354 1.646a.5.5 0 0 1 0 .708L5.707 8l5.647 5.646a.5.5 0 0 1-.708.708l-6-6a.5.5 0 0 1 0-.708l6-6a.5.5 0 0 1 .708 0z"/></svg>
    Gameservers
  </a>

  {#if loading}
    <p class="loading-text">Loading...</p>
  {:else if error}
    <div class="error-text">
      <p>{error}</p>
      <a href="/" class="btn-accent">Back to Dashboard</a>
    </div>
  {:else if gameserver}
    <!-- Server Header -->
    <div class="srv-header" class:running={isRunning} class:stopped={isStopped}>
      <div class="srv-identity">
        <div class="srv-id-left">
          <GameIcon src={game?.icon_path || ''} name={game?.name || gameserver.game_id} size={50} />
          <div>
            <div class="srv-name">{gameserver.name}</div>
            <div class="srv-game">{game?.name || gameserver.game_id}</div>
          </div>
        </div>
        <div class="srv-id-right">
          <StatusPill status={gameserver.status} />
        </div>
      </div>

      <div class="srv-actions">
        <div class="srv-actions-left">
          {#if isStopped}
            <button class="btn-action start" onclick={() => handleAction('start')} disabled={isTransitioning}>
              <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.596 8.697l-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z"/></svg>
              Start
            </button>
          {:else}
            <button class="btn-action stop" onclick={() => handleAction('stop')} disabled={isTransitioning}>
              <svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
              Stop
            </button>
            <button class="btn-action restart" onclick={() => handleAction('restart')} disabled={isTransitioning}>
              <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
              Restart
            </button>
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

  /* ═══════════ SERVER HEADER ═══════════ */
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

  /* Status line at top */
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
  .srv-game { font-size: 0.8rem; color: var(--text-tertiary); margin-top: 2px; }
  .srv-id-right { display: flex; align-items: center; gap: 10px; }

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

  /* ═══════════ TAB BAR ═══════════ */
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
