<script lang="ts">
  import { onMount } from 'svelte';
  import { navigate, getRoute } from '$lib/router';
  import { api, type Game, type DynamicOption } from '$lib/api';
  import { toast } from '$lib/stores';
  import { GameIcon, GameserverForm } from '$lib/components';

  const popularIds = ['minecraft-java', 'rust', 'counter-strike-2', 'valheim'];

  let games = $state<Game[]>([]);
  let loading = $state(true);
  let search = $state('');
  let step = $state<'pick' | 'configure'>('pick');

  let selectedGame = $state<Game | null>(null);

  // Form state — bound to GameserverForm
  let serverName = $state('');
  let memoryMb = $state(2048);
  let cpuLimit = $state(0);
  let cpuEnforced = $state(false);
  let storageLimitMb = $state(0);
  let backupLimit = $state(0);
  let portMode = $state('auto');
  let manualPorts = $state<{ name: string; host_port: number; instance_port: number; protocol: string }[]>([]);
  let autoRestart = $state(true);
  let autoStart = $state(true);
  let envValues = $state<Record<string, string>>({});
  let dynamicOptions = $state<Record<string, DynamicOption[]>>({});
  let submitting = $state(false);
  let globalMaxBackups = $state(10);


  const popularGames = $derived(games.filter(g => popularIds.includes(g.id)));
  const filteredGames = $derived(
    search ? games.filter(g => g.name.toLowerCase().includes(search.toLowerCase())) : games
  );

  onMount(async () => {
    try {
      const [gameList, settings] = await Promise.all([
        api.games.list(),
        api.settings.get().catch((e) => { console.warn('NewGameserver: failed to load settings', e); return null; }),
      ]);
      games = gameList;
      if (settings?.max_backups) globalMaxBackups = settings.max_backups;

      // Auto-select game from URL: /gameservers/new/minecraft
      const route = getRoute();
      if (route.params.game) {
        const match = gameList.find(g =>
          g.id === route.params.game || g.aliases?.includes(route.params.game)
        );
        if (match) pickGame(match);
      }
    } catch (e: any) {
      toast(`Failed to load games: ${e.message}`, 'error');
    } finally {
      loading = false;
    }
  });

  function pickGame(game: Game) {
    selectedGame = game;
    memoryMb = game.recommended_memory_mb;
    serverName = '';
    cpuLimit = 0;
    cpuEnforced = false;
    storageLimitMb = 0;
    backupLimit = 0;
    portMode = 'auto';

    manualPorts = game.default_ports.map(p => ({
      name: p.name,
      host_port: p.port,
      instance_port: p.port,
      protocol: p.protocol,
    }));

    envValues = {};
    for (const env of game.default_env) {
      if (!env.system) {
        envValues[env.key] = env.default;
      }
    }

    for (const env of game.default_env) {
      if (env.dynamic_options) {
        loadDynamicOptions(game.id, env.key);
      }
    }

    step = 'configure';
    // Update URL so the game selection is linkable and survives refresh
    window.history.replaceState(null, '', `/gameservers/new/${game.id}`);
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  function goBack() {
    step = 'pick';
    window.history.replaceState(null, '', '/gameservers/new');
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  async function loadDynamicOptions(gameId: string, key: string) {
    try {
      const opts = await api.games.options(gameId, key);
      dynamicOptions[key] = opts;
    } catch (e) {
      console.warn('NewGameserver: failed to load dynamic options for', key, e);
    }
  }

  async function createServer() {
    if (!selectedGame || !serverName.trim()) return;
    submitting = true;

    try {
      const env: Record<string, string> = {};
      for (const [key, val] of Object.entries(envValues)) {
        env[key] = val;
      }

      const payload: any = {
        name: serverName.trim(),
        game_id: selectedGame.id,
        memory_limit_mb: memoryMb,
        auto_restart: autoRestart,
        port_mode: portMode,
        env,
      };

      if (cpuLimit > 0) {
        payload.cpu_limit = cpuLimit;
        payload.cpu_enforced = cpuEnforced;
      }
      if (storageLimitMb > 0) payload.storage_limit_mb = storageLimitMb;
      if (backupLimit > 0) payload.backup_limit = backupLimit;
      if (portMode === 'manual') payload.ports = manualPorts;

      const result = await api.gameservers.create(payload);

      if (autoStart) {
        api.gameservers.start(result.id).catch((e) => { console.warn('NewGameserver: failed to auto-start', e); });
      }

      navigate(`/gameservers/${result.id}`);
    } catch (e: any) {
      toast(`Failed to create server: ${e.message}`, 'error');
    } finally {
      submitting = false;
    }
  }

</script>

<main>
  <a href="/" class="breadcrumb">
    <svg viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M11.354 1.646a.5.5 0 0 1 0 .708L5.707 8l5.647 5.646a.5.5 0 0 1-.708.708l-6-6a.5.5 0 0 1 0-.708l6-6a.5.5 0 0 1 .708 0z"/></svg>
    Gameservers
  </a>

  <div class="page-header">
    <h1>New Gameserver</h1>
  </div>

  {#if loading}
    <p class="loading-text">Loading games...</p>

  {:else if step === 'pick'}
    <!-- ═══════════ STEP 1: PICK GAME ═══════════ -->

    {#if popularGames.length > 0}
      <div class="section-label">Popular</div>
      <div class="featured-grid">
        {#each popularGames as game}
          <button class="featured-card" onclick={() => pickGame(game)}>
            <GameIcon src={game.icon_path} name={game.name} size={48} />
            <div class="featured-name">{game.name}</div>
            <div class="featured-desc">{game.description || ''}</div>
          </button>
        {/each}
      </div>
    {/if}

    <div class="all-header">
      <div class="section-label">All Games</div>
      <div class="all-search">
        <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.742 10.344a6.5 6.5 0 1 0-1.397 1.398h-.001c.03.04.062.078.098.115l3.85 3.85a1 1 0 0 0 1.415-1.414l-3.85-3.85a1.007 1.007 0 0 0-.115-.1zM12 6.5a5.5 5.5 0 1 1-11 0 5.5 5.5 0 0 1 11 0z"/></svg>
        <input type="text" placeholder="Search games..." bind:value={search}>
      </div>
    </div>

    <div class="game-list">
      {#each filteredGames as game}
        <button class="game-row" onclick={() => pickGame(game)}>
          <GameIcon src={game.icon_path} name={game.name} size={28} />
          <div class="game-row-info">
            <div class="game-row-name">{game.name}</div>
            <div class="game-row-desc">{game.description || ''}</div>
          </div>
          <svg class="game-row-arrow" viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M4.646 1.646a.5.5 0 0 1 .708 0l6 6a.5.5 0 0 1 0 .708l-6 6a.5.5 0 0 1-.708-.708L10.293 8 4.646 2.354a.5.5 0 0 1 0-.708z"/></svg>
        </button>
      {/each}
      {#if filteredGames.length === 0}
        <div class="no-results">No games match your search.</div>
      {/if}
    </div>

  {:else if step === 'configure' && selectedGame}
    <!-- ═══════════ STEP 2: CONFIGURE ═══════════ -->

    <div class="selected-game">
      <GameIcon src={selectedGame.icon_path} name={selectedGame.name} size={42} />
      <div class="selected-info">
        <div class="selected-name">{selectedGame.name}</div>
        <div class="selected-meta">Recommended memory: {selectedGame.recommended_memory_mb >= 1024 ? `${selectedGame.recommended_memory_mb / 1024} GB` : `${selectedGame.recommended_memory_mb} MB`}</div>
      </div>
      <button class="change-link" onclick={goBack}>Change</button>
    </div>

    <div class="config-panel">
      <GameserverForm
        game={selectedGame}
        bind:serverName={serverName}
        bind:memoryMb={memoryMb}
        bind:storageLimitMb={storageLimitMb}
        bind:cpuLimit={cpuLimit}
        bind:cpuEnforced={cpuEnforced}
        bind:backupLimit={backupLimit}
        bind:portMode={portMode}
        bind:manualPorts={manualPorts}
        bind:autoRestart={autoRestart}
        bind:envValues={envValues}
        {dynamicOptions}
        {globalMaxBackups}
      />

      <!-- Submit -->
      <div class="submit-row">
        <div class="submit-left">
          <div class="submit-toggle">
            <button class="toggle" class:on={autoStart} onclick={() => autoStart = !autoStart}></button>
            <span class="submit-toggle-label">Start now</span>
          </div>
        </div>
        <button class="btn-solid" disabled={!serverName.trim() || submitting} onclick={createServer}>
          <GameIcon src={selectedGame.icon_path} name={selectedGame.name} size={18} />
          {submitting ? 'Creating...' : `Create ${selectedGame.name.split(':')[0]} Server`}
        </button>
      </div>
    </div>
  {/if}

</main>

<style>
  .breadcrumb { display: inline-flex; align-items: center; gap: 6px; font-size: 0.84rem; color: var(--text-tertiary); text-decoration: none; margin-bottom: 20px; transition: color 0.15s; }
  .breadcrumb:hover { color: var(--accent); }
  .breadcrumb svg { width: 16px; height: 16px; }
  .loading-text { color: var(--text-tertiary); font-size: 0.85rem; padding: 40px 0; text-align: center; }

  .section-label { font-size: 0.68rem; font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.1em; color: var(--text-tertiary); margin-bottom: 10px; }

  /* Featured cards */
  .featured-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin-bottom: 28px; }
  .featured-card { background: var(--bg-surface); border: 1px solid var(--border-dim); border-radius: var(--radius); padding: 18px 16px; cursor: pointer; transition: border-color 0.2s, background 0.2s, box-shadow 0.25s, transform 0.15s; display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center; font-family: var(--font-body); color: var(--text-primary); }
  .featured-card:hover { border-color: var(--accent-border); background: var(--bg-elevated); box-shadow: 0 0 24px rgba(232,114,42,0.05); transform: translateY(-2px); }
  .featured-name { font-weight: 550; font-size: 0.85rem; }
  .featured-desc { font-size: 0.72rem; color: var(--text-tertiary); line-height: 1.35; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }

  /* All games list */
  .all-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; gap: 16px; }
  .all-search { position: relative; max-width: 240px; width: 100%; }
  .all-search input { width: 100%; padding: 7px 12px 7px 32px; border-radius: var(--radius-sm); background: var(--bg-inset); border: 1px solid var(--border-dim); color: var(--text-primary); font-family: var(--font-body); font-size: 0.82rem; outline: none; transition: border-color 0.2s; }
  .all-search input::placeholder { color: var(--text-tertiary); opacity: 0.5; }
  .all-search input:focus { border-color: var(--accent-border); }
  .all-search svg { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); width: 14px; height: 14px; color: var(--text-tertiary); opacity: 0.4; pointer-events: none; }

  .game-list { background: var(--bg-surface); border: 1px solid var(--border-dim); border-radius: var(--radius); overflow: hidden; }
  .game-row { display: flex; align-items: center; padding: 11px 16px; cursor: pointer; transition: background 0.12s; gap: 12px; border-left: 2px solid transparent; width: 100%; background: none; border-top: none; border-right: none; border-bottom: none; font-family: var(--font-body); color: var(--text-primary); text-align: left; }
  .game-row:hover { background: var(--bg-elevated); border-left-color: var(--accent); }
  .game-row + .game-row { border-top: 1px solid var(--border-dim); }
  .game-row-info { flex: 1; min-width: 0; }
  .game-row-name { font-size: 0.85rem; font-weight: 500; }
  .game-row-desc { font-size: 0.73rem; color: var(--text-tertiary); margin-top: 1px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .game-row-arrow { width: 14px; height: 14px; color: var(--text-tertiary); opacity: 0.3; flex-shrink: 0; transition: opacity 0.15s, color 0.15s; }
  .game-row:hover .game-row-arrow { opacity: 0.7; color: var(--accent); }
  .no-results { padding: 24px 16px; text-align: center; font-size: 0.84rem; color: var(--text-tertiary); }

  /* Step 2: Configure */
  .selected-game { display: flex; align-items: center; gap: 14px; margin-bottom: 22px; padding: 14px 18px; background: var(--bg-surface); border: 1px solid var(--accent-border); border-radius: var(--radius); position: relative; overflow: hidden; }
  .selected-game::before { content: ''; position: absolute; top: 0; left: 10%; right: 10%; height: 1px; background: linear-gradient(90deg, transparent, var(--accent), transparent); opacity: 0.3; }
  .selected-info { flex: 1; }
  .selected-name { font-weight: 600; font-size: 1rem; }
  .selected-meta { font-size: 0.76rem; color: var(--text-tertiary); margin-top: 2px; }
  .change-link { font-size: 0.78rem; color: var(--accent); background: none; border: none; font-weight: 500; padding: 5px 10px; border-radius: var(--radius-sm); cursor: pointer; transition: background 0.15s; }
  .change-link:hover { background: var(--accent-dim); }

  .config-panel { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius); padding: 24px; position: relative; overflow: hidden; }
  .config-panel::before { content: ''; position: absolute; inset: 0; background: radial-gradient(ellipse 80% 50% at 50% 0%, rgba(232,114,42,0.02) 0%, transparent 60%); pointer-events: none; }

  .submit-row { display: flex; align-items: center; justify-content: space-between; margin-top: 28px; padding-top: 20px; border-top: 1px solid var(--border-dim); position: relative; z-index: 1; }
  .submit-left { display: flex; align-items: center; gap: 16px; }
  .submit-toggle { display: flex; align-items: center; gap: 6px; }
  .submit-toggle-label { font-size: 0.78rem; color: var(--text-tertiary); }
  .submit-row .btn-solid:disabled { opacity: 0.5; cursor: not-allowed; }

  @media (max-width: 700px) { .featured-grid { grid-template-columns: repeat(2, 1fr); } }
  @media (max-width: 620px) { .config-panel { padding: 18px; } .all-header { flex-direction: column; align-items: flex-start; } .all-search { max-width: 100%; } }
</style>
