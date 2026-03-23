<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type Gameserver, type GameserverStats, type QueryData, type Game } from '$lib/api';
  import { onEvent, toast } from '$lib/stores';
  import { HeroPanel } from '$lib/components';

  let gameservers = $state<Gameserver[]>([]);
  let games = $state<Record<string, Game>>({});
  let stats = $state<Record<string, GameserverStats | null>>({});
  let queries = $state<Record<string, QueryData | null>>({});
  let search = $state('');
  let loading = $state(true);

  // Game icon map — emoji fallbacks until we have real icons
  const gameIcons: Record<string, string> = {
    'minecraft-java': '⛏️', 'minecraft-bedrock': '⛏️',
    'rust': '🪓', 'counter-strike-2': '🔫', 'valheim': '⚔️',
    'ark-survival-evolved': '🏝️', '7-days-to-die': '🧟',
    'satisfactory': '🏭', 'terraria': '🌿', 'garrys-mod': '🎮',
    'palworld': '🎮',
  };

  const filtered = $derived(
    search
      ? gameservers.filter(gs => gs.name.toLowerCase().includes(search.toLowerCase()))
      : gameservers
  );

  const statusSummary = $derived(() => {
    const counts: Record<string, number> = {};
    for (const gs of gameservers) {
      const s = gs.status === 'started' || gs.status === 'installing' || gs.status === 'starting' || gs.status === 'stopping'
        ? gs.status : gs.status;
      counts[s] = (counts[s] || 0) + 1;
    }
    return counts;
  });

  let pollInterval: ReturnType<typeof setInterval>;
  let unsub: (() => void) | null = null;

  onMount(async () => {
    try {
      const [gs, gameList] = await Promise.all([
        api.gameservers.list(),
        api.games.list(),
      ]);
      gameservers = gs;
      for (const g of gameList) {
        games[g.id] = g;
      }

      // Initial stats + query fetch for running servers
      await refreshData();

      // Poll stats every 5s
      pollInterval = setInterval(refreshData, 5000);
    } catch (e: any) {
      toast(`Failed to load gameservers: ${e.message}`, 'error');
    } finally {
      loading = false;
    }

    // SSE: update gameserver status in real-time
    unsub = onEvent('status_changed', (data: any) => {
      gameservers = gameservers.map(gs =>
        gs.id === data.gameserver_id
          ? { ...gs, status: data.new_status, error_reason: data.error_reason || '' }
          : gs
      );
    });
  });

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
    if (unsub) unsub();
  });

  async function refreshData() {
    for (const gs of gameservers) {
      if (gs.status === 'running' || gs.status === 'started') {
        try {
          const [s, q] = await Promise.all([
            api.gameservers.stats(gs.id).catch(() => null),
            api.gameservers.query(gs.id).catch(() => null),
          ]);
          stats[gs.id] = s;
          queries[gs.id] = q;
        } catch { /* ignore */ }
      } else {
        stats[gs.id] = null;
        queries[gs.id] = null;
      }
    }
  }

  function connectionAddress(gs: Gameserver): string {
    try {
      const ports = typeof gs.ports === 'string' ? JSON.parse(gs.ports) : gs.ports;
      if (ports && ports.length > 0) {
        return `${ports[0].host_port || ports[0].container_port}`;
      }
    } catch {}
    return '';
  }

  async function handleAction(gsId: string, action: 'start' | 'stop' | 'restart') {
    try {
      const fn = api.gameservers[action];
      await fn(gsId);
    } catch (e: any) {
      toast(`Failed to ${action}: ${e.message}`, 'error');
    }
  }
</script>

<main>
  <div class="page-header">
    <h1>Gameservers</h1>
    <a href="/gameservers/new" class="btn-new">
      <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
      New
    </a>
  </div>

  {#if !loading && gameservers.length > 0}
    <div class="summary">
      <div class="summary-stats">
        <span class="count">{gameservers.length} server{gameservers.length !== 1 ? 's' : ''}</span>
        {#each Object.entries(statusSummary()) as [status, count]}
          <span class="sep">·</span>
          <span class={status}>{count} {status}</span>
        {/each}
      </div>
      {#if gameservers.length > 3}
        <div class="summary-search">
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.742 10.344a6.5 6.5 0 1 0-1.397 1.398h-.001c.03.04.062.078.098.115l3.85 3.85a1 1 0 0 0 1.415-1.414l-3.85-3.85a1.007 1.007 0 0 0-.115-.1zM12 6.5a5.5 5.5 0 1 1-11 0 5.5 5.5 0 0 1 11 0z"/></svg>
          <input type="text" placeholder="Search servers..." bind:value={search}>
        </div>
      {/if}
    </div>
  {/if}

  {#if loading}
    <p class="loading-text">Loading...</p>
  {:else if gameservers.length === 0}
    <div class="empty">
      <div class="empty-icon">
        <svg viewBox="0 0 24 24" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" stroke="currentColor" fill="none">
          <rect x="2" y="6" width="20" height="12" rx="2"/>
          <line x1="6" y1="10" x2="6" y2="10.01"/>
          <line x1="10" y1="10" x2="10" y2="10.01"/>
        </svg>
      </div>
      <h2>No gameservers yet</h2>
      <p>Create your first gameserver to get started. Pick a game, name it, and you're live in minutes.</p>
      <a href="/gameservers/new" class="btn-solid">
        <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
        Create Gameserver
      </a>
    </div>
  {:else}
    <div class="server-list">
      {#each filtered as gs (gs.id)}
        <HeroPanel
          gameserver={gs}
          stats={stats[gs.id] || null}
          query={queries[gs.id] || null}
          connectionAddress={connectionAddress(gs)}
          sftpAddress={`sftp://${gs.sftp_username}@localhost:2222`}
          gameIcon={gameIcons[gs.game_id] || '🎮'}
          gameName={games[gs.game_id]?.name || gs.game_id}
          onaction={(action) => handleAction(gs.id, action as any)}
        />
      {/each}
    </div>
  {/if}
</main>

<style>
  .summary {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 16px; gap: 16px;
  }
  .summary-stats {
    display: flex; align-items: center; gap: 6px;
    font-size: 0.78rem; font-family: var(--font-mono); color: var(--text-tertiary);
  }
  .summary-stats .count { color: var(--text-secondary); }
  .summary-stats .sep { opacity: 0.3; }
  .summary-stats .running { color: var(--live); }
  .summary-stats .stopped { color: var(--idle); }
  .summary-stats .error { color: var(--danger); }

  .summary-search { position: relative; max-width: 220px; width: 100%; }
  .summary-search input {
    width: 100%; padding: 7px 12px 7px 32px;
    border-radius: var(--radius-sm); background: var(--bg-inset);
    border: 1px solid var(--border-dim); color: var(--text-primary);
    font-family: var(--font-body); font-size: 0.82rem; outline: none;
    transition: border-color 0.2s;
  }
  .summary-search input::placeholder { color: var(--text-tertiary); opacity: 0.6; }
  .summary-search input:focus { border-color: var(--accent-border); }
  .summary-search svg {
    position: absolute; left: 10px; top: 50%; transform: translateY(-50%);
    width: 14px; height: 14px; color: var(--text-tertiary); opacity: 0.5;
    pointer-events: none;
  }

  .server-list { display: flex; flex-direction: column; gap: 14px; }

  .btn-new {
    display: inline-flex; align-items: center; gap: 5px;
    padding: 7px 15px; border-radius: var(--radius-sm);
    background: transparent; border: 1px solid var(--border-warm);
    color: var(--accent); font-family: var(--font-body);
    font-size: 0.82rem; font-weight: 520; cursor: pointer; text-decoration: none;
    transition: all 0.2s;
  }
  .btn-new:hover {
    background: var(--accent-dim);
    border-color: rgba(232,114,42,0.35);
  }
  .btn-new svg { width: 13px; height: 13px; }

  .loading-text { color: var(--text-tertiary); font-size: 0.85rem; padding: 40px 0; text-align: center; }

  .empty {
    display: flex; flex-direction: column; align-items: center;
    padding: 80px 24px; text-align: center;
  }
  .empty-icon {
    width: 56px; height: 56px; border-radius: 14px;
    background: var(--bg-elevated); border: 1px solid var(--border);
    display: grid; place-items: center; margin-bottom: 20px;
  }
  .empty-icon svg { width: 26px; height: 26px; color: var(--text-tertiary); }
  .empty h2 { font-size: 1.15rem; font-weight: 600; margin-bottom: 6px; }
  .empty p { font-size: 0.88rem; color: var(--text-tertiary); max-width: 340px; line-height: 1.5; margin-bottom: 24px; }
</style>
