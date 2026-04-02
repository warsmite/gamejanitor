<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type WorkerView } from '$lib/api';
  import { gameserverStore, toast, confirm } from '$lib/stores';
  import { StatusPill } from '$lib/components';

  let workers = $state<WorkerView[]>([]);
  let loading = $state(true);
  let migrateTarget = $state<{ gsId: string; gsName: string } | null>(null);
  let migrating = $state(false);
  let bulkRunning = $state(false);

  const gameservers = $derived(gameserverStore.list);
  const onlineWorkers = $derived(workers.filter(w => w.status === 'online'));

  onMount(async () => {
    try {
      workers = await api.workers.list();
    } catch (e: any) {
      console.warn('Cluster: failed to load workers', e);
      toast('Failed to load workers', 'error');
    } finally {
      loading = false;
    }
  });

  function capacityPercent(allocated: number, max: number | undefined | null): number {
    if (!max || max <= 0) return 0;
    return Math.min(100, Math.round((allocated / max) * 100));
  }

  function barClass(pct: number): string {
    if (pct >= 90) return 'bar-danger';
    if (pct >= 80) return 'bar-caution';
    return 'bar-normal';
  }

  function formatMB(mb: number): string {
    if (mb >= 1024) return `${(mb / 1024).toFixed(mb % 1024 === 0 ? 0 : 1)} GB`;
    return `${mb} MB`;
  }

  function openMigrate(gsId: string, gsName: string) {
    migrateTarget = { gsId, gsName };
  }

  async function doMigrate(targetNodeId: string) {
    if (!migrateTarget) return;
    const target = migrateTarget;
    migrating = true;
    try {
      await api.gameservers.migrate(target.gsId, targetNodeId);
      toast(`Migrating "${target.gsName}" to ${targetNodeId}`, 'info');
      migrateTarget = null;
      workers = await api.workers.list();
    } catch (e: any) {
      toast(`Migration failed: ${e.message}`, 'error');
      migrateTarget = null;
    } finally {
      migrating = false;
    }
  }

  async function bulkAction(action: 'stop' | 'restart') {
    const label = action === 'stop' ? 'Stop' : 'Restart';
    const running = gameservers.filter(gs => gs.status === 'running');
    if (running.length === 0) {
      toast('No running gameservers', 'info');
      return;
    }
    if (!await confirm({
      title: `${label} All Gameservers`,
      message: `${label} ${running.length} running gameserver${running.length !== 1 ? 's' : ''}? This affects all nodes.`,
      confirmLabel: label,
      danger: action === 'stop',
    })) return;

    bulkRunning = true;
    try {
      await api.gameservers.bulk(action, []);
      toast(`${label} all initiated`, 'info');
    } catch (e: any) {
      toast(`Bulk ${action} failed: ${e.message}`, 'error');
    } finally {
      bulkRunning = false;
    }
  }
</script>

{#if loading}
  <main>
    <p class="loading-text">Loading cluster...</p>
  </main>
{:else}
  <main>
    <div class="page-header">
      <h1>Cluster</h1>
      <div class="header-actions">
        <button class="btn-action-bulk" onclick={() => bulkAction('restart')} disabled={bulkRunning}>
          <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
          Restart All
        </button>
        <button class="btn-action-bulk danger" onclick={() => bulkAction('stop')} disabled={bulkRunning}>
          <svg viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
          Stop All
        </button>
      </div>
    </div>

    <!-- Worker Nodes -->
    <section class="section">
      <div class="section-label">Worker Nodes</div>
      <div class="worker-grid">
        {#each workers as w (w.id)}
          {@const memPct = capacityPercent(w.allocated_memory_mb, w.max_memory_mb)}
          {@const cpuPct = capacityPercent(w.allocated_cpu, w.max_cpu)}
          {@const storagePct = capacityPercent(0, w.max_storage_mb)}
          <div class="worker-card" class:offline={w.status !== 'online'} class:cordoned={w.cordoned}>
            <div class="wc-header">
              <div class="wc-name">{w.id}</div>
              <div class="wc-status">
                <span class="status-dot" class:online={w.status === 'online'}></span>
                <span class="status-label">{w.status}</span>
              </div>
            </div>

            <div class="wc-meta">
              {#if w.lan_ip}<span class="meta-item"><span class="meta-key">LAN</span> {w.lan_ip}</span>{/if}
              {#if w.external_ip}<span class="meta-item"><span class="meta-key">EXT</span> {w.external_ip}</span>{/if}
            </div>

            <div class="wc-resources">
              {#if w.max_memory_mb}
                <div class="resource-row">
                  <div class="resource-label">
                    <span>Memory</span>
                    <span class="resource-value">{formatMB(w.allocated_memory_mb)} / {formatMB(w.max_memory_mb)}</span>
                  </div>
                  <div class="bar-track">
                    <div class="bar-fill {barClass(memPct)}" style="width:{memPct}%"></div>
                  </div>
                </div>
              {/if}
              {#if w.max_cpu}
                <div class="resource-row">
                  <div class="resource-label">
                    <span>CPU</span>
                    <span class="resource-value">{w.allocated_cpu.toFixed(1)} / {w.max_cpu} cores</span>
                  </div>
                  <div class="bar-track">
                    <div class="bar-fill {barClass(cpuPct)}" style="width:{cpuPct}%"></div>
                  </div>
                </div>
              {/if}
              {#if w.max_storage_mb}
                <div class="resource-row">
                  <div class="resource-label">
                    <span>Storage</span>
                    <span class="resource-value">{formatMB(w.max_storage_mb)}</span>
                  </div>
                  <div class="bar-track">
                    <div class="bar-fill {barClass(storagePct)}" style="width:{storagePct}%"></div>
                  </div>
                </div>
              {/if}
            </div>

            <div class="wc-footer">
              <span class="gs-count">{w.gameserver_count} server{w.gameserver_count !== 1 ? 's' : ''}</span>
              {#if w.cordoned}
                <span class="cordon-badge">Cordoned</span>
              {/if}
            </div>
          </div>
        {:else}
          <div class="empty-state">No workers connected.</div>
        {/each}
      </div>
    </section>

    <!-- Gameserver Placement -->
    <section class="section">
      <div class="section-label">Gameserver Placement</div>
      {#if gameservers.length === 0}
        <div class="empty-state">No gameservers.</div>
      {:else}
        <div class="placement-table-wrap">
          <table class="placement-table">
            <thead>
              <tr>
                <th>Server</th>
                <th>Game</th>
                <th>Status</th>
                <th>Node</th>
                <th>Memory</th>
                <th>CPU</th>
                <th class="col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {#each gameservers as gs (gs.id)}
                {@const game = gameserverStore.gameFor(gs.game_id)}
                <tr>
                  <td class="cell-name">{gs.name}</td>
                  <td class="cell-game">{game?.name || gs.game_id}</td>
                  <td><StatusPill status={gs.status} /></td>
                  <td class="cell-node">{gs.node_id || '—'}</td>
                  <td class="cell-mono">{gs.memory_limit_mb ? formatMB(gs.memory_limit_mb) : '—'}</td>
                  <td class="cell-mono">{gs.cpu_limit ? `${gs.cpu_limit} cores` : '—'}</td>
                  <td class="col-actions">
                    {#if onlineWorkers.length > 1 && gs.status !== 'stopped'}
                      <button class="btn-migrate" onclick={() => openMigrate(gs.id, gs.name)} disabled={migrating}>
                        Migrate
                      </button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  </main>
{/if}

<!-- Migrate Modal -->
{#if migrateTarget}
  <div class="modal-overlay" onclick={() => migrateTarget = null} onkeydown={(e) => e.key === 'Escape' && (migrateTarget = null)} role="dialog" aria-modal="true" tabindex="-1">
    <div class="modal" onclick={(e) => e.stopPropagation()}>
      <div class="modal-title">Migrate "{migrateTarget.gsName}"</div>
      <p class="modal-desc">Select a target node. The gameserver will be stopped, moved, and restarted.</p>
      <div class="node-picker">
        {#each onlineWorkers.filter(w => {
          const gs = gameserverStore.get(migrateTarget?.gsId || '');
          return gs && w.id !== gs.node_id && !w.cordoned;
        }) as w (w.id)}
          {@const memPct = capacityPercent(w.allocated_memory_mb, w.max_memory_mb)}
          <button class="node-option" onclick={() => doMigrate(w.id)} disabled={migrating}>
            <div class="node-option-name">{w.id}</div>
            <div class="node-option-capacity">
              <span>{formatMB(w.allocated_memory_mb)} / {w.max_memory_mb ? formatMB(w.max_memory_mb) : '∞'}</span>
              <span class="node-option-pct">{memPct}%</span>
            </div>
          </button>
        {:else}
          <div class="empty-state" style="padding:20px;">No available target nodes.</div>
        {/each}
      </div>
      <div class="modal-actions">
        <button class="btn-cancel" onclick={() => migrateTarget = null}>Cancel</button>
      </div>
    </div>
  </div>
{/if}

<style>
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .page-header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 24px;
  }
  .page-header h1 { font-size: 1.3rem; font-weight: 600; }
  .header-actions { display: flex; gap: 8px; }

  .btn-action-bulk {
    display: inline-flex; align-items: center; gap: 5px;
    padding: 7px 14px; border-radius: var(--radius-sm);
    background: transparent; border: 1px solid var(--border-dim);
    color: var(--text-secondary); font-family: var(--font-body);
    font-size: 0.78rem; cursor: pointer; transition: all 0.2s;
  }
  .btn-action-bulk:hover { border-color: var(--border); color: var(--text-primary); }
  .btn-action-bulk.danger:hover { border-color: rgba(239,68,68,0.4); color: var(--danger); }
  .btn-action-bulk:disabled { opacity: 0.4; pointer-events: none; }
  .btn-action-bulk svg { width: 13px; height: 13px; }

  .section { margin-bottom: 32px; animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1); }
  .section-label {
    font-size: 0.82rem; font-weight: 550;
    color: var(--text-secondary); margin-bottom: 14px;
  }

  /* Worker Cards */
  .worker-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 14px; }

  .worker-card {
    background: var(--bg-surface); border: 1px solid var(--border-subtle);
    border-radius: var(--radius); padding: 20px;
    position: relative; overflow: hidden;
    transition: border-color 0.2s;
  }
  .worker-card::before {
    content: ''; position: absolute; top: 0; left: 0; right: 0; height: 2px;
    background: var(--live); opacity: 0.6;
  }
  .worker-card.offline::before { background: var(--danger); }
  .worker-card.cordoned::before { background: var(--caution, #f59e0b); }
  .worker-card.offline { opacity: 0.6; }

  .wc-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
  .wc-name { font-size: 1.05rem; font-weight: 600; font-family: var(--font-mono); }
  .wc-status { display: flex; align-items: center; gap: 6px; font-size: 0.72rem; color: var(--text-tertiary); }
  .status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--danger); }
  .status-dot.online { background: var(--live); }
  .status-label { font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.05em; }

  .wc-meta { display: flex; gap: 16px; margin-bottom: 16px; }
  .meta-item { font-size: 0.75rem; font-family: var(--font-mono); color: var(--text-tertiary); }
  .meta-key { color: var(--text-tertiary); opacity: 0.5; margin-right: 4px; font-size: 0.65rem; text-transform: uppercase; letter-spacing: 0.05em; }

  .wc-resources { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
  .resource-row { display: flex; flex-direction: column; gap: 4px; }
  .resource-label { display: flex; justify-content: space-between; font-size: 0.72rem; color: var(--text-tertiary); }
  .resource-value { font-family: var(--font-mono); font-size: 0.68rem; }

  .bar-track { height: 6px; background: var(--bg-inset); border-radius: 3px; overflow: hidden; }
  .bar-fill { height: 100%; border-radius: 3px; transition: width 0.6s cubic-bezier(0.16, 1, 0.3, 1); }
  .bar-normal { background: var(--accent); }
  .bar-caution { background: #f59e0b; }
  .bar-danger { background: var(--danger); }

  .wc-footer { display: flex; align-items: center; gap: 10px; padding-top: 12px; border-top: 1px solid var(--border-dim); }
  .gs-count { font-size: 0.74rem; font-family: var(--font-mono); color: var(--text-tertiary); }
  .cordon-badge {
    font-size: 0.62rem; font-family: var(--font-mono); text-transform: uppercase;
    letter-spacing: 0.08em; color: #f59e0b; background: rgba(245,158,11,0.08);
    padding: 2px 8px; border-radius: 3px; border: 1px solid rgba(245,158,11,0.2);
  }

  /* Placement Table */
  .placement-table-wrap {
    background: var(--bg-surface); border: 1px solid var(--border-subtle);
    border-radius: var(--radius); overflow: hidden;
  }
  .placement-table { width: 100%; border-collapse: collapse; }
  .placement-table th {
    text-align: left; padding: 10px 16px;
    font-size: 0.65rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--text-tertiary); border-bottom: 1px solid var(--border-dim);
    background: var(--bg-elevated);
  }
  .placement-table td {
    padding: 12px 16px; font-size: 0.82rem;
    border-bottom: 1px solid var(--border-dim);
    color: var(--text-secondary);
  }
  .placement-table tbody tr:last-child td { border-bottom: none; }
  .placement-table tbody tr:hover { background: rgba(232,114,42,0.02); }

  .cell-name { font-weight: 500; color: var(--text-primary); }
  .cell-game { color: var(--text-tertiary); font-size: 0.78rem; }
  .cell-node { font-family: var(--font-mono); font-size: 0.78rem; }
  .cell-mono { font-family: var(--font-mono); font-size: 0.76rem; color: var(--text-tertiary); }
  .col-actions { width: 80px; text-align: right; }

  .btn-migrate {
    padding: 4px 12px; border-radius: var(--radius-sm);
    background: transparent; border: 1px solid var(--border-dim);
    color: var(--text-tertiary); font-size: 0.72rem; font-family: var(--font-mono);
    cursor: pointer; transition: all 0.15s;
  }
  .btn-migrate:hover { border-color: var(--accent-border); color: var(--accent); }
  .btn-migrate:disabled { opacity: 0.3; pointer-events: none; }

  /* Modal */
  .modal-overlay {
    position: fixed; inset: 0; background: rgba(0,0,0,0.6);
    display: grid; place-items: center; z-index: 100;
    animation: fade-in 0.15s ease;
  }
  @keyframes fade-in { from { opacity: 0; } to { opacity: 1; } }
  .modal {
    background: var(--bg-surface); border: 1px solid var(--border);
    border-radius: var(--radius); padding: 24px; width: 420px; max-width: 90vw;
    animation: fade-up 0.2s ease;
  }
  .modal-title { font-size: 1rem; font-weight: 600; margin-bottom: 6px; }
  .modal-desc { font-size: 0.8rem; color: var(--text-tertiary); margin-bottom: 18px; line-height: 1.4; }
  .modal-actions { display: flex; justify-content: flex-end; margin-top: 16px; }

  .node-picker { display: flex; flex-direction: column; gap: 8px; }
  .node-option {
    display: flex; align-items: center; justify-content: space-between;
    padding: 12px 16px; border-radius: var(--radius-sm);
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    cursor: pointer; transition: all 0.15s; text-align: left; width: 100%;
  }
  .node-option:hover { border-color: var(--accent-border); background: var(--bg-elevated); }
  .node-option:disabled { opacity: 0.4; pointer-events: none; }
  .node-option-name { font-weight: 500; font-family: var(--font-mono); font-size: 0.88rem; }
  .node-option-capacity { font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); display: flex; gap: 8px; }
  .node-option-pct { color: var(--accent); }

  .btn-cancel {
    padding: 7px 16px; border-radius: var(--radius-sm);
    background: transparent; border: 1px solid var(--border-dim);
    color: var(--text-tertiary); font-size: 0.8rem; cursor: pointer;
    transition: all 0.15s;
  }
  .btn-cancel:hover { border-color: var(--border); color: var(--text-secondary); }

  .loading-text { color: var(--text-tertiary); font-size: 0.85rem; padding: 40px 0; text-align: center; }
  .empty-state { color: var(--text-tertiary); font-size: 0.82rem; padding: 30px; text-align: center; }

  @media (max-width: 700px) {
    .worker-grid { grid-template-columns: 1fr; }
    .page-header { flex-direction: column; align-items: flex-start; gap: 12px; }
    .placement-table-wrap { overflow-x: auto; }
  }
</style>
