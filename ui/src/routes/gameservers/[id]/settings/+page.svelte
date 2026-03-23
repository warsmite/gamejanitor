<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, type Gameserver, type Game, type DynamicOption } from '$lib/api';
  import { toast, confirm } from '$lib/stores';
  import { GameIcon, GameserverForm } from '$lib/components';

  const gsId = $derived($page.params.id as string);

  let gameserver = $state<Gameserver | null>(null);
  let game = $state<Game | null>(null);
  let loading = $state(true);
  let saving = $state(false);

  // Form state — bound to GameserverForm
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

  // SFTP
  let sftpPassword = $state('');
  let regenerating = $state(false);

  // Danger
  let updating = $state(false);
  let reinstalling = $state(false);
  let deleting = $state(false);

  onMount(async () => {
    try {
      gameserver = await api.gameservers.get(gsId);
      serverName = gameserver.name;
      memoryLimitMB = gameserver.memory_limit_mb;
      storageLimitMB = gameserver.storage_limit_mb || 0;
      cpuLimit = gameserver.cpu_limit;
      cpuEnforced = gameserver.cpu_enforced;
      backupLimit = gameserver.backup_limit || 0;
      autoRestart = gameserver.auto_restart;
      portMode = gameserver.port_mode || 'auto';

      const gsEnv = typeof gameserver.env === 'string' ? JSON.parse(gameserver.env) : gameserver.env;
      if (gsEnv && typeof gsEnv === 'object') {
        env = { ...gsEnv };
      }

      // Parse existing ports for manual mode
      try {
        const ports = typeof gameserver.ports === 'string' ? JSON.parse(gameserver.ports) : gameserver.ports;
        if (ports && ports.length > 0) {
          manualPorts = ports.map((p: any) => ({
            name: p.name || '',
            host_port: p.host_port || p.container_port,
            container_port: p.container_port,
            protocol: p.protocol || 'tcp',
          }));
        }
      } catch { /* ignore */ }

      try {
        game = await api.games.get(gameserver.game_id);
        for (const e of game.default_env) {
          if (e.dynamic_options) {
            try {
              dynamicOptions[e.key] = await api.games.options(game.id, e.key);
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

  async function saveAll() {
    saving = true;
    try {
      await api.gameservers.update(gsId, {
        name: serverName,
        env,
        memory_limit_mb: memoryLimitMB,
        storage_limit_mb: storageLimitMB,
        cpu_limit: cpuLimit,
        cpu_enforced: cpuEnforced,
        backup_limit: backupLimit,
        auto_restart: autoRestart,
      });
      if (gameserver) {
        gameserver = { ...gameserver, name: serverName, memory_limit_mb: memoryLimitMB, storage_limit_mb: storageLimitMB, cpu_limit: cpuLimit, cpu_enforced: cpuEnforced, backup_limit: backupLimit, auto_restart: autoRestart };
      }
      toast('Settings saved', 'success');
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
      goto('/');
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
    <GameserverForm
      {game}
      bind:serverName={serverName}
      bind:memoryMb={memoryLimitMB}
      bind:storageLimitMb={storageLimitMB}
      bind:cpuLimit={cpuLimit}
      bind:cpuEnforced={cpuEnforced}
      bind:backupLimit={backupLimit}
      bind:portMode={portMode}
      bind:manualPorts={manualPorts}
      bind:autoRestart={autoRestart}
      bind:envValues={env}
      {dynamicOptions}
    />

    <!-- Save -->
    <div class="save-row">
      <button class="btn-solid" onclick={saveAll} disabled={saving} style="padding:9px 24px; font-size:0.86rem;">
        {saving ? 'Saving...' : 'Save Changes'}
      </button>
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
          </div>
          <button class="btn-action stop" onclick={deleteServer} disabled={deleting} style="flex-shrink:0;">
            {deleting ? 'Deleting...' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  </div>
{:else if gameserver}
  <!-- Game definition not found — show minimal settings -->
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

  .save-row {
    display: flex; justify-content: flex-end;
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

  .s-mono-value {
    font-family: var(--font-mono); font-size: 0.85rem;
    color: var(--text-secondary);
    padding: 9px 14px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
  }

  .sftp-row { display: flex; align-items: center; justify-content: space-between; gap: 14px; }
  .sftp-warn { font-size: 0.72rem; color: var(--text-tertiary); margin-top: 6px; }
  .sftp-pass { margin-top: 10px; }

  .danger-zone { background: rgba(239, 68, 68, 0.03); border: 1px solid rgba(239, 68, 68, 0.15); border-radius: var(--radius); padding: 20px; }
  .danger-zone .s-title { color: var(--danger); border-bottom-color: rgba(239, 68, 68, 0.12); }
  .danger-item { display: flex; align-items: flex-start; justify-content: space-between; padding: 12px 0; gap: 16px; }
  .danger-item + .danger-item { border-top: 1px solid rgba(239, 68, 68, 0.08); }
  .danger-text { flex: 1; }
  .danger-label { font-size: 0.88rem; font-weight: 500; color: var(--text-primary); }
  .danger-desc { font-size: 0.76rem; color: var(--text-tertiary); margin-top: 2px; }

  @media (max-width: 700px) {
    .settings-panel { padding: 18px; }
    .sftp-row { flex-direction: column; align-items: flex-start; }
    .danger-item { flex-direction: column; }
  }
</style>
