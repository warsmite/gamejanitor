// Centralized reactive store for all gameserver state.
// SSE events feed into this store; pages read from it reactively.

import { api, type Gameserver, type GameserverStats, type QueryData, type Game, type Backup, type Schedule } from '$lib/api';
import { role as roleStore } from './auth';
import { onEvent } from './sse';

export interface GameserverState {
  gameserver: Gameserver;
  stats: GameserverStats | null;
  query: QueryData | null;
  instanceStartedAt: string;
  backups: Backup[] | null;       // null = not loaded yet
  schedules: Schedule[] | null;   // null = not loaded yet
}

// Maps operation phases to display labels for the UI badge.
export const phaseLabels: Record<string, string> = {
  'downloading_game': 'Downloading game files...',
  'pulling_image': 'Pulling image...',
  'installing': 'Installing...',
  'starting': 'Starting...',
  'stopping': 'Stopping...',
  'creating_backup': 'Backing up...',
  'restoring_backup': 'Restoring...',
  'updating_game': 'Updating...',
  'reinstalling': 'Reinstalling...',
  'migrating': 'Migrating...',
  'deleting': 'Deleting...',
};

class GameserverStore {
  gameservers = $state<Record<string, GameserverState>>({});
  games = $state<Record<string, Game>>({});
  tokenId = $state<string>('');
  tokenRole = $state<string>('');
  canCreate = $state(false);
  cluster = $state<{ total_memory_mb: number; allocated_memory_mb: number; total_cpu: number; allocated_cpu: number; total_storage_mb: number; allocated_storage_mb: number } | null>(null);
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
    const gs = this.gameservers[id]?.gameserver;
    return !!gs && gs.process_state === 'running' && gs.ready;
  }

  isStopped(id: string): boolean {
    const gs = this.gameservers[id]?.gameserver;
    return !!gs && gs.process_state === 'none' && !gs.operation;
  }

  connectionAddress(id: string): string {
    const gs = this.gameservers[id]?.gameserver;
    if (!gs || !gs.connection_host) return '';
    const ports = gs.ports || [];
    const gamePort = ports.find((p: any) => p.name === 'game') || ports[0];
    if (!gamePort) return '';
    return `${gs.connection_host}:${gamePort.host_port}`;
  }

  sftpAddress(id: string): string {
    const gs = this.gameservers[id]?.gameserver;
    if (!gs || !gs.connection_host || !gs.sftp_port) return '';
    return `sftp://${gs.sftp_username}@${gs.connection_host}:${gs.sftp_port}`;
  }

  // Check if the current token has a permission on a specific gameserver.
  // Admin and owners have all permissions. Granted tokens check the grant's permission list.
  canOnGameserver(permission: string, gsId: string): boolean {
    // No auth = full access
    if (!this.tokenId) return true;
    // Admin = full access
    if (this.tokenRole === 'admin') return true;
    const gs = this.gameservers[gsId]?.gameserver;
    if (!gs) return false;
    // Owner = all permissions
    if (gs.created_by_token_id === this.tokenId) return true;
    // Check grants
    const grant = gs.grants?.[this.tokenId];
    if (!grant) return false;
    // Empty grant = all permissions
    if (grant.length === 0) return true;
    return grant.includes(permission);
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
      const [gameservers, gameList, clusterData, meResponse] = await Promise.all([
        api.gameservers.list(),
        api.games.list(),
        api.cluster.get().catch(() => null),
        api.me.get().catch(() => null),
      ]);

      if (clusterData) {
        this.cluster = clusterData;
      }

      this.tokenId = meResponse?.token_id || '';
      this.tokenRole = meResponse?.role || '';
      this.canCreate = this.tokenRole === 'admin' || meResponse?.can_create || false;
      roleStore.set(this.tokenRole);

      for (const g of gameList) {
        this.games[g.id] = g;
      }

      for (const gs of gameservers) {
        this.gameservers[gs.id] = this.newState(gs);
      }

      for (const gs of gameservers) {
        if (gs.process_state === 'running' && gs.ready) {
          api.gameservers.stats(gs.id).then(s => { if (s) this.updateStats(gs.id, s); }).catch((e) => { console.warn('gameserverStore:', e); });
          api.gameservers.query(gs.id).then(q => { if (q) this.updateQuery(gs.id, q); }).catch((e) => { console.warn('gameserverStore:', e); });
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
    this.authRequired = false;
    this.tokenId = '';
    this.tokenRole = '';
    this.canCreate = false;
  }

  // Reset and re-initialize after a token change (e.g. invite claim, login).
  async reinit() {
    this.destroy();
    await this.init();
  }

  // ── SSE Subscriptions ──

  private subscribeToSSE() {
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

    // Primary fact: process is ready (ready pattern matched, or no pattern).
    this.unsubs.push(onEvent('gameserver.ready', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      const now = new Date().toISOString();
      state.gameserver = {
        ...state.gameserver,
        process_state: 'running',
        ready: true,
        ready_at: state.gameserver.ready_at || now,
        started_at: state.gameserver.started_at || now,
        error_reason: '',
      };
      if (!state.instanceStartedAt) state.instanceStartedAt = now;
    }));

    // Primary fact: instance exited (clean or crash — distinguished by error_reason).
    this.unsubs.push(onEvent('gameserver.instance_exited', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      state.gameserver = {
        ...state.gameserver,
        process_state: 'exited',
        ready: false,
        instance_id: undefined,
      };
      state.instanceStartedAt = '';
      state.stats = null;
      state.query = null;
    }));

    // Primary fact: instance stopped (graceful, user-initiated).
    this.unsubs.push(onEvent('gameserver.instance_stopped', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      state.gameserver = {
        ...state.gameserver,
        process_state: 'none',
        ready: false,
        error_reason: '',
        instance_id: undefined,
      };
      state.instanceStartedAt = '';
      state.stats = null;
      state.query = null;
    }));

    // Primary fact: error recorded (crash, operation failed, etc.).
    this.unsubs.push(onEvent('gameserver.error', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      state.gameserver = {
        ...state.gameserver,
        error_reason: data.reason || '',
      };
    }));

    // Primary fact: worker connection state changed — patch worker_online on
    // every gameserver assigned to that node.
    this.unsubs.push(onEvent('worker.connected', (data: any) => {
      this.setWorkerOnline(data.worker_id, true);
    }));
    this.unsubs.push(onEvent('worker.disconnected', (data: any) => {
      this.setWorkerOnline(data.worker_id, false);
    }));

    // Operation state — update from event payload on phase changes
    this.unsubs.push(onEvent('gameserver.operation', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;
      state.gameserver = { ...state.gameserver, operation: data.operation ?? null };
    }));

    // Re-fetch gameserver on update (name/config changed)
    this.unsubs.push(onEvent('gameserver.update', (data: any) => {
      api.gameservers.get(data.gameserver_id).then(gs => {
        if (this.gameservers[gs.id]) {
          this.gameservers[gs.id].gameserver = gs;
        }
      }).catch((e) => { console.warn('gameserverStore:', e); });
    }));

    // Backup events — list refresh
    this.unsubs.push(onEvent('backup.*', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (state?.backups !== null) this.loadBackups(data.gameserver_id);
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
      instanceStartedAt: gs.started_at || '',
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

  // Patch worker_online across all gameservers assigned to a given node.
  // When a worker goes offline the process facts become stale — clear them to
  // "unknown" rather than lying about last-seen values.
  private setWorkerOnline(nodeId: string, online: boolean) {
    for (const state of Object.values(this.gameservers)) {
      if (state.gameserver.node_id !== nodeId) continue;
      state.gameserver = {
        ...state.gameserver,
        worker_online: online,
      };
      if (!online) {
        state.gameserver = {
          ...state.gameserver,
          process_state: 'none',
          ready: false,
        };
        state.stats = null;
        state.query = null;
      }
    }
  }
}

export const gameserverStore = new GameserverStore();

export function formatUptime(instanceStartedAt: string): string {
  if (!instanceStartedAt) return '';
  const started = new Date(instanceStartedAt).getTime();
  const diff = Math.max(0, Math.floor((Date.now() - started) / 1000));

  const days = Math.floor(diff / 86400);
  const hours = Math.floor((diff % 86400) / 3600);
  const mins = Math.floor((diff % 3600) / 60);

  if (days > 0) return `Up ${days}d ${hours}h`;
  if (hours > 0) return `Up ${hours}h ${mins}m`;
  return `Up ${mins}m`;
}

