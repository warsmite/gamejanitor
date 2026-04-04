<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type Event } from '$lib/api';
  import { gameserverStore, toast } from '$lib/stores';
  import { onGameserverEvent } from '$lib/stores/sse';
  import { CopyBlock, StatsChart } from '$lib/components';
  import { embedded } from '$lib/base';

  let { id }: { id: string } = $props();

  const can = (p: string) => gameserverStore.canOnGameserver(p, id);
  const gsState = $derived(gameserverStore.getState(id));
  const gameserver = $derived(gsState?.gameserver ?? null);
  const query = $derived(gsState?.query ?? null);
  const isRunning = $derived(gameserverStore.isRunning(id));
  const sftpAddr = $derived(gameserverStore.sftpAddress(id));
  const connIP = $derived(gameserverStore.connectionIP(id));

  // Additional ports beyond the primary game port (rcon, query if different, etc.)
  const extraPorts = $derived(() => {
    const ports = gameserver?.ports || [];
    const gamePort = ports.find((p: any) => p.name === 'game') || ports[0];
    if (!connIP || !gamePort) return [];
    return ports
      .filter((p: any) => p !== gamePort && p.host_port !== gamePort.host_port)
      .map((p: any) => ({
        label: p.name.charAt(0).toUpperCase() + p.name.slice(1),
        value: `${connIP}:${p.host_port}`,
      }));
  });

  // SFTP password regen
  let sftpPassword = $state('');
  let regenerating = $state(false);

  async function regenerateSftpPassword() {
    regenerating = true;
    try {
      const result = await api.gameservers.regenerateSftpPassword(id);
      sftpPassword = result.sftp_password;
      toast('SFTP password regenerated', 'success');
    } catch (e: any) {
      toast(`Failed: ${e.message}`, 'error');
    } finally {
      regenerating = false;
    }
  }

  // Activity feed — page-specific state
  let events = $state<Event[]>([]);
  let unsub: (() => void) | null = null;

  // Events worth showing in the activity feed — keyed by type with label and dot color.
  // Anything not in this map is filtered out (status_changed, stats, query, operation,
  // intermediate lifecycle steps like image_pulling, instance_creating, etc.)
  const activityEvents: Record<string, { label: string; color: string }> = {
    // User actions
    'gameserver.create':     { label: 'Created',                  color: 'green' },
    'gameserver.start':      { label: 'Started',                  color: 'green' },
    'gameserver.stop':       { label: 'Stopped',                  color: 'gray' },
    'gameserver.restart':    { label: 'Restarted',                color: 'orange' },
    'gameserver.update_game':{ label: 'Game updated',             color: 'orange' },
    'gameserver.reinstall':  { label: 'Reinstalled',              color: 'orange' },
    'gameserver.migrate':    { label: 'Migrated',                 color: 'orange' },
    'gameserver.update':     { label: 'Settings changed',         color: 'gray' },
    // Activity table short names
    'start':                 { label: 'Started',                  color: 'green' },
    'stop':                  { label: 'Stopped',                  color: 'gray' },
    'restart':               { label: 'Restarted',                color: 'orange' },
    'update_game':           { label: 'Game updated',             color: 'orange' },
    'reinstall':             { label: 'Reinstalled',              color: 'orange' },
    'migrate':               { label: 'Migrated',                 color: 'orange' },
    'create_backup':         { label: 'Backup started',           color: 'orange' },
    'restore_backup':        { label: 'Restore started',          color: 'orange' },
    // Lifecycle milestones
    'gameserver.ready':      { label: 'Ready — accepting players', color: 'green' },
    'gameserver.instance_exited': { label: 'Crashed',            color: 'red' },
    'gameserver.error':      { label: 'Error',                    color: 'red' },
    // Backups
    'backup.create':         { label: 'Backup started',           color: 'orange' },
    'backup.completed':      { label: 'Backup completed',         color: 'green' },
    'backup.failed':         { label: 'Backup failed',            color: 'red' },
    'backup.restore':        { label: 'Restore started',          color: 'orange' },
    'backup.restore.completed': { label: 'Restore completed',     color: 'green' },
    'backup.restore.failed': { label: 'Restore failed',           color: 'red' },
    'backup.delete':         { label: 'Backup deleted',           color: 'gray' },
    // Schedules
    'schedule.create':       { label: 'Schedule created',         color: 'gray' },
    'schedule.update':       { label: 'Schedule updated',         color: 'gray' },
    'schedule.delete':       { label: 'Schedule deleted',         color: 'gray' },
    'schedule.task.completed': { label: 'Scheduled task ran',     color: 'green' },
    'schedule.task.failed':  { label: 'Scheduled task failed',    color: 'red' },
    // Mods
    'mod.installed':         { label: 'Mod installed',            color: 'green' },
    'mod.uninstalled':       { label: 'Mod removed',              color: 'gray' },
  };

  function isRelevantEvent(type: string): boolean {
    return type in activityEvents;
  }

  onMount(async () => {
    try {
      const allEvents = await api.events.history({ gameserver_id: id, limit: 200 });
      events = allEvents.filter(e => isRelevantEvent(e.type)).slice(0, 20);
    } catch (e) { console.warn('Overview: failed to load events', e); }

    unsub = onGameserverEvent(id, (data: any) => {
      if (!isRelevantEvent(data.type)) return;

      const event: Event = {
        id: crypto.randomUUID(),
        gameserver_id: id,
        worker_id: '',
        type: data.type || 'unknown',
        actor: data.actor,
        data: data,
        created_at: new Date().toISOString(),
      };
      events = [event, ...events.slice(0, 19)];
    });
  });

  onDestroy(() => {
    unsub?.();
  });

  function eventLabel(type: string, data?: any): string {
    if (type === 'gameserver.error' && data?.reason) return `Error: ${data.reason}`;
    return activityEvents[type]?.label || type.replace(/\./g, ' ');
  }

  function eventDotColor(type: string): string {
    return activityEvents[type]?.color || 'gray';
  }

  function timeAgo(iso: string): string {
    const diff = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  function formatTime(iso: string): string {
    return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }
</script>

{#if gameserver}
  <div class="overview">

    <!-- Connection info — full width -->
    <div class="panel full-width" style="padding: 0;">
      <div class="connect-row">
        <CopyBlock label="Connect" value={gameserverStore.connectionAddress(id)} primary={true} />
        {#each extraPorts() as port}
          <CopyBlock label={port.label} value={port.value} />
        {/each}
      </div>
    </div>

    <!-- Query data -->
    <div class="panel">
      <div class="panel-title">Server Info</div>
      <div class="query-content">
        <div class="query-row">
          <span class="query-key">Players</span>
          <span class="query-val" style="color: var(--live);">
            {query ? `${query.players_online} / ${query.max_players}` : '—'}
          </span>
        </div>
        {#if query?.version}
          <div class="query-row">
            <span class="query-key">Version</span>
            <span class="query-val">{query.version}</span>
          </div>
        {/if}
        {#if query?.map}
          <div class="query-row">
            <span class="query-key">Map</span>
            <span class="query-val">{query.map}</span>
          </div>
        {/if}
        {#if !embedded && gameserver.node_id}
          <div class="query-row">
            <span class="query-key">Node</span>
            <span class="query-val query-val-mono">{gameserver.node_id}</span>
          </div>
        {/if}
        {#if query?.players && query.players.length > 0}
          <div class="player-grid">
            {#each query.players as player}
              <span class="player-tag">{player}</span>
            {/each}
          </div>
        {/if}
        {#if !query && !isRunning}
          <div class="query-empty">Server is offline</div>
        {:else if !query}
          <div class="query-empty">No query data available</div>
        {/if}
      </div>
    </div>

    <!-- Activity feed -->
    <div class="panel">
      <div class="panel-title">Activity</div>
      <div class="feed-content">
        <div class="feed-list">
          {#each events as event (event.id)}
            <div class="feed-item">
              <span class="feed-dot {eventDotColor(event.type)}"></span>
              <div>
                <div class="feed-text">{eventLabel(event.type, event.data)}</div>
                <div class="feed-time">{timeAgo(event.created_at)} · {formatTime(event.created_at)}</div>
              </div>
            </div>
          {:else}
            <div class="feed-empty">No recent activity</div>
          {/each}
        </div>
      </div>
    </div>

    <!-- Resource history charts -->
    <div class="panel full-width">
      <StatsChart {id} />
    </div>

    <!-- SFTP -->
    {#if sftpAddr || can('gameserver.regenerate-sftp')}
      <div class="panel full-width" style="padding: 0;">
        <div class="sftp-section">
          {#if sftpAddr}
            <div class="sftp-addr">
              <CopyBlock label="SFTP" value={sftpAddr} />
            </div>
          {/if}
          {#if can('gameserver.regenerate-sftp')}
            {#if sftpPassword}
              <CopyBlock label="Password" value={sftpPassword} />
            {/if}
            <button class="sftp-regen-btn" onclick={regenerateSftpPassword} disabled={regenerating}>
              <svg viewBox="0 0 16 16" fill="currentColor"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36A.25.25 0 0 1 11.534 7zm-7.068 2H.534a.25.25 0 0 1-.192-.41L2.308 6.23a.25.25 0 0 1 .384 0l1.966 2.36A.25.25 0 0 1 4.466 9z"/><path d="M8 3a5 5 0 1 1-4.546 2.914.5.5 0 0 0-.908-.418A6 6 0 1 0 8 2v1z"/></svg>
              {regenerating ? 'Generating...' : sftpPassword ? 'Regenerate' : 'Generate Password'}
            </button>
          {/if}
        </div>
      </div>
    {/if}

  </div>
{/if}

<style>
  .overview {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .full-width { grid-column: 1 / -1; }

  .panel-title {
    padding: 14px 18px 0;
    font-size: 0.82rem; font-weight: 550;
    color: var(--text-secondary);
  }

  .connect-row {
    display: flex; gap: 12px;
    padding: 14px 18px;
  }

  .sftp-section {
    display: flex; align-items: center; gap: 12px;
    padding: 14px 18px;
  }
  .sftp-addr { flex: 1; min-width: 0; }

  .sftp-regen-btn {
    display: inline-flex; align-items: center; gap: 5px;
    padding: 7px 14px; border-radius: var(--radius-sm);
    background: none; border: 1px solid var(--border-dim);
    color: var(--text-tertiary); font-family: var(--font-mono);
    font-size: 0.7rem; cursor: pointer; flex-shrink: 0;
    transition: color 0.15s, border-color 0.15s;
  }
  .sftp-regen-btn:hover { color: var(--text-secondary); border-color: var(--border); }
  .sftp-regen-btn:disabled { opacity: 0.4; cursor: not-allowed; }
  .sftp-regen-btn svg { width: 12px; height: 12px; }



  .query-content { padding: 14px 18px 18px; }
  .query-row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 6px 0;
  }
  .query-row + .query-row { border-top: 1px solid var(--border-dim); }
  .query-key { font-size: 0.78rem; color: var(--text-tertiary); }
  .query-val { font-size: 0.82rem; font-family: var(--font-mono); font-weight: 500; }
  .query-val-mono { font-size: 0.74rem; color: var(--text-tertiary); }
  .query-empty { font-size: 0.78rem; color: var(--text-tertiary); padding: 8px 0; }

  .player-grid {
    display: flex; flex-wrap: wrap; gap: 4px;
    margin-top: 10px;
  }
  .player-tag {
    padding: 3px 9px; border-radius: 4px;
    background: var(--bg-elevated); border: 1px solid var(--border-dim);
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-secondary);
    transition: border-color 0.15s;
  }
  .player-tag:hover { border-color: var(--border); }

  .feed-content { padding: 0 18px 14px; }
  .feed-list { max-height: 185px; overflow-y: auto; }
  .feed-list::-webkit-scrollbar { width: 4px; }
  .feed-list::-webkit-scrollbar-track { background: transparent; }
  .feed-list::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

  .feed-item {
    display: flex; align-items: flex-start; gap: 10px;
    padding: 8px 0;
  }
  .feed-item + .feed-item { border-top: 1px solid var(--border-dim); }

  .feed-dot {
    width: 6px; height: 6px; border-radius: 50%;
    margin-top: 5px; flex-shrink: 0;
  }
  .feed-dot.green { background: var(--live); box-shadow: 0 0 4px var(--live-glow); }
  .feed-dot.orange { background: var(--accent); }
  .feed-dot.red { background: var(--danger); }
  .feed-dot.gray { background: var(--idle); }

  .feed-text { font-size: 0.8rem; color: var(--text-secondary); line-height: 1.4; }
  .feed-time {
    font-size: 0.66rem; font-family: var(--font-mono);
    color: var(--text-tertiary); margin-top: 2px;
  }
  .feed-empty { font-size: 0.78rem; color: var(--text-tertiary); padding: 12px 0; }

  @media (max-width: 700px) {
    .overview { grid-template-columns: 1fr; }
    .full-width { grid-column: 1; }
    .connect-row { flex-direction: column; }
    .sftp-section { flex-direction: column; align-items: stretch; }
  }
</style>
