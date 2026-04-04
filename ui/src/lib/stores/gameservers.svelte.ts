// Centralized reactive store for all gameserver state.
// SSE events feed into this store; pages read from it reactively.

import { api, type Gameserver, type GameserverStats, type QueryData, type Game, type Backup, type Schedule } from '$lib/api';
import { basePath } from '$lib/base';
import { role as roleStore } from './auth';
import { onEvent } from './sse';

export interface GameserverState {
  gameserver: Gameserver;
  stats: GameserverStats | null;
  query: QueryData | null;
  logLines: string[];
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
};

class GameserverStore {
  gameservers = $state<Record<string, GameserverState>>({});
  games = $state<Record<string, Game>>({});
  tokenId = $state<string>('');
  tokenRole = $state<string>('');
  sftpPort = $state(0);
  cluster = $state<{ total_memory_mb: number; allocated_memory_mb: number; total_cpu: number; allocated_cpu: number; total_storage_mb: number; allocated_storage_mb: number } | null>(null);
  loading = $state(true);
  initialized = $state(false);
  authRequired = $state(false);

  private unsubs: (() => void)[] = [];
  private logStreams = new Map<string, EventSource>();

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
    return s === 'running';
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

  connectionIP(id: string): string {
    const gs = this.gameservers[id]?.gameserver;
    if (!gs) return '';
    // If there's a custom connection_address, extract the host part
    if (gs.connection_address) {
      const addr = gs.connection_address;
      const colonIdx = addr.lastIndexOf(':');
      return colonIdx > 0 ? addr.substring(0, colonIdx) : addr;
    }
    return gs.node?.external_ip || gs.node?.lan_ip || '';
  }

  sftpAddress(id: string): string {
    const gs = this.gameservers[id]?.gameserver;
    if (!gs || !this.sftpPort) return '';
    const ip = this.connectionIP(id);
    if (!ip) return '';
    return `sftp://${gs.sftp_username}@${ip}:${this.sftpPort}`;
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
      const [gameservers, gameList, clusterStatus, meResponse] = await Promise.all([
        api.gameservers.list(),
        api.games.list(),
        api.clusterStatus.get().catch(() => null),
        api.me.get().catch(() => null),
      ]);

      if (clusterStatus?.config?.sftp_port) {
        this.sftpPort = clusterStatus.config.sftp_port;
      }
      if (clusterStatus?.cluster) {
        this.cluster = clusterStatus.cluster;
      }

      this.tokenId = meResponse?.token_id || '';
      this.tokenRole = meResponse?.role || '';
      roleStore.set(this.tokenRole);

      for (const g of gameList) {
        this.games[g.id] = g;
      }

      for (const gs of gameservers) {
        this.gameservers[gs.id] = this.newState(gs);
      }

      // Connect live data for active servers
      for (const gs of gameservers) {
        if (gs.status !== 'stopped') {
          this.connectLogStream(gs.id);
        }
        if (gs.status === 'running') {
          api.gameservers.stats(gs.id).then(s => { if (s) this.updateStats(gs.id, s); }).catch((e) => { console.warn('gameserverStore:', e); });
          api.gameservers.query(gs.id).then(q => { if (q) this.updateQuery(gs.id, q); }).catch((e) => { console.warn('gameserverStore:', e); });
          api.gameservers.status(gs.id).then(s => {
            if (s?.instance?.started_at && this.gameservers[gs.id]) {
              this.gameservers[gs.id].instanceStartedAt = s.instance.started_at;
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
    for (const [id] of this.logStreams) this.disconnectLogStream(id);
    this.gameservers = {};
    this.games = {};
    this.loading = true;
    this.initialized = false;
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

    // Status changes — server sends the derived display status directly
    this.unsubs.push(onEvent('gameserver.status_changed', (data: any) => {
      const state = this.gameservers[data.gameserver_id];
      if (!state) return;

      const prevStatus = state.gameserver.status;
      state.gameserver = {
        ...state.gameserver,
        status: data.status,
        error_reason: data.error_reason || '',
      };

      // Uptime tracking
      if (data.status === 'running' || data.status === 'starting') {
        if (!state.instanceStartedAt) {
          state.instanceStartedAt = new Date().toISOString();
        }
      }
      if (data.status === 'stopped' || data.status === 'error') {
        state.instanceStartedAt = '';
      }

      // Log stream and polling data lifecycle
      if (data.status === 'stopped' || data.status === 'error') {
        this.disconnectLogStream(data.gameserver_id);
        state.stats = null;
        state.query = null;
        state.logLines = [];
      } else if (prevStatus === 'stopped' && data.status !== 'stopped') {
        this.connectLogStream(data.gameserver_id);
      }
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
      logLines: [],
      instanceStartedAt: '',
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

  private connectLogStream(id: string) {
    if (this.logStreams.has(id)) return;

    const url = `${basePath}/api/gameservers/${id}/logs/stream?tail=4`;
    const es = new EventSource(url);

    es.onmessage = (e) => {
      const state = this.gameservers[id];
      if (!state) return;
      state.logLines = [...state.logLines, e.data].slice(-4);
    };

    es.onerror = () => {
      this.disconnectLogStream(id);
      // Reconnect if server is still active
      const state = this.gameservers[id];
      if (state && state.gameserver.status !== 'stopped') {
        setTimeout(() => this.connectLogStream(id), 2000);
      }
    };

    this.logStreams.set(id, es);
  }

  private disconnectLogStream(id: string) {
    const es = this.logStreams.get(id);
    if (es) {
      es.close();
      this.logStreams.delete(id);
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

