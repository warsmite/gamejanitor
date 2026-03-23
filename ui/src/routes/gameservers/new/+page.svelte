<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, type Game, type EnvVar, type DynamicOption } from '$lib/api';
  import { toast } from '$lib/stores';
  import { GameIcon } from '$lib/components';

  // Popular game IDs for featured section
  const popularIds = ['minecraft-java', 'rust', 'counter-strike-2', 'valheim'];

  let games = $state<Game[]>([]);
  let loading = $state(true);
  let search = $state('');
  let step = $state<'pick' | 'configure'>('pick');

  // Selected game
  let selectedGame = $state<Game | null>(null);

  // Form state
  let serverName = $state('');
  let memoryMb = $state(2048);
  let envValues = $state<Record<string, string>>({});
  let dynamicOptions = $state<Record<string, DynamicOption[]>>({});
  let submitting = $state(false);

  const popularGames = $derived(games.filter(g => popularIds.includes(g.id)));
  const filteredGames = $derived(
    search ? games.filter(g => g.name.toLowerCase().includes(search.toLowerCase())) : games
  );

  const memoryDisplay = $derived(memoryMb >= 1024 ? `${memoryMb / 1024} GB` : `${memoryMb} MB`);
  const belowRecommended = $derived(selectedGame ? memoryMb < selectedGame.recommended_memory_mb : false);

  // Group env vars by their group field
  const envGroups = $derived(() => {
    if (!selectedGame) return [];
    const userEnvs = selectedGame.default_env.filter(e => !e.system);
    const groups: { name: string; vars: EnvVar[] }[] = [];
    const grouped = new Map<string, EnvVar[]>();
    const ungrouped: EnvVar[] = [];

    for (const env of userEnvs) {
      if (env.group) {
        if (!grouped.has(env.group)) grouped.set(env.group, []);
        grouped.get(env.group)!.push(env);
      } else {
        ungrouped.push(env);
      }
    }

    // Required fields first (outside groups)
    const required = ungrouped.filter(e => e.required);
    const optional = ungrouped.filter(e => !e.required);

    if (required.length > 0) groups.push({ name: '', vars: required });
    for (const [name, vars] of grouped) groups.push({ name, vars });
    if (optional.length > 0) groups.push({ name: '', vars: [...optional, ...groups.length === 0 ? [] : []] });

    return groups;
  });

  onMount(async () => {
    try {
      games = await api.games.list();
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

    // Initialize env values from defaults
    envValues = {};
    for (const env of game.default_env) {
      if (!env.system) {
        envValues[env.key] = env.default;
      }
    }

    // Load dynamic options for any env vars that need them
    for (const env of game.default_env) {
      if (env.dynamic_options) {
        loadDynamicOptions(game.id, env.key);
      }
    }

    step = 'configure';
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  function goBack() {
    step = 'pick';
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  async function loadDynamicOptions(gameId: string, key: string) {
    try {
      const opts = await api.games.options(gameId, key);
      dynamicOptions[key] = opts;
    } catch {
      // Silently fail — the select will just show the default
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

      const gs = await api.gameservers.create({
        name: serverName.trim(),
        game_id: selectedGame.id,
        memory_limit_mb: memoryMb,
        env,
      });

      // Auto-start the server
      await api.gameservers.start(gs.id);

      toast('Gameserver created', 'success');
      goto(`/gameservers/${gs.id}`);
    } catch (e: any) {
      toast(`Failed to create server: ${e.message}`, 'error');
    } finally {
      submitting = false;
    }
  }

  function updateSliderFill(e: Event) {
    const input = e.target as HTMLInputElement;
    const pct = ((parseInt(input.value) - parseInt(input.min)) / (parseInt(input.max) - parseInt(input.min))) * 100;
    input.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--border-dim) ${pct}%, var(--border-dim) 100%)`;
    memoryMb = parseInt(input.value);
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
      <!-- Server name -->
      <div class="form-row">
        <label class="label label-required">Server Name</label>
        <input class="input" type="text" placeholder="e.g. survival-smp" bind:value={serverName}>
      </div>

      <!-- Memory slider -->
      <div class="form-row">
        <div class="resource-header">
          <span class="label">Memory</span>
          <span class="resource-value">{memoryDisplay}</span>
        </div>
        <input type="range" class="slider" min="256" max="16384" step="256"
          value={memoryMb}
          oninput={updateSliderFill}
        >
        {#if belowRecommended}
          <div class="resource-warning">
            <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8.982 1.566a1.13 1.13 0 0 0-1.96 0L.165 13.233c-.457.778.091 1.767.98 1.767h13.713c.889 0 1.438-.99.98-1.767L8.982 1.566zM8 5c.535 0 .954.462.9.995l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 5.995A.905.905 0 0 1 8 5zm.002 6a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>
            Below recommended ({selectedGame.recommended_memory_mb >= 1024 ? `${selectedGame.recommended_memory_mb / 1024} GB` : `${selectedGame.recommended_memory_mb} MB`})
          </div>
        {/if}
      </div>

      <hr class="form-divider">

      <!-- Game-specific env vars -->
      {#each envGroups() as group}
        {#if group.vars.some(v => v.required && v.notice)}
          <!-- Special callout for required fields with notices (e.g. EULA) -->
          {#each group.vars.filter(v => v.required && v.notice) as env}
            <div class="eula-callout">
              <div class="eula-left">
                <div class="eula-title">{env.label || env.key}</div>
                <div class="eula-notice">{@html env.notice?.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank">$1</a>') || ''}</div>
                <div class="eula-required">Required</div>
              </div>
              <button class="toggle" class:on={envValues[env.key] === 'true'} onclick={() => envValues[env.key] = envValues[env.key] === 'true' ? 'false' : 'true'}></button>
            </div>
          {/each}
        {/if}

        {#if group.name || group.vars.some(v => !(v.required && v.notice))}
          <div class="env-group">
            {#if group.name}
              <div class="env-group-label">{group.name}</div>
            {/if}
            <div class="form-grid">
              {#each group.vars.filter(v => !(v.required && v.notice)) as env}
                <div class="form-row">
                  <label class="label" class:label-required={env.required}>{env.label || env.key}</label>
                  {#if env.type === 'boolean'}
                    <div style="display:flex; align-items:center; gap:8px;">
                      <button class="toggle" class:on={envValues[env.key] === 'true'} onclick={() => envValues[env.key] = envValues[env.key] === 'true' ? 'false' : 'true'}></button>
                      <span style="font-size:0.78rem; color:var(--text-tertiary);">{envValues[env.key] === 'true' ? 'Enabled' : 'Disabled'}</span>
                    </div>
                  {:else if env.type === 'select' || env.options}
                    <select class="select" bind:value={envValues[env.key]}>
                      {#if dynamicOptions[env.key]}
                        {#each dynamicOptions[env.key] as opt}
                          <option value={opt.value}>{opt.label}</option>
                        {/each}
                      {:else if env.options}
                        {#each env.options as opt}
                          <option value={opt}>{opt}</option>
                        {/each}
                      {/if}
                    </select>
                  {:else if env.type === 'number'}
                    <input class="input input-mono" type="number" bind:value={envValues[env.key]}>
                  {:else}
                    <input class="input" type="text" bind:value={envValues[env.key]}>
                  {/if}
                </div>
              {/each}
            </div>
          </div>
        {/if}
      {/each}

      <!-- Submit -->
      <div class="submit-row">
        <button class="btn-solid" disabled={!serverName.trim() || submitting} onclick={createServer}>
          <GameIcon src={selectedGame.icon_path} name={selectedGame.name} size={18} />
          {submitting ? 'Creating...' : `Create ${selectedGame.name.split(':')[0]} Server`}
        </button>
      </div>
    </div>
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

  .loading-text { color: var(--text-tertiary); font-size: 0.85rem; padding: 40px 0; text-align: center; }

  .section-label {
    font-size: 0.68rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary); margin-bottom: 10px;
  }

  /* Featured cards */
  .featured-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin-bottom: 28px; }
  .featured-card {
    background: var(--bg-surface); border: 1px solid var(--border-dim);
    border-radius: var(--radius); padding: 18px 16px; cursor: pointer;
    transition: border-color 0.2s, background 0.2s, box-shadow 0.25s, transform 0.15s;
    display: flex; flex-direction: column; align-items: center;
    gap: 10px; text-align: center; font-family: var(--font-body); color: var(--text-primary);
  }
  .featured-card:hover { border-color: var(--accent-border); background: var(--bg-elevated); box-shadow: 0 0 24px rgba(232,114,42,0.05); transform: translateY(-2px); }
  .featured-name { font-weight: 550; font-size: 0.85rem; }
  .featured-desc { font-size: 0.72rem; color: var(--text-tertiary); line-height: 1.35; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }

  /* All games list */
  .all-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; gap: 16px; }
  .all-search { position: relative; max-width: 240px; width: 100%; }
  .all-search input {
    width: 100%; padding: 7px 12px 7px 32px; border-radius: var(--radius-sm);
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    color: var(--text-primary); font-family: var(--font-body); font-size: 0.82rem;
    outline: none; transition: border-color 0.2s;
  }
  .all-search input::placeholder { color: var(--text-tertiary); opacity: 0.5; }
  .all-search input:focus { border-color: var(--accent-border); }
  .all-search svg { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); width: 14px; height: 14px; color: var(--text-tertiary); opacity: 0.4; pointer-events: none; }

  .game-list { background: var(--bg-surface); border: 1px solid var(--border-dim); border-radius: var(--radius); overflow: hidden; }
  .game-row {
    display: flex; align-items: center; padding: 11px 16px; cursor: pointer;
    transition: background 0.12s; gap: 12px; border-left: 2px solid transparent;
    width: 100%; background: none; border-top: none; border-right: none; border-bottom: none;
    font-family: var(--font-body); color: var(--text-primary); text-align: left;
  }
  .game-row:hover { background: var(--bg-elevated); border-left-color: var(--accent); }
  .game-row + .game-row { border-top: 1px solid var(--border-dim); }
  .game-row-info { flex: 1; min-width: 0; }
  .game-row-name { font-size: 0.85rem; font-weight: 500; }
  .game-row-desc { font-size: 0.73rem; color: var(--text-tertiary); margin-top: 1px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .game-row-arrow { width: 14px; height: 14px; color: var(--text-tertiary); opacity: 0.3; flex-shrink: 0; transition: opacity 0.15s, color 0.15s; }
  .game-row:hover .game-row-arrow { opacity: 0.7; color: var(--accent); }
  .no-results { padding: 24px 16px; text-align: center; font-size: 0.84rem; color: var(--text-tertiary); }

  /* Step 2: Configure */
  .selected-game {
    display: flex; align-items: center; gap: 14px;
    margin-bottom: 22px; padding: 14px 18px;
    background: var(--bg-surface); border: 1px solid var(--accent-border);
    border-radius: var(--radius); position: relative; overflow: hidden;
  }
  .selected-game::before { content: ''; position: absolute; top: 0; left: 10%; right: 10%; height: 1px; background: linear-gradient(90deg, transparent, var(--accent), transparent); opacity: 0.3; }
  .selected-info { flex: 1; }
  .selected-name { font-weight: 600; font-size: 1rem; }
  .selected-meta { font-size: 0.76rem; color: var(--text-tertiary); margin-top: 2px; }
  .change-link { font-size: 0.78rem; color: var(--accent); background: none; border: none; font-weight: 500; padding: 5px 10px; border-radius: var(--radius-sm); cursor: pointer; transition: background 0.15s; }
  .change-link:hover { background: var(--accent-dim); }

  .config-panel {
    background: var(--bg-surface); border: 1px solid var(--border-subtle);
    border-radius: var(--radius); padding: 24px; position: relative; overflow: hidden;
  }
  .config-panel::before { content: ''; position: absolute; inset: 0; background: radial-gradient(ellipse 80% 50% at 50% 0%, rgba(232,114,42,0.02) 0%, transparent 60%); pointer-events: none; }

  .form-row { margin-bottom: 18px; position: relative; z-index: 1; }
  .form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .form-divider { border: none; border-top: 1px solid var(--border-dim); margin: 22px 0; }

  .resource-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .resource-header .label { margin-bottom: 0; }
  .resource-value { font-size: 0.78rem; font-family: var(--font-mono); font-weight: 500; color: var(--text-primary); }
  .resource-warning { font-size: 0.72rem; color: var(--accent); margin-top: 6px; display: flex; align-items: center; gap: 5px; }
  .resource-warning svg { width: 12px; height: 12px; flex-shrink: 0; }

  .slider { -webkit-appearance: none; appearance: none; width: 100%; height: 4px; border-radius: 2px; background: var(--border-dim); outline: none; cursor: pointer; }
  .slider::-webkit-slider-thumb { -webkit-appearance: none; appearance: none; width: 16px; height: 16px; border-radius: 50%; background: var(--accent); cursor: pointer; box-shadow: 0 0 8px rgba(232,114,42,0.25); }
  .slider::-moz-range-thumb { width: 16px; height: 16px; border-radius: 50%; border: none; background: var(--accent); cursor: pointer; box-shadow: 0 0 8px rgba(232,114,42,0.25); }

  .env-group { background: var(--bg-elevated); border: 1px solid var(--border-dim); border-left: 2px solid rgba(232,114,42,0.2); border-radius: var(--radius); padding: 18px; margin-bottom: 14px; }
  .env-group-label { font-size: 0.66rem; font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.1em; color: var(--text-tertiary); margin-bottom: 14px; }

  .eula-callout { background: var(--bg-elevated); border: 1px solid rgba(232,114,42,0.15); border-radius: var(--radius); padding: 16px 18px; margin-bottom: 18px; display: flex; align-items: center; justify-content: space-between; gap: 16px; position: relative; overflow: hidden; }
  .eula-callout::before { content: ''; position: absolute; top: 0; left: 10%; right: 10%; height: 1px; background: linear-gradient(90deg, transparent, var(--accent), transparent); opacity: 0.15; }
  .eula-left { display: flex; flex-direction: column; gap: 4px; }
  .eula-title { font-size: 0.84rem; font-weight: 550; }
  .eula-notice { font-size: 0.73rem; color: var(--text-tertiary); line-height: 1.4; }
  .eula-notice :global(a) { color: var(--accent); text-decoration: none; }
  .eula-notice :global(a:hover) { text-decoration: underline; }
  .eula-required { font-size: 0.62rem; font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.08em; color: var(--accent); opacity: 0.8; }

  .submit-row { display: flex; align-items: center; justify-content: flex-end; margin-top: 28px; padding-top: 20px; border-top: 1px solid var(--border-dim); position: relative; z-index: 1; }
  .submit-row .btn-solid:disabled { opacity: 0.5; cursor: not-allowed; }

  @media (max-width: 700px) { .featured-grid { grid-template-columns: repeat(2, 1fr); } }
  @media (max-width: 620px) { .form-grid { grid-template-columns: 1fr; } .config-panel { padding: 18px; } .all-header { flex-direction: column; align-items: flex-start; } .all-search { max-width: 100%; } }
</style>
