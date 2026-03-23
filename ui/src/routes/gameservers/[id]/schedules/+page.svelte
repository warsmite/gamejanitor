<script lang="ts">
  import { page } from '$app/stores';
  import { onMount } from 'svelte';
  import { api, type Schedule } from '$lib/api';
  import { toast } from '$lib/stores';

  const gsId = $derived($page.params.id as string);

  let schedules = $state<Schedule[]>([]);
  let loading = $state(true);

  // Create form
  let showCreate = $state(false);
  let createName = $state('');
  let createType = $state('restart');
  let createCron = $state('');
  let createPayload = $state('');
  let creating = $state(false);

  // Edit form
  let editingId = $state('');
  let editName = $state('');
  let editCron = $state('');
  let editPayload = $state('');

  const typeColors: Record<string, string> = {
    restart: 'restart',
    backup: 'backup',
    command: 'command',
    update: 'update',
  };

  onMount(async () => {
    await loadSchedules();
  });

  async function loadSchedules() {
    try {
      schedules = await api.schedules.list(gsId);
    } catch (e: any) {
      toast(`Failed to load schedules: ${e.message}`, 'error');
    } finally {
      loading = false;
    }
  }

  async function createSchedule() {
    if (!createName || !createCron) return;
    creating = true;
    try {
      await api.schedules.create(gsId, {
        name: createName,
        type: createType,
        cron_expr: createCron,
        payload: createType === 'command' ? createPayload : '',
        enabled: true,
      });
      showCreate = false;
      createName = ''; createCron = ''; createPayload = '';
      await loadSchedules();
    } catch (e: any) {
      toast(`Failed to create schedule: ${e.message}`, 'error');
    } finally {
      creating = false;
    }
  }

  function startEdit(s: Schedule) {
    editingId = s.id;
    editName = s.name;
    editCron = s.cron_expr;
    editPayload = s.payload;
  }

  async function saveEdit(s: Schedule) {
    try {
      await api.schedules.update(gsId, s.id, {
        name: editName,
        cron_expr: editCron,
        payload: s.type === 'command' ? editPayload : undefined,
      });
      editingId = '';
      await loadSchedules();
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    }
  }

  async function toggleEnabled(s: Schedule) {
    try {
      await api.schedules.update(gsId, s.id, { enabled: !s.enabled });
      await loadSchedules();
    } catch (e: any) {
      toast(`Failed to toggle: ${e.message}`, 'error');
    }
  }

  async function deleteSchedule(s: Schedule) {
    if (!confirm(`Delete schedule "${s.name}"?`)) return;
    try {
      await api.schedules.delete(gsId, s.id);
      await loadSchedules();
    } catch (e: any) {
      toast(`Failed to delete: ${e.message}`, 'error');
    }
  }

  function formatDate(iso: string | undefined): string {
    if (!iso) return '—';
    return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
  }
</script>

<div class="sched-toolbar">
  <span class="sched-count">{schedules.length} schedule{schedules.length !== 1 ? 's' : ''}</span>
  <button class="btn-accent" onclick={() => showCreate = !showCreate} style="font-size:0.82rem; padding:8px 16px;">
    <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
    Create Schedule
  </button>
</div>

<!-- Create form -->
{#if showCreate}
  <div class="create-form">
    <div class="create-grid">
      <div>
        <label class="label">Name</label>
        <input class="input" type="text" placeholder="Daily Restart" bind:value={createName}>
      </div>
      <div>
        <label class="label">Type</label>
        <select class="select" bind:value={createType}>
          <option value="restart">Restart</option>
          <option value="backup">Backup</option>
          <option value="command">Command</option>
          <option value="update">Update Game</option>
        </select>
      </div>
      <div>
        <label class="label">Cron Expression</label>
        <input class="input input-mono" type="text" placeholder="0 3 * * *" bind:value={createCron}>
      </div>
      {#if createType === 'command'}
        <div>
          <label class="label">Command</label>
          <input class="input input-mono" type="text" placeholder="say Restarting in 5 minutes..." bind:value={createPayload}>
        </div>
      {/if}
    </div>
    <div class="create-actions">
      <button class="btn-solid" onclick={createSchedule} disabled={creating || !createName || !createCron} style="font-size:0.82rem; padding:8px 16px;">
        {creating ? 'Creating...' : 'Create'}
      </button>
      <button class="btn-accent" onclick={() => showCreate = false} style="font-size:0.82rem; padding:8px 16px;">Cancel</button>
    </div>
  </div>
{/if}

<div class="sched-panel">
  {#if loading}
    <div class="sched-row"><div class="sched-info"><span class="sched-name" style="color:var(--text-tertiary)">Loading...</span></div></div>
  {:else if schedules.length === 0}
    <div class="sched-row"><div class="sched-info"><span class="sched-name" style="color:var(--text-tertiary)">No schedules yet</span></div></div>
  {:else}
    {#each schedules as sched (sched.id)}
      <div class="sched-row" class:disabled={!sched.enabled}>
        <div class="sched-info">
          {#if editingId === sched.id}
            <div class="edit-grid">
              <input class="input" type="text" bind:value={editName} placeholder="Name">
              <input class="input input-mono" type="text" bind:value={editCron} placeholder="Cron">
              {#if sched.type === 'command'}
                <input class="input input-mono" type="text" bind:value={editPayload} placeholder="Command">
              {/if}
              <div class="edit-actions">
                <button class="btn-solid" onclick={() => saveEdit(sched)} style="font-size:0.72rem; padding:5px 12px;">Save</button>
                <button class="btn-accent" onclick={() => editingId = ''} style="font-size:0.72rem; padding:5px 12px;">Cancel</button>
              </div>
            </div>
          {:else}
            <div class="sched-name-row">
              <span class="sched-name">{sched.name}</span>
              <span class="type-badge {typeColors[sched.type] || ''}">{sched.type}</span>
            </div>
            <div class="sched-meta">
              <span class="sched-cron">{sched.cron_expr}</span>
              {#if sched.next_run}
                <span class="sched-meta-sep">·</span>
                <span>Next: {formatDate(sched.next_run)}</span>
              {/if}
            </div>
            {#if sched.type === 'command' && sched.payload}
              <div class="sched-payload">{sched.payload}</div>
            {/if}
          {/if}
        </div>
        <div class="sched-right">
          {#if editingId !== sched.id}
            <div class="sched-actions">
              <button class="sch-act" onclick={() => startEdit(sched)}>Edit</button>
              <button class="sch-act danger" onclick={() => deleteSchedule(sched)}>Delete</button>
            </div>
          {/if}
          <button class="toggle" class:on={sched.enabled} onclick={() => toggleEnabled(sched)}></button>
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

  .sched-toolbar {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 12px;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .sched-count { font-size: 0.78rem; color: var(--text-tertiary); font-family: var(--font-mono); }

  /* Create form */
  .create-form {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    padding: 18px;
    margin-bottom: 14px;
    animation: fade-up 0.3s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .create-grid {
    display: grid; grid-template-columns: 1fr 1fr;
    gap: 14px; margin-bottom: 14px;
  }
  .create-actions { display: flex; gap: 8px; }

  .sched-panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1) 0.05s both;
  }

  .sched-row {
    display: flex; align-items: center;
    padding: 14px 18px;
    gap: 14px;
    transition: background 0.12s;
    border-left: 2px solid transparent;
  }
  .sched-row:hover { background: var(--bg-elevated); }
  .sched-row + .sched-row { border-top: 1px solid var(--border-dim); }
  .sched-row.disabled { opacity: 0.5; }

  .sched-info { flex: 1; min-width: 0; }
  .sched-name-row { display: flex; align-items: center; gap: 8px; }
  .sched-name { font-size: 0.88rem; font-weight: 500; }

  .type-badge {
    display: inline-flex; align-items: center;
    padding: 2px 8px; border-radius: 4px;
    font-size: 0.65rem; font-family: var(--font-mono);
    font-weight: 500; text-transform: uppercase; letter-spacing: 0.04em;
  }
  .type-badge.restart { background: rgba(245,158,11,0.1); color: var(--caution); }
  .type-badge.backup { background: var(--live-dim); color: var(--live); }
  .type-badge.command { background: var(--accent-dim); color: var(--accent); }
  .type-badge.update { background: rgba(96,165,250,0.1); color: #60a5fa; }

  .sched-meta {
    display: flex; align-items: center; gap: 8px;
    margin-top: 4px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }
  .sched-meta-sep { opacity: 0.3; }
  .sched-cron { opacity: 0.6; }

  .sched-payload {
    margin-top: 4px;
    font-size: 0.7rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
    padding: 3px 8px;
    background: var(--bg-inset);
    border-radius: 3px;
    display: inline-block;
  }

  .sched-right {
    display: flex; align-items: center; gap: 10px;
    flex-shrink: 0;
  }

  .sched-actions {
    display: flex; gap: 2px;
    opacity: 0; transition: opacity 0.15s;
  }
  .sched-row:hover .sched-actions { opacity: 1; }

  .sch-act {
    padding: 4px 8px; border-radius: 3px;
    font-size: 0.68rem; font-family: var(--font-mono);
    color: var(--text-tertiary); background: none; border: none;
    cursor: pointer; transition: color 0.15s, background 0.15s;
  }
  .sch-act:hover { color: var(--accent); background: var(--accent-subtle); }
  .sch-act.danger:hover { color: var(--danger); background: rgba(239,68,68,0.06); }

  /* Edit inline */
  .edit-grid { display: flex; flex-direction: column; gap: 8px; }
  .edit-actions { display: flex; gap: 6px; margin-top: 4px; }

  @media (max-width: 700px) {
    .sched-toolbar { flex-direction: column; align-items: flex-start; gap: 10px; }
    .sched-actions { opacity: 1; }
    .create-grid { grid-template-columns: 1fr; }
  }
</style>
