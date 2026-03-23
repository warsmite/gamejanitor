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
  let cpuLimit = $state(0);
  let cpuEnforced = $state(false);
  let storageLimitMb = $state(0);
  let backupLimit = $state(0);    // 0 = use global default
  let portMode = $state('auto');
  let manualPorts = $state<{ name: string; host_port: number; container_port: number; protocol: string }[]>([]);
  let nodeId = $state('');
  let nodeTags = $state('');
  let autoRestart = $state(true);
  let autoStart = $state(true);
  let envValues = $state<Record<string, string>>({});
  let dynamicOptions = $state<Record<string, DynamicOption[]>>({});
  let submitting = $state(false);
  let advancedOpen = $state(false);

  // Post-creation state
  let createdServer = $state<{ id: string; name: string; sftp_password: string; sftp_username: string } | null>(null);

  const popularGames = $derived(games.filter(g => popularIds.includes(g.id)));
  const filteredGames = $derived(
    search ? games.filter(g => g.name.toLowerCase().includes(search.toLowerCase())) : games
  );

  const memoryDisplay = $derived(memoryMb === 0 ? 'Unlimited' : memoryMb >= 1024 ? `${memoryMb / 1024} GB` : `${memoryMb} MB`);
  const belowRecommended = $derived(selectedGame ? memoryMb > 0 && memoryMb < selectedGame.recommended_memory_mb : false);
  const cpuDisplay = $derived(cpuLimit === 0 ? 'Unlimited' : `${cpuLimit} cores`);
  const storageDisplay = $derived(
    storageLimitMb === 0 ? 'Unlimited' :
    storageLimitMb >= 1024000 ? '1 TB' :
    storageLimitMb >= 1024 ? `${Math.round(storageLimitMb / 1024)} GB` :
    `${storageLimitMb} MB`
  );
  const backupDisplay = $derived(backupLimit === 0 ? 'Use global setting' : `${backupLimit} max`);

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
    cpuLimit = 0;
    cpuEnforced = false;
    storageLimitMb = 0;
    backupLimit = 0;
    portMode = 'auto';

    // Initialize manual ports from game's default ports
    manualPorts = game.default_ports.map(p => ({
      name: p.name,
      host_port: p.port,
      container_port: p.port,
      protocol: p.protocol,
    }));

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

    // Initialize slider fills after DOM updates
    requestAnimationFrame(() => {
      document.querySelectorAll('.slider').forEach((el) => {
        const slider = el as HTMLInputElement;
        const pct = ((parseInt(slider.value) - parseInt(slider.min)) / (parseInt(slider.max) - parseInt(slider.min))) * 100;
        slider.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--border-dim) ${pct}%, var(--border-dim) 100%)`;
      });
    });
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
      if (nodeId.trim()) payload.node_id = nodeId.trim();
      if (nodeTags.trim()) payload.node_tags = JSON.stringify(nodeTags.split(',').map((t: string) => t.trim()).filter(Boolean));

      const result = await api.gameservers.create(payload);

      // Show SFTP password (show-once from create response)
      createdServer = {
        id: result.id,
        name: result.name,
        sftp_password: (result as any).sftp_password || '',
        sftp_username: result.sftp_username,
      };

      if (autoStart) {
        api.gameservers.start(result.id).catch(() => {});
      }
    } catch (e: any) {
      toast(`Failed to create server: ${e.message}`, 'error');
    } finally {
      submitting = false;
    }
  }

  let sftpCopied = $state(false);
  function copySftpPassword() {
    if (!createdServer) return;
    navigator.clipboard.writeText(createdServer.sftp_password).then(() => {
      sftpCopied = true;
      setTimeout(() => sftpCopied = false, 2000);
    });
  }

  function updateSliderFill(e: Event) {
    const input = e.target as HTMLInputElement;
    const pct = ((parseInt(input.value) - parseInt(input.min)) / (parseInt(input.max) - parseInt(input.min))) * 100;
    input.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--border-dim) ${pct}%, var(--border-dim) 100%)`;
    memoryMb = parseInt(input.value);
  }

  function updateStorageSlider(e: Event) {
    const input = e.target as HTMLInputElement;
    const pct = ((parseInt(input.value) - parseInt(input.min)) / (parseInt(input.max) - parseInt(input.min))) * 100;
    input.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--border-dim) ${pct}%, var(--border-dim) 100%)`;
    storageLimitMb = parseInt(input.value);
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

      <!-- ── QUICK CREATE: Name + Memory + Required fields ── -->
      <div class="form-row">
        <label class="label label-required">Server Name</label>
        <input class="input" type="text" placeholder="e.g. survival-smp" bind:value={serverName}>
      </div>

      <div class="form-row">
        <div class="resource-header">
          <span class="label">Memory</span>
          <span class="resource-value">{memoryDisplay}</span>
        </div>
        <input type="range" class="slider" min="0" max="16384" step="256"
          value={memoryMb}
          oninput={updateSliderFill}
        >
        <div class="resource-warning" class:visible={belowRecommended}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8.982 1.566a1.13 1.13 0 0 0-1.96 0L.165 13.233c-.457.778.091 1.767.98 1.767h13.713c.889 0 1.438-.99.98-1.767L8.982 1.566zM8 5c.535 0 .954.462.9.995l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 5.995A.905.905 0 0 1 8 5zm.002 6a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>
          Below recommended ({selectedGame.recommended_memory_mb >= 1024 ? `${selectedGame.recommended_memory_mb / 1024} GB` : `${selectedGame.recommended_memory_mb} MB`})
        </div>
      </div>

      <div class="form-row">
        <div class="resource-header">
          <span class="label">Storage</span>
          <span class="resource-value">{storageDisplay}</span>
        </div>
        <input type="range" class="slider storage-slider" min="0" max="1024000" step="10240"
          value={storageLimitMb}
          oninput={updateStorageSlider}
        >
        <div class="field-hint">Soft limit — not enforced by Docker, used for placement and monitoring</div>
      </div>

      <!-- Required env vars with notices (e.g. EULA) — always visible -->
      {#each envGroups() as group}
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
      {/each}

      <hr class="form-divider">

      <!-- ── GAME SETTINGS: env vars grouped ── -->
      {#each envGroups() as group}
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

      <!-- ── ADVANCED: collapsed by default ── -->
      <button class="advanced-toggle" onclick={() => advancedOpen = !advancedOpen}>
        <svg class="adv-chevron" class:open={advancedOpen} viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M4.646 1.646a.5.5 0 0 1 .708 0l6 6a.5.5 0 0 1 0 .708l-6 6a.5.5 0 0 1-.708-.708L10.293 8 4.646 2.354a.5.5 0 0 1 0-.708z"/></svg>
        Advanced
        {#if !advancedOpen}
          <span class="adv-hint">Ports, resources, placement...</span>
        {/if}
      </button>

      {#if advancedOpen}
        <div class="advanced-content">
          <!-- Ports -->
          <div class="env-group">
            <div class="env-group-label">Ports</div>
            <div class="form-row">
              <label class="label">Manual Port Assignment</label>
              <div style="display:flex; align-items:center; gap:8px;">
                <button class="toggle" class:on={portMode === 'manual'} onclick={() => portMode = portMode === 'auto' ? 'manual' : 'auto'}></button>
                <span style="font-size:0.78rem; color:var(--text-tertiary);">{portMode === 'auto' ? 'Auto (allocated from port range)' : 'Manual'}</span>
              </div>
            </div>
            {#if portMode === 'manual'}
              <div class="port-table" style="margin-top:14px;">
                {#each manualPorts as port, i}
                  <div class="port-row">
                    <span class="port-name">{port.name}</span>
                    <div class="port-field">
                      <label class="port-label">Host</label>
                      <input class="input input-mono" type="number" style="width:100px;" bind:value={manualPorts[i].host_port}>
                    </div>
                    <div class="port-field">
                      <label class="port-label">Container</label>
                      <input class="input input-mono" type="number" style="width:100px;" bind:value={manualPorts[i].container_port}>
                    </div>
                    <span class="port-proto">{port.protocol}</span>
                  </div>
                {/each}
              </div>
            {/if}
          </div>

          <!-- Resources -->
          <div class="env-group">
            <div class="env-group-label">Resources</div>
            <div class="form-grid">
              <div class="form-row">
                <div class="resource-header">
                  <span class="label">CPU Limit</span>
                  <span class="resource-value dim">{cpuDisplay}</span>
                </div>
                <div class="input-with-suffix">
                  <input class="input input-mono" type="number" min="0" step="0.5" placeholder="0" bind:value={cpuLimit}>
                  <span class="input-suffix">cores</span>
                </div>
              </div>
              <div class="form-row">
                <label class="label">Enforce CPU Limit</label>
                <div style="display:flex; align-items:center; gap:8px;">
                  <button class="toggle" class:on={cpuEnforced} onclick={() => cpuEnforced = !cpuEnforced}></button>
                  <span style="font-size:0.78rem; color:var(--text-tertiary);">{cpuEnforced ? 'Hard limit (Docker enforced)' : 'Soft limit (scheduling only)'}</span>
                </div>
              </div>
            </div>
            <div class="form-grid" style="margin-top:14px;">
              <div class="form-row">
                <div class="resource-header">
                  <span class="label">Backup Limit</span>
                  <span class="resource-value dim">{backupDisplay}</span>
                </div>
                <div class="input-with-suffix">
                  <input class="input input-mono" type="number" min="0" placeholder="0" bind:value={backupLimit}>
                  <span class="input-suffix">max</span>
                </div>
              </div>
            </div>
          </div>

          <!-- Placement -->
          <div class="env-group">
            <div class="env-group-label">Placement</div>
            <div class="form-grid">
              <div class="form-row">
                <label class="label">Node ID</label>
                <input class="input input-mono" type="text" placeholder="Auto (best available)" bind:value={nodeId} disabled>
                <span class="field-hint">Single-node mode — auto placement</span>
              </div>
              <div class="form-row">
                <label class="label">Node Tags</label>
                <input class="input input-mono" type="text" placeholder="e.g. ssd, eu-west" bind:value={nodeTags} disabled>
                <span class="field-hint">Requires multi-node deployment</span>
              </div>
            </div>
          </div>
        </div>
      {/if}

      <!-- ── SUBMIT ── -->
      <div class="submit-row">
        <div class="submit-left">
          <div class="submit-toggle">
            <button class="toggle" class:on={autoRestart} onclick={() => autoRestart = !autoRestart}></button>
            <span class="submit-toggle-label">Auto-restart</span>
          </div>
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

  <!-- SFTP password modal (shown after creation) -->
  {#if createdServer}
    <div class="modal-overlay" onclick={() => goto(`/gameservers/${createdServer?.id}`)}>
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <div class="modal-title">Gameserver Created</div>
        <p class="modal-text">Your SFTP credentials are below. <strong>Save the password now</strong> — it cannot be retrieved later.</p>

        <div class="credential-block">
          <div class="credential-row">
            <span class="credential-label">Username</span>
            <span class="credential-value">{createdServer.sftp_username}</span>
          </div>
          <div class="credential-row">
            <span class="credential-label">Password</span>
            <span class="credential-value">{createdServer.sftp_password}</span>
          </div>
        </div>

        <div class="modal-actions">
          <button class="btn-accent" onclick={copySftpPassword}>
            {sftpCopied ? 'Copied!' : 'Copy Password'}
          </button>
          <button class="btn-solid" onclick={() => goto(`/gameservers/${createdServer?.id}`)}>
            Go to Server
          </button>
        </div>
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
  .resource-warning { font-size: 0.72rem; color: var(--accent); margin-top: 6px; display: flex; align-items: center; gap: 5px; visibility: hidden; }
  .resource-warning.visible { visibility: visible; }
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

  .input-with-suffix { position: relative; }
  .input-with-suffix input { padding-right: 50px; }
  .input-suffix { position: absolute; right: 12px; top: 50%; transform: translateY(-50%); font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); pointer-events: none; }

  .port-row {
    display: flex; align-items: center; gap: 12px;
    padding: 8px 0;
  }
  .port-row + .port-row { border-top: 1px solid var(--border-dim); }
  .port-name { font-size: 0.82rem; font-weight: 500; min-width: 60px; }
  .port-field { display: flex; flex-direction: column; gap: 3px; }
  .port-label { font-size: 0.65rem; font-family: var(--font-mono); color: var(--text-tertiary); text-transform: uppercase; letter-spacing: 0.08em; }
  .port-proto { font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); text-transform: uppercase; }

  /* Advanced toggle */
  .advanced-toggle {
    display: flex; align-items: center; gap: 6px;
    font-size: 0.8rem; color: var(--text-tertiary);
    cursor: pointer; background: none; border: none;
    font-family: var(--font-body); padding: 12px 0;
    transition: color 0.15s; margin-top: 8px;
  }
  .advanced-toggle:hover { color: var(--text-secondary); }
  .adv-chevron { width: 14px; height: 14px; transition: transform 0.2s; }
  .adv-chevron.open { transform: rotate(90deg); }
  .adv-hint { font-size: 0.72rem; color: var(--text-tertiary); opacity: 0.5; margin-left: 4px; }

  .advanced-content { animation: slide-down 0.25s ease-out; padding-top: 10px; }
  @keyframes slide-down { from { opacity: 0; transform: translateY(-6px); } to { opacity: 1; transform: translateY(0); } }

  .resource-value.dim { color: var(--text-tertiary); font-size: 0.72rem; }

  .field-hint { font-size: 0.68rem; color: var(--text-tertiary); opacity: 0.6; margin-top: 4px; }

  input:disabled { opacity: 0.4; cursor: not-allowed; }

  .submit-row { display: flex; align-items: center; justify-content: space-between; margin-top: 28px; padding-top: 20px; border-top: 1px solid var(--border-dim); position: relative; z-index: 1; }
  .submit-left { display: flex; align-items: center; gap: 16px; }
  .submit-toggle { display: flex; align-items: center; gap: 6px; }
  .submit-toggle-label { font-size: 0.78rem; color: var(--text-tertiary); }
  .submit-row .btn-solid:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Modal */
  .modal-overlay {
    position: fixed; inset: 0; z-index: 200;
    background: rgba(0,0,0,0.6);
    display: grid; place-items: center;
    animation: fade-in 0.2s ease-out;
  }
  @keyframes fade-in { from { opacity: 0; } to { opacity: 1; } }

  .modal {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 12px;
    padding: 28px;
    max-width: 440px; width: 90%;
    animation: modal-up 0.3s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes modal-up { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }

  .modal-title { font-size: 1.1rem; font-weight: 600; margin-bottom: 8px; }
  .modal-text { font-size: 0.84rem; color: var(--text-secondary); line-height: 1.5; margin-bottom: 18px; }
  .modal-text strong { color: var(--accent); }

  .credential-block {
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    padding: 14px;
    margin-bottom: 20px;
  }
  .credential-row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 6px 0;
  }
  .credential-row + .credential-row { border-top: 1px solid var(--border-dim); }
  .credential-label { font-size: 0.76rem; color: var(--text-tertiary); }
  .credential-value { font-size: 0.84rem; font-family: var(--font-mono); font-weight: 500; color: var(--text-primary); }

  .modal-actions { display: flex; justify-content: flex-end; gap: 8px; }

  @media (max-width: 700px) { .featured-grid { grid-template-columns: repeat(2, 1fr); } }
  @media (max-width: 620px) { .form-grid { grid-template-columns: 1fr; } .config-panel { padding: 18px; } .all-header { flex-direction: column; align-items: flex-start; } .all-search { max-width: 100%; } }
</style>
