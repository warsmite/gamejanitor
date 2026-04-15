<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Backup } from '$lib/api';
  import { gameserverStore, toast, confirm } from '$lib/stores';

  let { id }: { id: string } = $props();

  // Read backups from store — SSE keeps them updated
  const gsState = $derived(gameserverStore.getState(id));
  const gameserver = $derived(gsState?.gameserver);
  const backups = $derived(gsState?.backups ?? []);
  const loading = $derived(gsState?.backups === null);
  let creating = $state(false);
  let globalMaxBackups = $state(0);

  const totalSize = $derived(
    backups
      .filter(b => b.status === 'completed')
      .reduce((sum, b) => sum + b.size_bytes, 0)
  );

  // Per-server override takes precedence; 0 means "use global default".
  const effectiveLimit = $derived(gameserver?.backup_limit || globalMaxBackups);
  const usingGlobal = $derived(!gameserver?.backup_limit);

  onMount(async () => {
    // Trigger lazy load if not already loaded
    if (gsState?.backups === null) {
      gameserverStore.loadBackups(id);
    }
    try {
      const resp = await api.settings.get();
      if (resp?.settings?.max_backups) globalMaxBackups = resp.settings.max_backups;
    } catch (e) {
      console.warn('Backups: failed to load settings', e);
    }
  });

  async function createBackup() {
    creating = true;
    try {
      await api.backups.create(id);
      // SSE will refresh the list via store
    } catch (e: any) {
      toast(`Failed to create backup: ${e.message}`, 'error');
    } finally {
      creating = false;
    }
  }

  async function restoreBackup(backup: Backup) {
    if (!await confirm({ title: 'Restore Backup', message: `Restore from "${backup.name}"? This will overwrite all current server data. There is no rollback.`, confirmLabel: 'Restore', danger: true })) return;
    try {
      await api.backups.restore(id, backup.id);
      toast('Restore started', 'info');
    } catch (e: any) {
      toast(`Failed to restore: ${e.message}`, 'error');
    }
  }

  function downloadBackup(backup: Backup) {
    const url = api.backups.downloadUrl(id, backup.id);
    window.open(url, '_blank');
  }

  async function deleteBackup(backup: Backup) {
    if (!await confirm({ title: 'Delete Backup', message: `Delete backup "${backup.name}"? This cannot be undone.`, confirmLabel: 'Delete', danger: true })) return;
    try {
      await api.backups.delete(id, backup.id);
      // SSE will refresh the list via store
    } catch (e: any) {
      toast(`Failed to delete: ${e.message}`, 'error');
    }
  }

  function formatSize(bytes: number): string {
    if (bytes === 0) return '—';
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(0)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  }

  function formatDate(iso: string): string {
    return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }
</script>

<div class="backups-toolbar">
  <span class="backups-info">
    {#if effectiveLimit > 0}
      {backups.length} / {effectiveLimit} backup{effectiveLimit !== 1 ? 's' : ''}{#if usingGlobal} <span class="limit-hint">(global)</span>{/if}
    {:else}
      {backups.length} backup{backups.length !== 1 ? 's' : ''}
    {/if}
    {#if totalSize > 0} · {formatSize(totalSize)} total{/if}
  </span>
  <button class="btn-solid" onclick={createBackup} disabled={creating} style="font-size:0.82rem; padding:8px 16px;">
    <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
    {creating ? 'Creating...' : 'Create Backup'}
  </button>
</div>

<div class="backup-panel">
  {#if loading}
    <div class="backup-row"><div class="backup-info"><span class="backup-name" style="color:var(--text-tertiary)">Loading...</span></div></div>
  {:else if backups.length === 0}
    <div class="backup-empty">
      <span>No backups yet. Create one to save a snapshot of your server you can restore later.</span>
    </div>
  {:else}
    {#each backups as backup (backup.id)}
      <div class="backup-row" class:in-progress={backup.status === 'in_progress'} class:failed={backup.status === 'failed'}>
        <div class="backup-info">
          <div class="backup-name">{backup.name}</div>
          <div class="backup-meta">
            <span class="backup-status {backup.status === 'in_progress' ? 'in-progress' : backup.status}">
              <span class="bs-dot"></span>
              {backup.status === 'in_progress' ? 'In Progress' : backup.status === 'completed' ? 'Completed' : 'Failed'}
            </span>
            {#if backup.status === 'completed' && backup.size_bytes > 0}
              <span class="backup-meta-sep">·</span>
              {formatSize(backup.size_bytes)}
            {/if}
            <span class="backup-meta-sep">·</span>
            {formatDate(backup.created_at)}
          </div>
          {#if backup.status === 'failed' && backup.error_reason}
            <div class="backup-error">
              <svg viewBox="0 0 16 16" fill="currentColor" width="11" height="11"><path d="M8.982 1.566a1.13 1.13 0 0 0-1.96 0L.165 13.233c-.457.778.091 1.767.98 1.767h13.713c.889 0 1.438-.99.98-1.767L8.982 1.566zM8 5c.535 0 .954.462.9.995l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 5.995A.905.905 0 0 1 8 5zm.002 6a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>
              {backup.error_reason}
            </div>
          {/if}
        </div>
        <div class="backup-actions">
          {#if backup.status === 'completed'}
            <button class="bk-act restore" onclick={() => restoreBackup(backup)}>Restore</button>
            <button class="bk-act" onclick={() => downloadBackup(backup)}>Download</button>
          {/if}
          {#if backup.status !== 'in_progress'}
            <button class="bk-act danger" onclick={() => deleteBackup(backup)}>Delete</button>
          {/if}
        </div>
      </div>
    {/each}
  {/if}
</div>

<style>
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .backups-toolbar {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 12px;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .backups-info {
    font-size: 0.78rem; color: var(--text-tertiary); font-family: var(--font-mono);
  }
  .limit-hint { opacity: 0.55; }

  .backup-panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1) 0.05s both;
  }

  .backup-row {
    display: flex; align-items: center;
    padding: 14px 18px;
    gap: 14px;
    transition: background 0.12s;
    border-left: 2px solid transparent;
  }
  .backup-row:hover { background: var(--bg-elevated); }
  .backup-row + .backup-row { border-top: 1px solid var(--border-dim); }
  .backup-row.in-progress { border-left-color: var(--accent); }
  .backup-row.failed { border-left-color: var(--danger); }

  .backup-info { flex: 1; min-width: 0; }
  .backup-name { font-size: 0.88rem; font-weight: 500; }
  .backup-meta {
    display: flex; align-items: center; gap: 8px;
    margin-top: 3px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }
  .backup-meta-sep { opacity: 0.3; }

  .backup-status {
    display: inline-flex; align-items: center; gap: 5px;
    padding: 3px 9px 3px 7px; border-radius: 100px;
    font-size: 0.68rem; font-family: var(--font-mono);
    font-weight: 500; text-transform: uppercase; letter-spacing: 0.04em;
  }
  .backup-status.completed { background: var(--live-dim); color: var(--live); }
  .backup-status.completed .bs-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--live); }
  .backup-status.in-progress { background: rgba(232,114,42,0.08); color: var(--accent); }
  .backup-status.in-progress .bs-dot {
    width: 6px; height: 6px; border-radius: 50%; background: var(--accent);
    animation: bp-pulse 1.5s ease-in-out infinite;
  }
  @keyframes bp-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }
  .backup-status.failed { background: rgba(239,68,68,0.08); color: var(--danger); }
  .backup-status.failed .bs-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--danger); }

  .backup-error {
    font-size: 0.7rem; color: var(--danger);
    margin-top: 3px;
    display: flex; align-items: center; gap: 4px;
  }

  .backup-actions {
    display: flex; gap: 2px;
    flex-shrink: 0;
    opacity: 0; transition: opacity 0.15s;
  }
  .backup-row:hover .backup-actions { opacity: 1; }

  .bk-act {
    padding: 5px 10px; border-radius: 4px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); background: none; border: none;
    cursor: pointer; transition: color 0.15s, background 0.15s;
  }
  .bk-act:hover { color: var(--accent); background: var(--accent-subtle); }
  .bk-act.restore:hover { color: var(--caution); background: rgba(245,158,11,0.06); }
  .bk-act.danger:hover { color: var(--danger); background: rgba(239,68,68,0.06); }

  .backup-empty {
    padding: 32px 18px; text-align: center;
    font-size: 0.82rem; color: var(--text-tertiary); line-height: 1.5;
  }

  @media (max-width: 700px) {
    .backups-toolbar { flex-direction: column; align-items: flex-start; gap: 10px; }
    .backup-actions { opacity: 1; }
  }
</style>
