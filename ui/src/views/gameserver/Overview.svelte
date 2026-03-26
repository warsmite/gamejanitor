<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type Event } from '$lib/api';
  import { gameserverStore, toast } from '$lib/stores';
  import { onGameserverEvent } from '$lib/stores/sse';
  import { CopyBlock, TelemetryCell } from '$lib/components';

  let { id }: { id: string } = $props();

  const gsState = $derived(gameserverStore.getState(id));
  const gameserver = $derived(gsState?.gameserver ?? null);
  const stats = $derived(gsState?.stats ?? null);
  const query = $derived(gsState?.query ?? null);
  const isRunning = $derived(gameserverStore.isRunning(id));

  const hasStorageLimit = $derived(!!stats?.storage_limit_mb);

  // Derived telemetry values
  const memPercent = $derived(
    stats ? Math.round((stats.memory_usage_mb / stats.memory_limit_mb) * 100) : 0
  );
  const cpuPercent = $derived(stats ? Math.round(stats.cpu_percent) : 0);
  const storageMB = $derived(stats ? Math.round(stats.volume_size_bytes / (1024 * 1024)) : 0);
  const storagePercent = $derived(
    stats?.storage_limit_mb ? Math.round((storageMB / stats.storage_limit_mb) * 100) : (stats ? 100 : 0)
  );

  function connectionAddress(): string {
    if (!gameserver) return '';
    try {
      const ports = typeof gameserver.ports === 'string' ? JSON.parse(gameserver.ports) : gameserver.ports;
      if (ports && ports.length > 0) {
        return `${ports[0].host_port || ports[0].container_port}`;
      }
    } catch (e) { console.warn('Overview: failed to parse ports', e); }
    return '';
  }

  // Activity feed — page-specific state
  let events = $state<Event[]>([]);
  let unsub: (() => void) | null = null;

  onMount(async () => {
    // Load activity feed
    try {
      const allEvents = await api.events.history({ gameserver_id: id, limit: 40 });
      events = allEvents.filter(e => e.event_type !== 'status_changed').slice(0, 20);
    } catch (e) { console.warn('Overview: failed to load events', e); }

    // SSE: activity feed only — stats/query/status handled by store
    unsub = onGameserverEvent(id, (data: any) => {
      if (data.type === 'status_changed' || data.type === 'gameserver.stats' || data.type === 'gameserver.query') return;

      const event: Event = {
        id: crypto.randomUUID(),
        event_type: data.type || 'unknown',
        gameserver_id: id,
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
    if (type === 'gameserver.error' && data?.reason) {
      return `Error: ${data.reason}`;
    }
    const labels: Record<string, string> = {
      'gameserver.create': 'Gameserver created',
      'gameserver.start': 'Start requested',
      'gameserver.stop': 'Stop requested',
      'gameserver.restart': 'Restart requested',
      'gameserver.update_game': 'Game update requested',
      'gameserver.reinstall': 'Reinstall requested',
      'gameserver.migrate': 'Migration requested',
      'gameserver.image_pulling': 'Pulling image...',
      'gameserver.image_pulled': 'Image pulled',
      'gameserver.container_creating': 'Creating container',
      'gameserver.container_started': 'Container started',
      'gameserver.ready': 'Ready — accepting players',
      'gameserver.container_stopping': 'Stopping container',
      'gameserver.container_stopped': 'Container stopped',
      'gameserver.container_exited': 'Container exited',
      'gameserver.error': 'Error',
      'backup.create': 'Backup started',
      'backup.completed': 'Backup completed',
      'backup.failed': 'Backup failed',
      'backup.restore': 'Restore started',
      'backup.restore.completed': 'Restore completed',
      'backup.restore.failed': 'Restore failed',
      'backup.delete': 'Backup deleted',
      'schedule.create': 'Schedule created',
      'schedule.update': 'Schedule updated',
      'schedule.delete': 'Schedule deleted',
      'schedule.task.completed': 'Scheduled task completed',
      'schedule.task.failed': 'Scheduled task failed',
    };
    return labels[type] || type.replace(/\./g, ' ');
  }

  function eventDotColor(type: string): string {
    if (type.includes('ready') || type.includes('started') || type.includes('completed')) return 'green';
    if (type.includes('error') || type.includes('failed') || type.includes('exited')) return 'red';
    if (type.includes('pulling') || type.includes('creating') || type.includes('stopping') || type.includes('start') || type.includes('stop') || type.includes('restart')) return 'orange';
    return 'gray';
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
        <CopyBlock label="Connect" value={connectionAddress()} primary={true} />
        <CopyBlock label="SFTP" value={`sftp://${gameserver.sftp_username}@localhost:2222`} />
      </div>
    </div>

    <!-- Telemetry — full width -->
    <div class="panel full-width">
      <div class="panel-title">Resources</div>
      <div class="tele-grid">
        <TelemetryCell
          label="Memory"
          value={stats ? `${(stats.memory_usage_mb / 1024).toFixed(1)}` : '—'}
          unit={stats ? ' GB' : ''}
          detail={stats ? `of ${(stats.memory_limit_mb / 1024).toFixed(0)} GB` : ''}
          percent={memPercent}
          color="accent"
        />
        <TelemetryCell
          label="CPU"
          value={stats ? `${cpuPercent}` : '—'}
          unit={stats ? '%' : ''}
          detail={gameserver.cpu_limit ? `${gameserver.cpu_limit} cores` : ''}
          percent={cpuPercent}
          color="accent"
        />
        <TelemetryCell
          label="Storage"
          value={stats ? `${storageMB < 1024 ? storageMB + '' : (storageMB / 1024).toFixed(1)}` : '—'}
          unit={stats ? (storageMB < 1024 ? ' MB' : ' GB') : ''}
          detail={stats?.storage_limit_mb ? `of ${(stats.storage_limit_mb / 1024).toFixed(0)} GB` : 'no limit'}
          percent={storagePercent}
          color={hasStorageLimit ? 'accent' : 'live'}
        />
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
              <span class="feed-dot {eventDotColor(event.event_type)}"></span>
              <div>
                <div class="feed-text">{eventLabel(event.event_type, event.data)}</div>
                <div class="feed-time">{timeAgo(event.created_at)} · {formatTime(event.created_at)}</div>
              </div>
            </div>
          {:else}
            <div class="feed-empty">No recent activity</div>
          {/each}
        </div>
      </div>
    </div>

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
    font-size: 0.66rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
  }

  .connect-row {
    display: flex; gap: 12px;
    padding: 14px 18px;
  }

  .tele-grid {
    display: grid; grid-template-columns: repeat(3, 1fr);
    padding: 14px 18px 18px;
  }
  .tele-grid > :global(div) { padding: 0 16px; }
  .tele-grid > :global(div + div) { border-left: 1px solid var(--border-dim); }
  .tele-grid > :global(div:first-child) { padding-left: 0; }
  .tele-grid > :global(div:last-child) { padding-right: 0; }

  .query-content { padding: 14px 18px 18px; }
  .query-row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 6px 0;
  }
  .query-row + .query-row { border-top: 1px solid var(--border-dim); }
  .query-key { font-size: 0.78rem; color: var(--text-tertiary); }
  .query-val { font-size: 0.82rem; font-family: var(--font-mono); font-weight: 500; }
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
  .feed-list { max-height: 280px; overflow-y: auto; }
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
    .tele-grid { grid-template-columns: 1fr; }
    .tele-grid > :global(div + div) { border-left: none; border-top: 1px solid var(--border-dim); padding-left: 0; padding-top: 12px; }
    .connect-row { flex-direction: column; }
  }
</style>
