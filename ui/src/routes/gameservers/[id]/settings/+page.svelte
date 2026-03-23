<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, type Gameserver, type Game, type EnvVar, type DynamicOption } from '$lib/api';
  import { toast } from '$lib/stores';
  import { GameIcon } from '$lib/components';

  const gsId = $derived($page.params.id as string);

  let gameserver = $state<Gameserver | null>(null);
  let game = $state<Game | null>(null);
  let loading = $state(true);
  let saving = $state(false);

  // Editable fields
  let serverName = $state('');
  let env = $state<Record<string, string>>({});
  let memoryLimitMB = $state(2048);

  // SFTP
  let sftpPassword = $state('');
  let regenerating = $state(false);

  // Danger
  let updating = $state(false);
  let reinstalling = $state(false);
  let deleteConfirmName = $state('');
  let deleting = $state(false);

  // Dynamic options cache
  let dynamicOptions = $state<Record<string, DynamicOption[]>>({});

  // Group env vars (excluding system/autogenerate)
  const visibleEnvVars = $derived(
    game?.default_env.filter(e => !e.system && !e.autogenerate) || []
  );

  const envGroups = $derived(() => {
    const groups: Record<string, EnvVar[]> = {};
    for (const e of visibleEnvVars) {
      const group = e.group || 'General';
      if (!groups[group]) groups[group] = [];
      groups[group].push(e);
    }
    return groups;
  });

  onMount(async () => {
    try {
      gameserver = await api.gameservers.get(gsId);
      serverName = gameserver.name;
      memoryLimitMB = gameserver.memory_limit_mb;

      // Parse env from gameserver
      const gsEnv = typeof gameserver.env === 'string' ? JSON.parse(gameserver.env) : gameserver.env;
      if (gsEnv && typeof gsEnv === 'object') {
        env = { ...gsEnv };
      }

      try {
        game = await api.games.get(gameserver.game_id);
        // Load dynamic options for env vars that have them
        for (const e of game.default_env) {
          if (e.dynamic_options) {
            try {
              dynamicOptions[e.key] = await api.games.options(game.id, e.dynamic_options.source);
            } catch { /* non-fatal */ }
          }
        }
      } catch { /* game def not found */ }
    } catch (e: any) {
      toast(`Failed to load: ${e.message}`, 'error');
    } finally {
      loading = false;
    }
  });

  async function saveGeneral() {
    saving = true;
    try {
      await api.gameservers.update(gsId, { name: serverName });
      if (gameserver) gameserver = { ...gameserver, name: serverName };
      toast('Saved', 'success');
    } catch (e: any) {
      toast(`Failed to save: ${e.message}`, 'error');
    } finally {
      saving = false;
    }
  }

  async function saveEnv() {
    saving = true;
    try {
      await api.gameservers.update(gsId, { env });
      toast('Environment saved', 'success');
    } catch (e: any) {
      toast(`Failed to save: ${e.message}`, 'error');
    } finally {
      saving = false;
    }
  }

  async function saveResources() {
    saving = true;
    try {
      await api.gameservers.update(gsId, { memory_limit_mb: memoryLimitMB });
      if (gameserver) gameserver = { ...gameserver, memory_limit_mb: memoryLimitMB };
      toast('Resources saved', 'success');
    } catch (e: any) {
      toast(`Failed to save: ${e.message}`, 'error');
    } finally {
      saving = false;
    }
  }

  async function regenerateSftpPassword() {
    regenerating = true;
    try {
      const result = await api.gameservers.regenerateSftpPassword(gsId);
      sftpPassword = result.sftp_password;
      toast('SFTP password regenerated', 'success');
    } catch (e: any) {
      toast(`Failed: ${e.message}`, 'error');
    } finally {
      regenerating = false;
    }
  }

  async function updateGame() {
    if (!confirm('Update the game to the latest version? The server will restart.')) return;
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
    if (!confirm('Reinstall will wipe all server data. Backups are preserved. Continue?')) return;
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
    if (!gameserver || deleteConfirmName !== gameserver.name) return;
    deleting = true;
    try {
      await api.gameservers.delete(gsId);
      toast('Gameserver deleted', 'info');
      goto('/');
    } catch (e: any) {
      toast(`Failed: ${e.message}`, 'error');
    } finally {
      deleting = false;
    }
  }

  function memoryLabel(mb: number): string {
    if (mb === 0) return 'Unlimited';
    if (mb < 1024) return `${mb} MB`;
    return `${(mb / 1024).toFixed(mb % 1024 === 0 ? 0 : 1)} GB`;
  }

  function updateSliderBackground(e: Event) {
    const el = e.target as HTMLInputElement;
    const min = Number(el.min);
    const max = Number(el.max);
    const val = Number(el.value);
    const pct = ((val - min) / (max - min)) * 100;
    el.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--border-dim) ${pct}%, var(--border-dim) 100%)`;
  }

  function renderEnvInput(ev: EnvVar): string {
    return env[ev.key] ?? ev.default ?? '';
  }
</script>

{#if loading}
  <p style="color:var(--text-tertiary); text-align:center; padding:40px;">Loading...</p>
{:else if gameserver}
  <div class="settings-panel">

    <!-- General -->
    <div class="s-section">
      <div class="s-title">General</div>
      <div class="s-grid">
        <div class="s-row">
          <label class="label">Server Name</label>
          <input class="input" type="text" bind:value={serverName}>
        </div>
        <div class="s-row">
          <label class="label">Game</label>
          <div class="s-readonly">
            {#if game}
              <GameIcon src={game.icon_path} name={game.name} size={20} />
              <span style="margin-left:8px;">{game.name}</span>
            {:else}
              {gameserver.game_id}
            {/if}
          </div>
        </div>
      </div>
      <div class="s-save-row">
        <button class="btn-solid" onclick={saveGeneral} disabled={saving} style="padding:8px 20px; font-size:0.84rem;">
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>
    </div>

    <!-- Environment Variables -->
    {#if visibleEnvVars.length > 0}
      <div class="s-section">
        <div class="s-title">Environment Variables</div>
        {#each Object.entries(envGroups()) as [groupName, vars]}
          <div class="env-group">
            <div class="env-group-label">{groupName}</div>
            <div class="s-grid">
              {#each vars as ev}
                <div class="s-row">
                  <label class="label">{ev.label || ev.key}</label>
                  {#if ev.type === 'boolean'}
                    <div style="display:flex; align-items:center; gap:8px;">
                      <button class="toggle" class:on={env[ev.key] === 'true' || (!env[ev.key] && ev.default === 'true')} onclick={() => {
                        const current = env[ev.key] ?? ev.default ?? 'false';
                        env[ev.key] = current === 'true' ? 'false' : 'true';
                      }}></button>
                      <span style="font-size:0.78rem; color:var(--text-tertiary);">
                        {(env[ev.key] ?? ev.default) === 'true' ? 'Enabled' : 'Disabled'}
                      </span>
                    </div>
                  {:else if ev.options && ev.options.length > 0}
                    <select class="select" value={renderEnvInput(ev)} onchange={(e) => env[ev.key] = (e.target as HTMLSelectElement).value}>
                      {#each ev.options as opt}
                        <option value={opt}>{opt}</option>
                      {/each}
                    </select>
                  {:else if dynamicOptions[ev.key]}
                    <select class="select" value={renderEnvInput(ev)} onchange={(e) => env[ev.key] = (e.target as HTMLSelectElement).value}>
                      {#each dynamicOptions[ev.key] as opt}
                        <option value={opt.value}>{opt.label}</option>
                      {/each}
                    </select>
                  {:else}
                    <input
                      class="input"
                      class:input-mono={ev.type === 'number'}
                      type={ev.type === 'number' ? 'number' : 'text'}
                      value={renderEnvInput(ev)}
                      oninput={(e) => env[ev.key] = (e.target as HTMLInputElement).value}
                    >
                  {/if}
                </div>
              {/each}
            </div>
          </div>
        {/each}
        <div class="s-save-row">
          <button class="btn-solid" onclick={saveEnv} disabled={saving} style="padding:8px 20px; font-size:0.84rem;">
            {saving ? 'Saving...' : 'Save Changes'}
          </button>
        </div>
      </div>
    {/if}

    <!-- Resources -->
    <div class="s-section">
      <div class="s-title">Resources</div>
      <div class="s-row">
        <div class="resource-header">
          <span class="label">Memory</span>
          <span class="resource-value">{memoryLabel(memoryLimitMB)}</span>
        </div>
        <input
          type="range" class="slider"
          min="0" max="16384" step="256"
          bind:value={memoryLimitMB}
          oninput={updateSliderBackground}
        >
      </div>
      <div class="s-save-row" style="margin-top:14px;">
        <button class="btn-solid" onclick={saveResources} disabled={saving} style="padding:8px 20px; font-size:0.84rem;">
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>
    </div>

    <!-- SFTP -->
    <div class="s-section">
      <div class="s-title">SFTP</div>
      <div class="sftp-row">
        <div style="flex:1;">
          <label class="label">Username</label>
          <div class="s-mono-value">{gameserver.sftp_username}</div>
        </div>
        <button class="btn-accent" onclick={regenerateSftpPassword} disabled={regenerating} style="padding:8px 14px; font-size:0.8rem; align-self:flex-end;">
          {regenerating ? 'Regenerating...' : 'Regenerate Password'}
        </button>
      </div>
      {#if sftpPassword}
        <div class="sftp-pass">
          <label class="label">New Password (copy now — shown only once)</label>
          <div class="s-mono-value" style="color:var(--accent);">{sftpPassword}</div>
        </div>
      {:else}
        <div class="sftp-warn">Current SFTP password will stop working immediately.</div>
      {/if}
    </div>

    <!-- Danger Zone -->
    <div class="s-section">
      <div class="danger-zone">
        <div class="s-title">Danger Zone</div>
        <div class="danger-item">
          <div class="danger-text">
            <div class="danger-label">Update Game</div>
            <div class="danger-desc">Re-runs the install script to update to the latest game version. The server will restart.</div>
          </div>
          <button class="btn-action restart" onclick={updateGame} disabled={updating} style="flex-shrink:0;">
            {updating ? 'Updating...' : 'Update'}
          </button>
        </div>
        <div class="danger-item">
          <div class="danger-text">
            <div class="danger-label">Reinstall Server</div>
            <div class="danger-desc">Wipes all data and reinstalls from scratch. Backups are preserved.</div>
          </div>
          <button class="btn-action stop" onclick={reinstall} disabled={reinstalling} style="flex-shrink:0;">
            {reinstalling ? 'Reinstalling...' : 'Reinstall'}
          </button>
        </div>
        <div class="danger-item">
          <div class="danger-text">
            <div class="danger-label">Delete Gameserver</div>
            <div class="danger-desc">Permanently deletes this gameserver and all its data. This cannot be undone.</div>
            <div class="delete-confirm">
              <input
                class="input" type="text"
                placeholder="Type server name to confirm..."
                bind:value={deleteConfirmName}
                style="max-width:280px; margin-top:8px;"
              >
            </div>
          </div>
          <button
            class="btn-action stop"
            onclick={deleteServer}
            disabled={deleting || deleteConfirmName !== gameserver.name}
            style="flex-shrink:0;"
          >
            {deleting ? 'Deleting...' : 'Delete'}
          </button>
        </div>
      </div>
    </div>

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

  .s-section { position: relative; z-index: 1; margin-bottom: 28px; }
  .s-section:last-child { margin-bottom: 0; }

  .s-title {
    font-size: 0.68rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
    margin-bottom: 14px;
    padding-bottom: 8px;
    border-bottom: 1px solid var(--border-dim);
  }

  .s-row { margin-bottom: 14px; }
  .s-row:last-child { margin-bottom: 0; }
  .s-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }

  .s-readonly {
    display: flex; align-items: center;
    padding: 9px 14px;
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    font-size: 0.85rem; color: var(--text-tertiary);
  }

  .s-mono-value {
    font-family: var(--font-mono); font-size: 0.85rem;
    color: var(--text-secondary);
    padding: 9px 14px;
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
  }

  .env-group {
    background: var(--bg-elevated);
    border: 1px solid var(--border-dim);
    border-left: 2px solid rgba(232, 114, 42, 0.2);
    border-radius: var(--radius);
    padding: 18px;
    margin-bottom: 14px;
  }
  .env-group:last-child { margin-bottom: 0; }
  .env-group-label {
    font-size: 0.66rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
    margin-bottom: 14px;
  }

  .s-save-row {
    display: flex; justify-content: flex-end;
    margin-top: 16px;
  }

  /* Resources */
  .resource-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .resource-header .label { margin-bottom: 0; }
  .resource-value { font-size: 0.78rem; font-family: var(--font-mono); font-weight: 500; color: var(--text-primary); }

  .slider {
    -webkit-appearance: none; appearance: none;
    width: 100%; height: 4px; border-radius: 2px;
    background: var(--border-dim); outline: none; cursor: pointer;
  }
  .slider::-webkit-slider-thumb {
    -webkit-appearance: none; appearance: none;
    width: 16px; height: 16px; border-radius: 50%;
    background: var(--accent); cursor: pointer;
    box-shadow: 0 0 8px rgba(232,114,42,0.25);
  }
  .slider::-moz-range-thumb {
    width: 16px; height: 16px; border-radius: 50%; border: none;
    background: var(--accent); cursor: pointer;
    box-shadow: 0 0 8px rgba(232,114,42,0.25);
  }

  /* SFTP */
  .sftp-row {
    display: flex; align-items: center; justify-content: space-between;
    gap: 14px;
  }
  .sftp-warn {
    font-size: 0.72rem; color: var(--text-tertiary);
    margin-top: 6px;
  }
  .sftp-pass { margin-top: 10px; }

  /* Danger zone */
  .danger-zone {
    background: rgba(239, 68, 68, 0.03);
    border: 1px solid rgba(239, 68, 68, 0.15);
    border-radius: var(--radius);
    padding: 20px;
  }
  .danger-zone .s-title {
    color: var(--danger);
    border-bottom-color: rgba(239, 68, 68, 0.12);
  }
  .danger-item {
    display: flex; align-items: flex-start; justify-content: space-between;
    padding: 12px 0;
    gap: 16px;
  }
  .danger-item + .danger-item { border-top: 1px solid rgba(239, 68, 68, 0.08); }
  .danger-text { flex: 1; }
  .danger-label { font-size: 0.88rem; font-weight: 500; color: var(--text-primary); }
  .danger-desc { font-size: 0.76rem; color: var(--text-tertiary); margin-top: 2px; }

  @media (max-width: 700px) {
    .s-grid { grid-template-columns: 1fr; }
    .settings-panel { padding: 18px; }
    .sftp-row { flex-direction: column; align-items: flex-start; }
    .danger-item { flex-direction: column; }
  }
</style>
