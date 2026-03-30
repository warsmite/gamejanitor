<script lang="ts">
  import { gameserverStore, toast } from '$lib/stores';
  import { api } from '$lib/api';
  import { HeroPanel } from '$lib/components';

  const can = (p: string) => gameserverStore.can(p);
  let search = $state('');

  const gameservers = $derived(gameserverStore.list);
  const loading = $derived(gameserverStore.loading);
  const cluster = $derived(gameserverStore.cluster);

  const filtered = $derived(
    search
      ? gameservers.filter(gs => gs.name.toLowerCase().includes(search.toLowerCase()))
      : gameservers
  );

  const statusSummary = $derived(() => {
    const counts: Record<string, number> = {};
    for (const gs of gameservers) {
      counts[gs.status] = (counts[gs.status] || 0) + 1;
    }
    return counts;
  });

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
    {#if can('gameserver.create')}
      <a href="/gameservers/new" class="btn-new">
        <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
        New
      </a>
    {/if}
  </div>

  {#if !loading && gameservers.length > 0}
    <div class="summary">
      <div class="summary-stats">
        <span class="count">{gameservers.length} server{gameservers.length !== 1 ? 's' : ''}</span>
        {#each Object.entries(statusSummary()) as [status, count]}
          <span class="sep">·</span>
          <span class={status}>{count} {status}</span>
        {/each}
        {#if cluster && cluster.total_memory_mb > 0}
          <span class="sep capacity-label">— Allocated:</span>
          <span class="capacity mem">{(cluster.allocated_memory_mb / 1024).toFixed(1)}<span class="cap-dim">/{(cluster.total_memory_mb / 1024).toFixed(0)} GB mem</span></span>
          <span class="sep">·</span>
          <span class="capacity cpu">{cluster.allocated_cpu.toFixed(1)}<span class="cap-dim">/{cluster.total_cpu.toFixed(0)} CPU</span></span>
          {#if cluster.total_storage_mb > 0}
            <span class="sep">·</span>
            <span class="capacity">{(cluster.allocated_storage_mb / 1024).toFixed(0)}<span class="cap-dim">/{(cluster.total_storage_mb / 1024).toFixed(0)} GB disk</span></span>
          {/if}
        {/if}
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
      {#if can('gameserver.create')}
        <a href="/gameservers/new" class="btn-solid">
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
          Create Gameserver
        </a>
      {/if}
    </div>
  {:else}
    <div class="server-list">
      {#each filtered as gs (gs.id)}
        {@const state = gameserverStore.getState(gs.id)}
        {@const game = gameserverStore.gameFor(gs.game_id)}
        <HeroPanel
          gameserver={gs}
          stats={state?.stats ?? null}
          query={state?.query ?? null}
          connectionAddress={gameserverStore.connectionAddress(gs.id)}
          iconPath={game?.icon_path || ''}
          gameName={game?.name || gs.game_id}
          logLines={state?.logLines?.slice(-4) ?? []}
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
  .summary-stats .capacity-label { opacity: 0.5; margin-left: 2px; }
  .summary-stats .capacity { color: var(--text-secondary); }
  .summary-stats .capacity.mem { color: #8b5cf6; }
  .summary-stats .capacity.cpu { color: var(--accent); }
  .summary-stats .cap-dim { color: var(--text-tertiary); opacity: 0.6; }

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
