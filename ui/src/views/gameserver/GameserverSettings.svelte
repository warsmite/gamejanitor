<script lang="ts">
  import { navigate } from '$lib/router';
  import { embedded } from '$lib/base';
  import { onMount } from 'svelte';
  import { api, type Gameserver, type Game, type DynamicOption } from '$lib/api';
  import { toast, confirm, gameserverStore } from '$lib/stores';
  import { ResourceSlider, EnvEditor } from '$lib/components';

  let { id }: { id: string } = $props();
  const gsId = id;

  const can = (p: string) => gameserverStore.can(p);
  const isRunning = $derived(gameserverStore.isRunning(id));

  let gameserver = $state<Gameserver | null>(null);
  let game = $state<Game | null>(null);
  let loading = $state(true);
  let saving = $state(false);

  // Form state
  let serverName = $state('');
  let env = $state<Record<string, string>>({});
  let memoryLimitMB = $state(2048);
  let storageLimitMB = $state(0);
  let cpuLimit = $state(0);
  let cpuEnforced = $state(false);
  let backupLimit = $state(0);
  let portMode = $state('auto');
  let manualPorts = $state<{ name: string; host_port: number; container_port: number; protocol: string }[]>([]);
  let autoRestart = $state(true);
  let dynamicOptions = $state<Record<string, DynamicOption[]>>({});

  // Danger
  let updating = $state(false);
  let reinstalling = $state(false);
  let deleting = $state(false);

  // Can this user edit anything?
  const canEditAnything = $derived(
    can('gameserver.configure.name') || can('gameserver.configure.env') ||
    can('gameserver.configure.resources') || can('gameserver.configure.ports') ||
    can('gameserver.configure.auto-restart')
  );

  onMount(async () => {
    try {
      gameserver = await api.gameservers.get(gsId);
      serverName = gameserver.name;
      memoryLimitMB = gameserver.memory_limit_mb;
      storageLimitMB = gameserver.storage_limit_mb || 0;
      cpuLimit = gameserver.cpu_limit;
      cpuEnforced = gameserver.cpu_enforced;
      backupLimit = gameserver.backup_limit || 0;
      autoRestart = gameserver.auto_restart ?? false;
      portMode = gameserver.port_mode || 'auto';

      const gsEnv = typeof gameserver.env === 'string' ? JSON.parse(gameserver.env) : gameserver.env;
      if (gsEnv && typeof gsEnv === 'object') env = { ...gsEnv };

      try {
        const ports = typeof gameserver.ports === 'string' ? JSON.parse(gameserver.ports) : gameserver.ports;
        if (ports && ports.length > 0) {
          manualPorts = ports.map((p: any) => ({
            name: p.name || '', host_port: p.host_port || p.container_port,
            container_port: p.container_port, protocol: p.protocol || 'tcp',
          }));
        }
      } catch (e) { console.warn('GameserverSettings: failed to parse ports', e); }

      try {
        game = await api.games.get(gameserver.game_id);
        for (const e of game.default_env) {
          if (e.dynamic_options) {
            try { dynamicOptions[e.key] = await api.games.options(game.id, e.key); }
            catch (e) { console.warn('GameserverSettings: failed to load dynamic options', e); }
          }
        }
      } catch (e) { console.warn('GameserverSettings: game definition not found', e); }
    } catch (e: any) {
      toast(`Failed to load: ${e.message}`, 'error');
    } finally {
      loading = false;
    }
  });

  async function saveAll(andRestart = false) {
    saving = true;
    try {
      const update: Record<string, any> = {};
      if (can('gameserver.configure.name')) update.name = serverName;
      if (can('gameserver.configure.env')) update.env = env;
      if (can('gameserver.configure.resources')) {
        update.memory_limit_mb = memoryLimitMB;
        update.storage_limit_mb = storageLimitMB;
        update.cpu_limit = cpuLimit;
        update.cpu_enforced = cpuEnforced;
        update.backup_limit = backupLimit;
      }
      if (can('gameserver.configure.auto-restart')) update.auto_restart = autoRestart;

      await api.gameservers.update(gsId, update);
      gameserver = await api.gameservers.get(gsId);
      if (andRestart && isRunning) {
        await api.gameservers.restart(gsId);
        restartRequired = false;
        toast('Settings saved, restarting...', 'success');
      } else {
        toast('Settings saved', 'success');
      }
    } catch (e: any) {
      toast(`Failed to save: ${e.message}`, 'error');
    } finally {
      saving = false;
    }
  }

  async function updateGame() {
    if (!await confirm({ title: 'Update Game', message: 'Update to the latest game version? The server will restart.', confirmLabel: 'Update' })) return;
    updating = true;
    try {
      await api.gameservers.updateGame(gsId);
      toast('Game update started', 'info');
    } catch (e: any) {
      toast(`Failed: ${e.message}`, 'error');
    } finally {
      updating = false;
    }
  }

  async function reinstall() {
    if (!await confirm({ title: 'Reinstall Server', message: 'This will wipe all server data and reinstall from scratch. Backups are preserved.', confirmLabel: 'Reinstall', danger: true })) return;
    reinstalling = true;
    try {
      await api.gameservers.reinstall(gsId);
      toast('Reinstall started', 'info');
    } catch (e: any) {
      toast(`Failed: ${e.message}`, 'error');
    } finally {
      reinstalling = false;
    }
  }

  async function deleteServer() {
    if (!gameserver) return;
    if (!await confirm({ title: 'Delete Gameserver', message: `Permanently delete "${gameserver.name}" and all its data? This cannot be undone.`, confirmLabel: 'Delete', danger: true })) return;
    deleting = true;
    try {
      await api.gameservers.delete(gsId);
      toast('Gameserver deleted', 'info');
      if (embedded) {
        window.location.href = window.location.origin;
      } else {
        navigate('/');
      }
    } catch (e: any) {
      toast(`Failed: ${e.message}`, 'error');
    } finally {
      deleting = false;
    }
  }
</script>

{#if loading}
  <p style="color:var(--text-tertiary); text-align:center; padding:40px;">Loading...</p>
{:else if gameserver && game}
  <div class="settings-panel">

    {#if can('gameserver.configure.name')}
      <div class="s-section">
        <div class="s-title">Server Name</div>
        <input class="input" type="text" bind:value={serverName} placeholder="My Server">
      </div>
    {/if}

    {#if can('gameserver.configure.env') && game}
      <div class="s-section">
        <div class="s-title">Game Configuration</div>
        <EnvEditor envDefs={game.default_env} bind:values={env} {dynamicOptions} gridClass="form-grid" />
      </div>
    {/if}

    {#if can('gameserver.configure.resources') && game}
      <div class="s-section">
        <div class="s-title">Resources</div>
        <ResourceSlider label="Memory" bind:value={memoryLimitMB} min={0} max={16384} step={256} display={(v) => v === 0 ? 'Unlimited' : v < 1024 ? `${v} MB` : `${(v/1024).toFixed(v%1024===0?0:1)} GB`} />
        <div class="form-grid" style="margin-top: 14px;">
          <div class="form-row">
            <label class="label">CPU Limit</label>
            <div class="input-with-suffix">
              <input class="input input-mono" type="number" min="0" step="0.5" placeholder="Unlimited" value={cpuLimit || ''} oninput={(e) => cpuLimit = parseFloat((e.target as HTMLInputElement).value) || 0}>
              <span class="input-suffix">cores</span>
            </div>
          </div>
          <div class="form-row">
            <label class="label">Enforce CPU</label>
            <div class="toggle-row">
              <button class="toggle" class:on={cpuEnforced} onclick={() => cpuEnforced = !cpuEnforced}></button>
              <span class="toggle-label">{cpuEnforced ? 'Hard limit' : 'Soft limit'}</span>
            </div>
          </div>
        </div>
        <ResourceSlider label="Storage" bind:value={storageLimitMB} min={0} max={1024000} step={10240} display={(v) => v === 0 ? 'Unlimited' : v >= 1024 ? `${Math.round(v/1024)} GB` : `${v} MB`} />
        <div class="form-row" style="margin-top: 14px;">
          <label class="label">Backup Limit</label>
          <div class="input-with-suffix">
            <input class="input input-mono" type="number" min="0" placeholder="Global default" value={backupLimit || ''} oninput={(e) => backupLimit = parseInt((e.target as HTMLInputElement).value) || 0}>
            <span class="input-suffix">max</span>
          </div>
        </div>
      </div>
    {/if}

    {#if can('gameserver.configure.ports')}
      <div class="s-section">
        <div class="s-title">Ports</div>
        <div class="toggle-row" style="margin-bottom: 14px;">
          <button class="toggle" class:on={portMode === 'manual'} onclick={() => portMode = portMode === 'auto' ? 'manual' : 'auto'}></button>
          <span class="toggle-label">{portMode === 'auto' ? 'Auto (allocated from port range)' : 'Manual'}</span>
        </div>
        {#if portMode === 'manual'}
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
        {/if}
      </div>
    {/if}

    {#if can('gameserver.configure.auto-restart')}
      <div class="s-section">
        <div class="s-title">Behavior</div>
        <div class="toggle-row">
          <button class="toggle" class:on={autoRestart} onclick={() => autoRestart = !autoRestart}></button>
          <span class="toggle-label">Auto-restart on crash</span>
        </div>
      </div>
    {/if}

    {#if canEditAnything}
      <div class="save-row">
        <button class="btn-accent" onclick={() => saveAll(false)} disabled={saving} style="padding:9px 24px; font-size:0.86rem;">
          {saving ? 'Saving...' : 'Save'}
        </button>
        {#if isRunning && can('gameserver.restart')}
          <button class="btn-solid" onclick={() => saveAll(true)} disabled={saving} style="padding:9px 24px; font-size:0.86rem;">
            {saving ? 'Saving...' : 'Save & Restart'}
          </button>
        {/if}
      </div>
    {/if}

    {#if can('gameserver.update-game') || can('gameserver.reinstall') || can('gameserver.delete')}
      <div class="s-section">
        <div class="danger-zone">
          <div class="s-title">Danger Zone</div>
          {#if can('gameserver.update-game')}
            <div class="danger-item">
              <div class="danger-text">
                <div class="danger-label">Update Game</div>
                <div class="danger-desc">Re-runs the install script to update to the latest game version. The server will restart.</div>
              </div>
              <button class="btn-action restart" onclick={updateGame} disabled={updating} style="flex-shrink:0;">
                {updating ? 'Updating...' : 'Update'}
              </button>
            </div>
          {/if}
          {#if can('gameserver.reinstall')}
            <div class="danger-item">
              <div class="danger-text">
                <div class="danger-label">Reinstall Server</div>
                <div class="danger-desc">Wipes all data and reinstalls from scratch. Backups are preserved.</div>
              </div>
              <button class="btn-action stop" onclick={reinstall} disabled={reinstalling} style="flex-shrink:0;">
                {reinstalling ? 'Reinstalling...' : 'Reinstall'}
              </button>
            </div>
          {/if}
          {#if can('gameserver.delete')}
            <div class="danger-item">
              <div class="danger-text">
                <div class="danger-label">Delete Gameserver</div>
                <div class="danger-desc">Permanently deletes this gameserver and all its data. This cannot be undone.</div>
              </div>
              <button class="btn-action stop" onclick={deleteServer} disabled={deleting} style="flex-shrink:0;">
                {deleting ? 'Deleting...' : 'Delete'}
              </button>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
{:else if gameserver}
  <div class="settings-panel">
    <p style="color:var(--text-tertiary);">Game definition not found for "{gameserver.game_id}". Settings limited.</p>
  </div>
{/if}

<style>
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .settings-panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    padding: 24px;
    position: relative; overflow: hidden;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .settings-panel::before {
    content: ''; position: absolute; inset: 0;
    background: radial-gradient(ellipse 80% 40% at 50% 0%, rgba(232,114,42,0.015) 0%, transparent 50%);
    pointer-events: none;
  }

  .input {
    width: 100%; padding: 9px 14px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm); color: var(--text-primary);
    font-family: var(--font-body); font-size: 0.85rem; outline: none;
  }
  .input:focus { border-color: var(--accent-border); }
  .input-mono { font-family: var(--font-mono); }

  .form-row { margin-bottom: 18px; position: relative; z-index: 1; }
  .form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; align-items: end; }
  .form-grid .form-row { margin-bottom: 0; }

  .input-with-suffix { position: relative; }
  .input-with-suffix input { padding-right: 50px; }
  .input-suffix { position: absolute; right: 12px; top: 50%; transform: translateY(-50%); font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); pointer-events: none; }

  .port-row { display: flex; align-items: center; gap: 12px; padding: 8px 0; }
  .port-row + .port-row { border-top: 1px solid var(--border-dim); }
  .port-name { font-size: 0.82rem; font-weight: 500; min-width: 60px; }
  .port-field { display: flex; flex-direction: column; gap: 3px; }
  .port-label { font-size: 0.65rem; font-family: var(--font-mono); color: var(--text-tertiary); text-transform: uppercase; letter-spacing: 0.08em; }
  .port-proto { font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); text-transform: uppercase; }


  .save-row {
    display: flex; justify-content: flex-end; gap: 8px;
    position: relative; z-index: 1;
    margin: 20px 0 28px;
    padding-top: 20px;
    border-top: 1px solid var(--border-dim);
  }

  .s-section { position: relative; z-index: 1; margin-bottom: 28px; }
  .s-section:last-child { margin-bottom: 0; }
  .s-title {
    font-size: 0.68rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
    margin-bottom: 14px; padding-bottom: 8px;
    border-bottom: 1px solid var(--border-dim);
  }

  .danger-zone { background: rgba(239, 68, 68, 0.03); border: 1px solid rgba(239, 68, 68, 0.15); border-radius: var(--radius); padding: 20px; }
  .danger-zone .s-title { color: var(--danger); border-bottom-color: rgba(239, 68, 68, 0.12); }
  .danger-item { display: flex; align-items: flex-start; justify-content: space-between; padding: 12px 0; gap: 16px; }
  .danger-item + .danger-item { border-top: 1px solid rgba(239, 68, 68, 0.08); }
  .danger-text { flex: 1; }
  .danger-label { font-size: 0.88rem; font-weight: 500; color: var(--text-primary); }
  .danger-desc { font-size: 0.76rem; color: var(--text-tertiary); margin-top: 2px; }

  @media (max-width: 700px) {
    .settings-panel { padding: 18px; }
    .danger-item { flex-direction: column; }
  }
</style>
