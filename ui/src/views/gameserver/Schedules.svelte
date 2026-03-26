<script lang="ts">
  
  import { onMount } from 'svelte';
  import { api, type Schedule } from '$lib/api';
  import { gameserverStore, toast, confirm } from '$lib/stores';

  let { id }: { id: string } = $props();
  const gsId = id;

  // Read schedules from store — SSE keeps them updated
  const gsState = $derived(gameserverStore.getState(gsId));
  const schedules = $derived(gsState?.schedules ?? []);
  const loading = $derived(gsState?.schedules === null);

  // Create form
  let showCreate = $state(false);
  let createName = $state('');
  let createType = $state('restart');
  let createPayload = $state('');
  let createOneShot = $state(false);
  let creating = $state(false);

  // Schedule builder
  let frequency = $state<'interval' | 'daily' | 'weekly'>('daily');
  let intervalHours = $state(6);
  let dailyHour = $state(3);
  let dailyMinute = $state(0);
  let weeklyDay = $state(0); // 0=Sun
  let weeklyHour = $state(4);
  let weeklyMinute = $state(0);
  let advancedMode = $state(false);
  let advancedCron = $state('');

  // Edit form
  let editingId = $state('');
  let editName = $state('');
  let editPayload = $state('');

  const typeColors: Record<string, string> = {
    restart: 'restart',
    backup: 'backup',
    command: 'command',
    update: 'update',
  };

  const dayNames = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const dayNamesShort = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

  // Build cron from presets
  const generatedCron = $derived(() => {
    if (frequency === 'interval') {
      return `0 */${intervalHours} * * *`;
    }
    if (frequency === 'daily') {
      return `${dailyMinute} ${dailyHour} * * *`;
    }
    // weekly
    return `${weeklyMinute} ${weeklyHour} * * ${weeklyDay}`;
  });

  const activeCron = $derived(advancedMode ? advancedCron : generatedCron());

  // Human-readable description
  function describeCron(cron: string): string {
    if (!cron || !cron.trim()) return '';
    const parts = cron.trim().split(/\s+/);
    if (parts.length !== 5) return cron;

    const [min, hour, , , dow] = parts;

    // Every N hours: "0 */6 * * *"
    const intervalMatch = hour.match(/^\*\/(\d+)$/);
    if (intervalMatch && min === '0' && dow === '*') {
      const h = parseInt(intervalMatch[1]);
      return h === 1 ? 'Every hour' : `Every ${h} hours`;
    }

    // Every hour: "0 * * * *"
    if (hour === '*' && min === '0' && dow === '*') {
      return 'Every hour';
    }

    // Every N minutes: "*/N * * * *"
    const minInterval = min.match(/^\*\/(\d+)$/);
    if (minInterval && hour === '*' && dow === '*') {
      return `Every ${minInterval[1]} minutes`;
    }

    const h = parseInt(hour);
    const m = parseInt(min);
    if (isNaN(h) || isNaN(m)) return cron;

    const timeStr = formatTime12(h, m);

    // Weekly: specific dow
    if (dow !== '*') {
      const dayIdx = parseInt(dow);
      const dayName = dayNames[dayIdx] || dow;
      return `Every ${dayName} at ${timeStr}`;
    }

    // Daily
    return `Every day at ${timeStr}`;
  }

  function formatTime12(h: number, m: number): string {
    const period = h >= 12 ? 'PM' : 'AM';
    const h12 = h === 0 ? 12 : h > 12 ? h - 12 : h;
    return `${h12}:${m.toString().padStart(2, '0')} ${period}`;
  }

  // Try to parse a cron expression into preset values. Returns false if it doesn't match a preset.
  function parseCronToPreset(cron: string): boolean {
    const parts = cron.trim().split(/\s+/);
    if (parts.length !== 5) return false;
    const [min, hour, dom, mon, dow] = parts;
    if (dom !== '*' || mon !== '*') return false;

    // Interval: "0 */N * * *"
    const intervalMatch = hour.match(/^\*\/(\d+)$/);
    if (intervalMatch && min === '0' && dow === '*') {
      const h = parseInt(intervalMatch[1]);
      if ([1, 2, 4, 6, 8, 12].includes(h)) {
        frequency = 'interval';
        intervalHours = h;
        return true;
      }
    }

    // Hourly: "0 * * * *"
    if (hour === '*' && min === '0' && dow === '*') {
      frequency = 'interval';
      intervalHours = 1;
      return true;
    }

    const h = parseInt(hour);
    const m = parseInt(min);
    if (isNaN(h) || isNaN(m)) return false;

    // Weekly: "M H * * D"
    if (dow !== '*') {
      const d = parseInt(dow);
      if (!isNaN(d) && d >= 0 && d <= 6) {
        frequency = 'weekly';
        weeklyDay = d;
        weeklyHour = h;
        weeklyMinute = m;
        return true;
      }
    }

    // Daily: "M H * * *"
    if (dow === '*') {
      frequency = 'daily';
      dailyHour = h;
      dailyMinute = m;
      return true;
    }

    return false;
  }

  // Sync preset → advanced when toggling
  function toggleAdvanced() {
    if (!advancedMode) {
      advancedCron = generatedCron();
    }
    advancedMode = !advancedMode;
  }

  function resetForm() {
    createName = '';
    createType = 'restart';
    createPayload = '';
    createOneShot = false;
    frequency = 'daily';
    intervalHours = 6;
    dailyHour = 3;
    dailyMinute = 0;
    weeklyDay = 0;
    weeklyHour = 4;
    weeklyMinute = 0;
    advancedMode = false;
    advancedCron = '';
  }

  onMount(() => {
    if (gsState?.schedules === null) {
      gameserverStore.loadSchedules(gsId);
    }
  });

  async function createSchedule() {
    if (!createName || !activeCron) return;
    creating = true;
    try {
      await api.schedules.create(gsId, {
        name: createName,
        type: createType,
        cron_expr: activeCron,
        payload: createType === 'command' ? createPayload : '',
        enabled: true,
        one_shot: createOneShot,
      });
      // SSE will refresh the list via store
      showCreate = false;
      resetForm();
    } catch (e: any) {
      toast(`Failed to create schedule: ${e.message}`, 'error');
    } finally {
      creating = false;
    }
  }

  function startEdit(s: Schedule) {
    editingId = s.id;
    editName = s.name;
    editPayload = s.payload;
    // Try to parse cron into presets, fall back to advanced mode
    if (!parseCronToPreset(s.cron_expr)) {
      advancedMode = true;
      advancedCron = s.cron_expr;
    } else {
      advancedMode = false;
    }
  }

  async function saveEdit(s: Schedule) {
    const cron = activeCron;
    try {
      await api.schedules.update(gsId, s.id, {
        name: editName,
        cron_expr: cron,
        payload: s.type === 'command' ? editPayload : undefined,
      });
      // SSE will refresh the list via store
      editingId = '';
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    }
  }

  async function toggleEnabled(s: Schedule) {
    try {
      await api.schedules.update(gsId, s.id, { enabled: !s.enabled });
      // SSE will refresh the list via store
    } catch (e: any) {
      toast(`Failed to toggle: ${e.message}`, 'error');
    }
  }

  async function deleteSchedule(s: Schedule) {
    if (!await confirm({ title: 'Delete Schedule', message: `Delete schedule "${s.name}"?`, confirmLabel: 'Delete', danger: true })) return;
    try {
      await api.schedules.delete(gsId, s.id);
      // SSE will refresh the list via store
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
  <button class="btn-accent" onclick={() => { showCreate = !showCreate; if (!showCreate) resetForm(); }} style="font-size:0.82rem; padding:8px 16px;">
    <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
    Create Schedule
  </button>
</div>

<!-- Create form -->
{#if showCreate}
  <div class="create-form">
    <!-- Row 1: Name + Type -->
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
    </div>

    {#if createType === 'command'}
      <div style="margin-bottom:14px;">
        <label class="label">Command</label>
        <input class="input input-mono" type="text" placeholder="say Restarting in 5 minutes..." bind:value={createPayload}>
      </div>
    {/if}

    <!-- Schedule builder -->
    {#if !advancedMode}
      <div class="builder">
        <div class="builder-row">
          <label class="label">Frequency</label>
          <div class="freq-tabs">
            <button class="freq-tab" class:active={frequency === 'interval'} onclick={() => frequency = 'interval'}>Every X hours</button>
            <button class="freq-tab" class:active={frequency === 'daily'} onclick={() => frequency = 'daily'}>Daily</button>
            <button class="freq-tab" class:active={frequency === 'weekly'} onclick={() => frequency = 'weekly'}>Weekly</button>
          </div>
        </div>

        {#if frequency === 'interval'}
          <div class="builder-row">
            <label class="label">Every</label>
            <div class="interval-pills">
              {#each [1, 2, 4, 6, 8, 12] as h}
                <button class="pill" class:active={intervalHours === h} onclick={() => intervalHours = h}>{h}h</button>
              {/each}
            </div>
          </div>
        {/if}

        {#if frequency === 'daily'}
          <div class="builder-row">
            <label class="label">Time</label>
            <div class="time-pick">
              <select class="select time-sel" bind:value={dailyHour}>
                {#each Array(24) as _, h}
                  <option value={h}>{formatTime12(h, 0).split(':')[0]} {h >= 12 ? 'PM' : 'AM'}</option>
                {/each}
              </select>
              <span class="time-colon">:</span>
              <select class="select time-sel" bind:value={dailyMinute}>
                {#each [0, 15, 30, 45] as m}
                  <option value={m}>{m.toString().padStart(2, '0')}</option>
                {/each}
              </select>
            </div>
          </div>
        {/if}

        {#if frequency === 'weekly'}
          <div class="builder-row">
            <label class="label">Day</label>
            <div class="day-pills">
              {#each dayNamesShort as name, i}
                <button class="pill" class:active={weeklyDay === i} onclick={() => weeklyDay = i}>{name}</button>
              {/each}
            </div>
          </div>
          <div class="builder-row">
            <label class="label">Time</label>
            <div class="time-pick">
              <select class="select time-sel" bind:value={weeklyHour}>
                {#each Array(24) as _, h}
                  <option value={h}>{formatTime12(h, 0).split(':')[0]} {h >= 12 ? 'PM' : 'AM'}</option>
                {/each}
              </select>
              <span class="time-colon">:</span>
              <select class="select time-sel" bind:value={weeklyMinute}>
                {#each [0, 15, 30, 45] as m}
                  <option value={m}>{m.toString().padStart(2, '0')}</option>
                {/each}
              </select>
            </div>
          </div>
        {/if}
      </div>
    {:else}
      <div style="margin-bottom:14px;">
        <label class="label">Cron Expression</label>
        <input class="input input-mono" type="text" placeholder="0 3 * * *" bind:value={advancedCron}>
      </div>
    {/if}

    <!-- Preview + Advanced toggle -->
    <div class="preview-row">
      <div class="preview">
        <span class="preview-icon">
          <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M8 3.5a.5.5 0 0 0-1 0V8a.5.5 0 0 0 .252.434l3.5 2a.5.5 0 0 0 .496-.868L8 7.71V3.5z"/><path d="M8 16A8 8 0 1 0 8 0a8 8 0 0 0 0 16zm7-8A7 7 0 1 1 1 8a7 7 0 0 1 14 0z"/></svg>
        </span>
        <span class="preview-text">{describeCron(activeCron) || 'Configure a schedule above'}</span>
        {#if activeCron}
          <span class="preview-cron">{activeCron}</span>
        {/if}
      </div>
      <button class="adv-toggle" onclick={toggleAdvanced}>
        {advancedMode ? 'Use presets' : 'Advanced'}
      </button>
    </div>

    <div class="create-footer">
      <div class="oneshot-toggle">
        <button class="toggle" class:on={createOneShot} onclick={() => createOneShot = !createOneShot}></button>
        <span class="oneshot-label">Run once</span>
        {#if createOneShot}
          <span class="oneshot-hint">Disables automatically after first run</span>
        {/if}
      </div>
      <div class="create-actions">
        <button class="btn-accent" onclick={() => { showCreate = false; resetForm(); }} style="font-size:0.82rem; padding:8px 16px;">Cancel</button>
        <button class="btn-solid" onclick={createSchedule} disabled={creating || !createName || !activeCron} style="font-size:0.82rem; padding:8px 16px;">
          {creating ? 'Creating...' : 'Create'}
        </button>
      </div>
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
            <div class="edit-form">
              <div class="edit-top">
                <div>
                  <label class="label">Name</label>
                  <input class="input" type="text" bind:value={editName} placeholder="Name">
                </div>
                {#if sched.type === 'command'}
                  <div>
                    <label class="label">Command</label>
                    <input class="input input-mono" type="text" bind:value={editPayload} placeholder="Command">
                  </div>
                {/if}
              </div>

              {#if !advancedMode}
                <div class="builder">
                  <div class="builder-row">
                    <label class="label">Frequency</label>
                    <div class="freq-tabs">
                      <button class="freq-tab" class:active={frequency === 'interval'} onclick={() => frequency = 'interval'}>Every X hours</button>
                      <button class="freq-tab" class:active={frequency === 'daily'} onclick={() => frequency = 'daily'}>Daily</button>
                      <button class="freq-tab" class:active={frequency === 'weekly'} onclick={() => frequency = 'weekly'}>Weekly</button>
                    </div>
                  </div>
                  {#if frequency === 'interval'}
                    <div class="builder-row">
                      <label class="label">Every</label>
                      <div class="interval-pills">
                        {#each [1, 2, 4, 6, 8, 12] as h}
                          <button class="pill" class:active={intervalHours === h} onclick={() => intervalHours = h}>{h}h</button>
                        {/each}
                      </div>
                    </div>
                  {/if}
                  {#if frequency === 'daily'}
                    <div class="builder-row">
                      <label class="label">Time</label>
                      <div class="time-pick">
                        <select class="select time-sel" bind:value={dailyHour}>
                          {#each Array(24) as _, h}
                            <option value={h}>{formatTime12(h, 0).split(':')[0]} {h >= 12 ? 'PM' : 'AM'}</option>
                          {/each}
                        </select>
                        <span class="time-colon">:</span>
                        <select class="select time-sel" bind:value={dailyMinute}>
                          {#each [0, 15, 30, 45] as m}
                            <option value={m}>{m.toString().padStart(2, '0')}</option>
                          {/each}
                        </select>
                      </div>
                    </div>
                  {/if}
                  {#if frequency === 'weekly'}
                    <div class="builder-row">
                      <label class="label">Day</label>
                      <div class="day-pills">
                        {#each dayNamesShort as name, i}
                          <button class="pill" class:active={weeklyDay === i} onclick={() => weeklyDay = i}>{name}</button>
                        {/each}
                      </div>
                    </div>
                    <div class="builder-row">
                      <label class="label">Time</label>
                      <div class="time-pick">
                        <select class="select time-sel" bind:value={weeklyHour}>
                          {#each Array(24) as _, h}
                            <option value={h}>{formatTime12(h, 0).split(':')[0]} {h >= 12 ? 'PM' : 'AM'}</option>
                          {/each}
                        </select>
                        <span class="time-colon">:</span>
                        <select class="select time-sel" bind:value={weeklyMinute}>
                          {#each [0, 15, 30, 45] as m}
                            <option value={m}>{m.toString().padStart(2, '0')}</option>
                          {/each}
                        </select>
                      </div>
                    </div>
                  {/if}
                </div>
              {:else}
                <div style="margin-bottom:10px;">
                  <label class="label">Cron Expression</label>
                  <input class="input input-mono" type="text" placeholder="0 3 * * *" bind:value={advancedCron}>
                </div>
              {/if}

              <div class="preview-row">
                <div class="preview">
                  <span class="preview-icon">
                    <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M8 3.5a.5.5 0 0 0-1 0V8a.5.5 0 0 0 .252.434l3.5 2a.5.5 0 0 0 .496-.868L8 7.71V3.5z"/><path d="M8 16A8 8 0 1 0 8 0a8 8 0 0 0 0 16zm7-8A7 7 0 1 1 1 8a7 7 0 0 1 14 0z"/></svg>
                  </span>
                  <span class="preview-text">{describeCron(activeCron)}</span>
                  <span class="preview-cron">{activeCron}</span>
                </div>
                <button class="adv-toggle" onclick={toggleAdvanced}>
                  {advancedMode ? 'Use presets' : 'Advanced'}
                </button>
              </div>

              <div class="edit-actions">
                <button class="btn-solid" onclick={() => saveEdit(sched)} style="font-size:0.72rem; padding:5px 12px;">Save</button>
                <button class="btn-accent" onclick={() => editingId = ''} style="font-size:0.72rem; padding:5px 12px;">Cancel</button>
              </div>
            </div>
          {:else}
            <div class="sched-name-row">
              <span class="sched-name">{sched.name}</span>
              <span class="type-badge {typeColors[sched.type] || ''}">{sched.type}</span>
              {#if sched.one_shot}
                <span class="once-badge">once</span>
              {/if}
            </div>
            <div class="sched-meta">
              <span class="sched-human">{describeCron(sched.cron_expr)}</span>
              <span class="sched-meta-sep">·</span>
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
  .create-footer {
    display: flex; align-items: center; justify-content: space-between;
  }
  .create-actions { display: flex; gap: 8px; }
  .oneshot-toggle { display: flex; align-items: center; gap: 8px; }
  .oneshot-label { font-size: 0.82rem; font-weight: 450; color: var(--text-secondary); }
  .oneshot-hint { font-size: 0.7rem; color: var(--text-tertiary); font-family: var(--font-mono); }

  /* Schedule builder */
  .builder {
    background: var(--bg-elevated);
    border: 1px solid var(--border-dim);
    border-left: 2px solid rgba(232, 114, 42, 0.2);
    border-radius: var(--radius);
    padding: 16px;
    margin-bottom: 14px;
  }
  .builder-row { margin-bottom: 12px; }
  .builder-row:last-child { margin-bottom: 0; }

  /* Frequency tabs */
  .freq-tabs {
    display: flex; gap: 4px;
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    padding: 3px;
  }
  .freq-tab {
    flex: 1;
    padding: 7px 12px;
    border-radius: 4px;
    border: none; background: none;
    font-family: var(--font-body); font-size: 0.78rem; font-weight: 450;
    color: var(--text-tertiary); cursor: pointer;
    transition: all 0.15s;
  }
  .freq-tab:hover { color: var(--text-secondary); }
  .freq-tab.active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: 0 1px 3px rgba(0,0,0,0.2);
  }

  /* Interval pills */
  .interval-pills, .day-pills {
    display: flex; gap: 4px; flex-wrap: wrap;
  }
  .pill {
    padding: 6px 14px; border-radius: var(--radius-sm);
    border: 1px solid var(--border-dim); background: none;
    font-family: var(--font-mono); font-size: 0.78rem;
    color: var(--text-tertiary); cursor: pointer;
    transition: all 0.15s;
  }
  .pill:hover { border-color: var(--border); color: var(--text-secondary); }
  .pill.active {
    border-color: var(--accent-border);
    background: var(--accent-dim);
    color: var(--accent);
  }

  /* Time picker */
  .time-pick {
    display: flex; align-items: center; gap: 6px;
  }
  .time-sel { width: auto; min-width: 100px; }
  .time-colon {
    font-size: 1rem; font-weight: 600;
    color: var(--text-tertiary);
  }

  /* Preview row */
  .preview-row {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 14px; gap: 12px;
  }
  .preview {
    display: flex; align-items: center; gap: 8px;
    padding: 8px 12px;
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    flex: 1; min-width: 0;
  }
  .preview-icon { color: var(--accent); opacity: 0.6; display: flex; flex-shrink: 0; }
  .preview-text {
    font-size: 0.82rem; font-weight: 450;
    color: var(--text-secondary);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .preview-cron {
    font-size: 0.68rem; font-family: var(--font-mono);
    color: var(--text-tertiary); opacity: 0.6;
    margin-left: auto; flex-shrink: 0;
  }
  .adv-toggle {
    padding: 6px 12px; border-radius: var(--radius-sm);
    border: 1px solid var(--border-dim); background: none;
    font-family: var(--font-mono); font-size: 0.7rem;
    color: var(--text-tertiary); cursor: pointer;
    transition: color 0.15s, border-color 0.15s;
    flex-shrink: 0;
  }
  .adv-toggle:hover { color: var(--accent); border-color: var(--accent-border); }

  /* Schedule list */
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

  .once-badge {
    display: inline-flex; align-items: center;
    padding: 2px 7px; border-radius: 4px;
    font-size: 0.6rem; font-family: var(--font-mono);
    font-weight: 500; text-transform: uppercase; letter-spacing: 0.04em;
    background: rgba(82,82,74,0.12); color: var(--idle);
    border: 1px solid rgba(82,82,74,0.15);
  }

  .sched-meta {
    display: flex; align-items: center; gap: 8px;
    margin-top: 4px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }
  .sched-meta-sep { opacity: 0.3; }
  .sched-cron { opacity: 0.5; }
  .sched-human { color: var(--text-secondary); }

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
  .edit-form { display: flex; flex-direction: column; gap: 10px; width: 100%; }
  .edit-top { display: flex; gap: 12px; }
  .edit-top > div { flex: 1; }
  .edit-actions { display: flex; gap: 6px; }

  @media (max-width: 700px) {
    .sched-toolbar { flex-direction: column; align-items: flex-start; gap: 10px; }
    .sched-actions { opacity: 1; }
    .create-grid { grid-template-columns: 1fr; }
    .day-pills { gap: 3px; }
    .pill { padding: 6px 10px; }
    .preview-row { flex-direction: column; align-items: stretch; }
  }
</style>
