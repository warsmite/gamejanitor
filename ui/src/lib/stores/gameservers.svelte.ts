// Centralized reactive store for all gameserver state.
// SSE events feed into this store; pages read from it reactively.

import { api, type Gameserver, type GameserverStats, type QueryData, type Game, type Backup, type Schedule } from '$lib/api';
import { onEvent } from './sse';

export interface DepotProgressData {
  percent: number;
  completedBytes: number;
  totalBytes: number;
}

export interface GameserverState {
  gameserver: Gameserver;
  stats: GameserverStats | null;
  query: QueryData | null;
  logLines: string[];
  containerStartedAt: string;
  activeOperation: string | null;
  depotProgress: DepotProgressData | null;
  backups: Backup[] | null;       // null = not loaded yet
  schedules: Schedule[] | null;   // null = not loaded yet
}

const operationStartEvents: Record<string, string> = {
  'gameserver.depot_downloading': 'Downloading game files...',
  'backup.create': 'Backing up...',
  'backup.restore': 'Restoring...',
  'gameserver.update_game': 'Updating...',
  'gameserver.reinstall': 'Reinstalling...',
  'gameserver.migrate': 'Migrating...',
};

const operationClearEvents: Record<string, string[]> = {
  'gameserver.depot_downloading': ['gameserver.depot_complete', 'gameserver.depot_cached', 'gameserver.image_pulling', 'gameserver.error'],
  'backup.create': ['backup.completed', 'backup.failed'],
  'backup.restore': ['backup.restore.completed', 'backup.restore.failed', 'gameserver.ready', 'gameserver.error'],
  'gameserver.update_game': ['gameserver.ready', 'gameserver.error'],
  'gameserver.reinstall': ['gameserver.ready', 'gameserver.error'],
  'gameserver.migrate': ['gameserver.ready', 'gameserver.error'],
};

class GameserverStore {
  gameservers = $state<Record<string, GameserverState>>({});
  games = $state<Record<string, Game>>({});
  permissions = $state<string[]>([]);
  loading = $state(true);
  initialized = $state(false);
  authRequired = $state(false);

  private unsubs: (() => void)[] = [];

  // ── Accessors ──

  get list(): Gameserver[] {
    return Object.values(this.gameservers)
      .map(s => s.gameserver)
      .sort((a, b) => a.name.localeCompare(b.name));
  }

  getState(id: string): GameserverState | undefined {
    return this.gameservers[id];
  }

  get(id: string): Gameserver | undefined {
    return this.gameservers[id]?.gameserver;
  }

  gameFor(gameId: string): Game | undefined {
    return this.games[gameId];
  }

  isRunning(id: string): boolean {
    const s = this.gameservers[id]?.gameserver.status;
    return s === 'running' || s === 'started';
  }

  isStopped(id: string): boolean {
    return this.gameservers[id]?.gameserver.status === 'stopped';
  }

  connectionAddress(id: string): string {
    const gs = this.gameservers[id]?.gameserver;
    if (!gs) return '';
    if (gs.connection_address) return gs.connection_address;

    const ip = gs.node?.external_ip || gs.node?.lan_ip || '';
    const ports = gs.ports || [];
    const gamePort = ports.find((p: any) => p.name === 'game') || ports[0];
    if (!ip || !gamePort) return '';
    return `${ip}:${gamePort.host_port}`;
  }

  can(permission: string): boolean {
    return this.permissions.includes(permission);
  }

  // ── Data loading (lazy, called by pages on first visit) ──

  async loadBackups(gsId: string) {
    try {
      const backups = await api.backups.list(gsId);
      const state = this.gameservers[gsId];
      if (state) state.backups = backups || [];
    } catch (e) { console.warn('gameserverStore: failed to load backups', e); }
  }

  async loadSchedules(gsId: string) {
    try {
      const schedules = await api.schedules.list(gsId);
      const state = this.gameservers[gsId];
      if (state) state.schedules = schedules || [];
    } catch (e) { console.warn('gameserverStore: failed to load schedules', e); }
  }

  // ── Lifecycle ──

  async init() {
    if (this.initialized) return;

    try {
      const [gsResponse, gameList] = await Promise.all([
        api.gameservers.list(),
        api.games.list(),
      ]);

      this.permissions = gsResponse.permissions || [];

      for (const g of gameList) {
        this.games[g.id] = g;
      }

      for (const gs of gsResponse.gameservers) {
        this.gameservers[gs.id] = this.newState(gs);
      }

      // Fetch live data for active servers
      for (const gs of gsResponse.gameservers) {
        if (gs.status !== 'stopped') {
          this.fetchLogs(gs.id);
        }
        if (gs.status === 'running' || gs.status === 'started') {
          api.gameservers.stats(gs.id).then(s => { if (s) this.updateStats(gs.id, s); }).catch((e) => { console.warn('gameserverStore:', e); });
          api.gameservers.query(gs.id).then(q => { if (q) this.updateQuery(gs.id, q); }).catch((e) => { console.warn('gameserverStore:', e); });
          api.gameservers.status(gs.id).then(s => {
            if (s?.container?.started_at && this.gameservers[gs.id]) {
              this.gameservers[gs.id].containerStartedAt = s.container.started_at;
            }
          }).catch((e) => { console.warn('gameserverStore:', e); });
        }
      }

      this.subscribeToSSE();
    } catch (e: any) {
      if (e?.status === 401) {
        this.authRequired = true;
      } else {
        console.error('Failed to initialize gameserver store:', e);
      }
    } finally {
      this.loading = false;
      this.initialized = true;
    }
  }

  destroy() {
    for (const unsub of this.unsubs) unsub();
    this.unsubs = [];
    this.gameservers = {};
    this.games = {};
    this.loading = true;
    this.initialized = false;
  }

  // ── SSE Subscriptions ──

  private subscribeToSSE() {
    // Status changes
    this.unsubs.push(onEvent('status_changed', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;

      const wasInactive = state.gameserver.status === 'stopped';
      const nowStopped = data.new_status === 'stopped';

      state.gameserver = { ...state.gameserver, status: data.new_status, error_reason: data.error_reason || '' };

      if (wasInactive && !nowStopped) {
        this.fetchLogs(data.gameserver_id);
      }
      if (nowStopped) {
        state.stats = null;
        state.query = null;
        state.logLines = [];
        state.containerStartedAt = '';
        state.activeOperation = null;
      }
    }));

    // Stats from server-side polling
    this.unsubs.push(onEvent('gameserver.stats', (data: any) => {
      this.updateStats(data.gameserver_id, {
        cpu_percent: data.cpu_percent,
        memory_usage_mb: data.memory_usage_mb,
        memory_limit_mb: data.memory_limit_mb,
        net_rx_bytes: data.net_rx_bytes ?? 0,
        net_tx_bytes: data.net_tx_bytes ?? 0,
        volume_size_bytes: data.volume_size_bytes,
        storage_limit_mb: data.storage_limit_mb,
      });
    }));

    // Query from server-side polling
    this.unsubs.push(onEvent('gameserver.query', (data: any) => {
      this.updateQuery(data.gameserver_id, {
        players_online: data.players_online,
        max_players: data.max_players,
        players: data.players || [],
        map: data.map,
        version: data.version,
      });
    }));

    // Lifecycle events — log refresh, uptime, operations
    this.unsubs.push(onEvent('gameserver.*', (data: any) => {
      if (!data.gameserver_id || data.type === 'gameserver.stats' || data.type === 'gameserver.query') return;
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;

      // Uptime tracking
      if (data.type === 'gameserver.container_started' || data.type === 'gameserver.ready') {
        state.containerStartedAt = new Date().toISOString();
      }
      if (data.type === 'gameserver.container_stopped' || data.type === 'gameserver.container_exited') {
        state.containerStartedAt = '';
      }

      // Operation tracking
      if (data.type in operationStartEvents) {
        state.activeOperation = data.type;
      }
      if (state.activeOperation && operationClearEvents[state.activeOperation]?.includes(data.type)) {
        state.activeOperation = null;
      }

      // Depot download progress
      if (data.type === 'gameserver.depot_progress') {
        state.depotProgress = {
          percent: data.percent ?? 0,
          completedBytes: data.completed_bytes ?? 0,
          totalBytes: data.total_bytes ?? 0,
        };
      }
      if (data.type === 'gameserver.depot_complete' || data.type === 'gameserver.depot_cached' || data.type === 'gameserver.error') {
        state.depotProgress = null;
      }

      // Re-fetch gameserver on update (name/config changed)
      if (data.type === 'gameserver.update') {
        api.gameservers.get(data.gameserver_id).then(gs => {
          if (this.gameservers[gs.id]) {
            this.gameservers[gs.id].gameserver = gs;
          }
        }).catch((e) => { console.warn('gameserverStore:', e); });
      }

      // Refresh log tail
      this.fetchLogs(data.gameserver_id);
    }));

    // Backup events — operation badges + list refresh
    this.unsubs.push(onEvent('backup.create', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      state.activeOperation = 'backup.create';
      if (state.backups !== null) this.loadBackups(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('backup.completed', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      if (state.activeOperation === 'backup.create') state.activeOperation = null;
      if (state.backups !== null) this.loadBackups(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('backup.failed', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      if (state.activeOperation === 'backup.create') state.activeOperation = null;
      if (state.backups !== null) this.loadBackups(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('backup.delete', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (state?.backups !== null) this.loadBackups(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('backup.restore', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      state.activeOperation = 'backup.restore';
      if (state.backups !== null) this.loadBackups(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('backup.restore.completed', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      if (state.activeOperation === 'backup.restore') state.activeOperation = null;
      if (state.backups !== null) this.loadBackups(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('backup.restore.failed', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      if (state.activeOperation === 'backup.restore') state.activeOperation = null;
      if (state.backups !== null) this.loadBackups(data.gameserver_id);
    }));

    // Schedule events — list refresh
    this.unsubs.push(onEvent('schedule.create', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (state?.schedules !== null) this.loadSchedules(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('schedule.update', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (state?.schedules !== null) this.loadSchedules(data.gameserver_id);
    }));
    this.unsubs.push(onEvent('schedule.delete', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (state?.schedules !== null) this.loadSchedules(data.gameserver_id);
    }));

    // Gameserver create/delete
    this.unsubs.push(onEvent('gameserver.create', (data: any) => {
      if (data.gameserver_id && !this.gameservers[data.gameserver_id]) {
        api.gameservers.get(data.gameserver_id).then(gs => {
          this.gameservers[gs.id] = this.newState(gs);
        }).catch((e) => { console.warn('gameserverStore:', e); });
      }
    }));

    this.unsubs.push(onEvent('gameserver.delete', (data: any) => {
      if (data.gameserver_id) {
        delete this.gameservers[data.gameserver_id];
      }
    }));
  }

  // ── Internal helpers ──

  private newState(gs: Gameserver): GameserverState {
    return {
      gameserver: gs,
      stats: null,
      query: null,
      logLines: [],
      containerStartedAt: '',
      activeOperation: null,
      depotProgress: null,
      backups: null,
      schedules: null,
    };
  }

  private updateStats(id: string, stats: GameserverStats) {
    const state = this.gameservers[id];
    if (state) state.stats = stats;
  }

  private updateQuery(id: string, query: QueryData) {
    const state = this.gameservers[id];
    if (state) state.query = query;
  }

  private fetchLogs(id: string) {
    api.gameservers.logs(id, 4).then(r => {
      const state = this.gameservers[id];
      if (state && r?.lines) {
        state.logLines = r.lines.slice(-4);
      }
    }).catch((e) => { console.warn('gameserverStore:', e); });
  }
}

export const gameserverStore = new GameserverStore();

export function formatUptime(containerStartedAt: string): string {
  if (!containerStartedAt) return '';
  const started = new Date(containerStartedAt).getTime();
  const diff = Math.max(0, Math.floor((Date.now() - started) / 1000));

  const days = Math.floor(diff / 86400);
  const hours = Math.floor((diff % 86400) / 3600);
  const mins = Math.floor((diff % 3600) / 60);

  if (days > 0) return `Up ${days}d ${hours}h`;
  if (hours > 0) return `Up ${hours}h ${mins}m`;
  return `Up ${mins}m`;
}

export const operationLabels: Record<string, string> = operationStartEvents;
