// Typed API client for the Gamejanitor REST API.
// Every API call goes through this file — no raw fetch elsewhere.

import { basePath } from './base';

function getToken(): string | null {
  const match = document.cookie.match(/(?:^|; )_token=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : null;
}

interface ApiError {
  status: number;
  error: string;
}

class ApiClientError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = 'ApiClientError';
  }
}

async function request<T>(method: string, path: string, body?: any, query?: Record<string, any>): Promise<T> {
  let url = basePath + path;
  if (query) {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null) params.set(k, String(v));
    }
    const qs = params.toString();
    if (qs) url += '?' + qs;
  }

  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const opts: RequestInit = { method, headers };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }

  const resp = await fetch(url, opts);

  if (resp.status === 204 || resp.status === 202) return undefined as T;

  if (resp.status === 401) {
    console.warn('api: 401 Unauthorized on', method, path);
    throw new ApiClientError(401, 'Unauthorized');
  }

  const json = await resp.json();

  if (!resp.ok || json.status === 'error') {
    throw new ApiClientError(resp.status, json.error || 'Unknown error');
  }

  return json.data as T;
}

function get<T>(path: string, query?: Record<string, any>): Promise<T> {
  return request<T>('GET', path, undefined, query);
}

function post<T>(path: string, body?: any): Promise<T> {
  return request<T>('POST', path, body);
}

function patch<T>(path: string, body?: any): Promise<T> {
  return request<T>('PATCH', path, body);
}

function put<T>(path: string, body?: any): Promise<T> {
  return request<T>('PUT', path, body);
}

function del<T>(path: string, query?: Record<string, any>): Promise<T> {
  return request<T>('DELETE', path, undefined, query);
}

async function putRaw(url: string, content: string): Promise<void> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const resp = await fetch(basePath + url, { method: 'PUT', headers, body: content });
  if (resp.status === 204) return;
  if (!resp.ok) {
    const json = await resp.json().catch((e) => { console.warn('api: failed to parse write response', e); return { error: 'Write failed' }; });
    throw new ApiClientError(resp.status, json.error || 'Write failed');
  }
}

async function uploadFile(path: string, filePath: string, file: File, filename?: string): Promise<void> {
  const form = new FormData();
  form.append('path', filePath);
  form.append('file', file, filename || file.name);

  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const resp = await fetch(basePath + path, { method: 'POST', headers, body: form });
  if (!resp.ok) {
    const json = await resp.json().catch((e) => { console.warn('api: failed to parse upload response', e); return { error: 'Upload failed' }; });
    throw new ApiClientError(resp.status, json.error || 'Upload failed');
  }
}

// ── Types ──

export interface GameserverNode {
  external_ip: string;
  lan_ip: string;
}

export interface OperationProgress {
  percent: number;
  completed_bytes?: number;
  total_bytes?: number;
}

export interface Operation {
  type: string;
  phase: string;
  progress?: OperationProgress;
}

export interface Gameserver {
  id: string;
  name: string;
  game_id: string;
  status: string;
  error_reason?: string;
  operation?: Operation | null;
  ports: any;
  env: any;
  memory_limit_mb: number;
  cpu_limit: number;
  cpu_enforced: boolean;
  storage_limit_mb?: number;
  backup_limit?: number;
  instance_id?: string;
  volume_name: string;
  port_mode: string;
  node_id?: string;
  node?: GameserverNode;
  node_tags: Record<string, string>;
  sftp_username: string;
  installed: boolean;
  auto_restart: boolean;
  connection_address?: string;
  archived: boolean;
  created_at: string;
  updated_at: string;
}

export interface GameserverStatus {
  status: string;
  error_reason?: string;
  container?: {
    state: string;
    started_at: string;
  };
}

export interface GameserverStats {
  cpu_percent: number;
  memory_usage_mb: number;
  memory_limit_mb: number;
  net_rx_bytes: number;
  net_tx_bytes: number;
  volume_size_bytes: number;
  storage_limit_mb?: number;
}

export interface StatsHistoryPoint {
  timestamp: string;
  cpu_percent: number;
  memory_usage_mb: number;
  memory_limit_mb: number;
  net_rx_bytes_per_sec: number;
  net_tx_bytes_per_sec: number;
  volume_size_bytes: number;
  players_online: number;
}

export interface QueryData {
  players_online: number;
  max_players: number;
  players: string[];
  map: string;
  version: string;
}

export interface Game {
  id: string;
  name: string;
  description?: string;
  base_image: string;
  icon_path: string;
  default_ports: any[];
  default_env: EnvVar[];
  recommended_memory_mb: number;
  disabled_capabilities: string[];
}


export interface EnvVar {
  key: string;
  default: string;
  label?: string;
  type?: string;
  group?: string;
  options?: string[];
  dynamic_options?: { source: string; config?: Record<string, any> };
  required?: boolean;
  notice?: string;
  autogenerate?: string;
  system?: boolean;
  hidden?: boolean;
  triggers_install?: boolean;
}

export interface DynamicOption {
  value: string;
  label: string;
  group?: string;
}

export interface Backup {
  id: string;
  gameserver_id: string;
  name: string;
  size_bytes: number;
  status: string;
  error_reason: string;
  created_at: string;
}

export interface Schedule {
  id: string;
  gameserver_id: string;
  name: string;
  type: string;
  cron_expr: string;
  payload: string;
  enabled: boolean;
  one_shot: boolean;
  last_run?: string;
  next_run?: string;
  created_at: string;
}

export interface Token {
  id: string;
  name: string;
  scope: string;
  gameserver_ids: string[];
  permissions: string[];
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
}

export interface WebhookEndpoint {
  id: string;
  description: string;
  url: string;
  secret_set: boolean;
  events: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface WebhookDelivery {
  id: string;
  event_type: string;
  state: string;
  attempts: number;
  last_attempt_at?: string;
  next_attempt_at: string;
  last_error?: string;
  created_at: string;
}

export interface WorkerView {
  id: string;
  lan_ip: string;
  external_ip: string;
  cpu_cores: number;
  memory_total_mb: number;
  memory_available_mb: number;
  gameserver_count: number;
  allocated_memory_mb: number;
  allocated_cpu: number;
  port_range_start?: number;
  port_range_end?: number;
  max_memory_mb?: number;
  max_cpu?: number;
  max_storage_mb?: number;
  cordoned: boolean;
  tags: Record<string, string>;
  status: string;
  last_seen?: string;
}

export interface FileEntry {
  name: string;
  is_dir: boolean;
  size: number;
  mod_time: string;
  permissions: string;
}

export interface Event {
  id: string;
  gameserver_id: string | null;
  worker_id: string;
  type: string;
  actor: any;
  data: any;
  created_at: string;
}

// ── Mods ──

export interface ModVersionOption {
  value: string;
  label: string;
  group?: string;
}

export interface VersionPickerConfig {
  env: string;
  current: string;
  options: ModVersionOption[];
}

export interface LoaderOption {
  value: string;
  mod_sources: string[];
}

export interface LoaderPickerConfig {
  env: string;
  current: string;
  options: LoaderOption[];
}

export interface ModCategorySource {
  name: string;
  delivery: string;
  install_path?: string;
  overrides_path?: string;
  filters?: Record<string, string>;
  config?: Record<string, string>;
}

export interface ModCategoryDef {
  name: string;
  always_available?: boolean;
  install_path?: string;
  sources: ModCategorySource[];
}

export interface ModTabConfig {
  version?: VersionPickerConfig;
  loader?: LoaderPickerConfig;
  categories: ModCategoryDef[];
}

export interface InstalledMod {
  id: string;
  gameserver_id: string;
  source: string;
  source_id: string;
  category: string;
  name: string;
  version: string;
  version_id: string;
  file_path: string;
  file_name: string;
  download_url: string;
  file_hash: string;
  delivery: string;
  auto_installed: boolean;
  depends_on?: string;
  pack_id?: string;
  metadata: any;
  installed_at: string;
}

export interface ModSearchResult {
  source_id: string;
  source: string;
  name: string;
  slug: string;
  author: string;
  description: string;
  icon_url: string;
  downloads: number;
  updated_at: string;
  loaders?: string[];
  game_versions?: string[];
}

export interface ModSearchResults {
  results: ModSearchResult[];
  total: number;
  offset: number;
  limit: number;
}

export interface ModVersion {
  version_id: string;
  version: string;
  file_name: string;
  download_url: string;
  game_version: string;
  game_versions?: string[];
  loader: string;
}

export interface ModUpdate {
  mod_id: string;
  mod_name: string;
  current_version: string;
  latest_version: ModVersion;
}

export interface ModIssue {
  mod_id: string;
  mod_name: string;
  type: string;
  reason: string;
}

export interface PackInstallResult {
  pack: InstalledMod;
  mod_count: number;
  overrides: string[];
  version_changed?: string;
  loader_changed?: string;
  needs_restart: boolean;
  version_downgrade?: boolean;
}

export interface ScanResult {
  tracked: InstalledMod[];
  untracked: UntrackedFile[];
  missing: InstalledMod[];
}

export interface UntrackedFile {
  name: string;
  path: string;
  size: number;
  category: string;
}

// ── API ──

export const api = {
  gameservers: {
    list: () => get<{ gameservers: Gameserver[]; permissions: string[] }>('/api/gameservers'),
    get: (id: string) => get<Gameserver>(`/api/gameservers/${id}`),
    create: (data: any) => post<Gameserver>('/api/gameservers', data),
    update: (id: string, data: any) => patch<Gameserver>(`/api/gameservers/${id}`, data),
    delete: (id: string) => del(`/api/gameservers/${id}`),
    start: (id: string) => post<Gameserver>(`/api/gameservers/${id}/start`),
    stop: (id: string) => post<Gameserver>(`/api/gameservers/${id}/stop`),
    restart: (id: string) => post<Gameserver>(`/api/gameservers/${id}/restart`),
    updateGame: (id: string) => post<Gameserver>(`/api/gameservers/${id}/update-game`),
    reinstall: (id: string) => post<Gameserver>(`/api/gameservers/${id}/reinstall`),
    migrate: (id: string, nodeId: string) => post<Gameserver>(`/api/gameservers/${id}/migrate`, { node_id: nodeId }),
    archive: (id: string) => post<Gameserver>(`/api/gameservers/${id}/archive`),
    unarchive: (id: string, nodeId?: string) => post<Gameserver>(`/api/gameservers/${id}/unarchive`, nodeId ? { node_id: nodeId } : undefined),
    bulk: (action: string, ids: string[]) => post<any>('/api/gameservers/bulk', { action, all: ids.length === 0 }),
    regenerateSftpPassword: (id: string) => post<{ sftp_password: string }>(`/api/gameservers/${id}/regenerate-sftp-password`),
    status: (id: string) => get<GameserverStatus>(`/api/gameservers/${id}/status`),
    stats: (id: string) => get<GameserverStats>(`/api/gameservers/${id}/stats`),
    statsHistory: (id: string, period: '1h' | '24h' | '7d' = '1h') =>
      get<StatsHistoryPoint[]>(`/api/gameservers/${id}/stats/history`, { period }),
    query: (id: string) => get<QueryData>(`/api/gameservers/${id}/query`),
    logs: (id: string, opts?: { tail?: number; session?: number }) => get<{ lines: string[]; historical?: boolean; session?: number }>(`/api/gameservers/${id}/logs`, opts),
    logSessions: (id: string) => get<{ index: number; mod_time: string }[]>(`/api/gameservers/${id}/logs/sessions`),
    command: (id: string, command: string) => post<{ output: string }>(`/api/gameservers/${id}/command`, { command }),
  },

  backups: {
    list: (gsId: string) => get<Backup[]>(`/api/gameservers/${gsId}/backups`),
    create: (gsId: string) => post<Backup>(`/api/gameservers/${gsId}/backups`),
    restore: (gsId: string, bkId: string) => post(`/api/gameservers/${gsId}/backups/${bkId}/restore`),
    downloadUrl: (gsId: string, bkId: string) => `/api/gameservers/${gsId}/backups/${bkId}/download`,
    delete: (gsId: string, bkId: string) => del(`/api/gameservers/${gsId}/backups/${bkId}`),
  },

  schedules: {
    list: (gsId: string) => get<Schedule[]>(`/api/gameservers/${gsId}/schedules`),
    get: (gsId: string, sId: string) => get<Schedule>(`/api/gameservers/${gsId}/schedules/${sId}`),
    create: (gsId: string, data: any) => post<Schedule>(`/api/gameservers/${gsId}/schedules`, data),
    update: (gsId: string, sId: string, data: any) => patch<Schedule>(`/api/gameservers/${gsId}/schedules/${sId}`, data),
    delete: (gsId: string, sId: string) => del(`/api/gameservers/${gsId}/schedules/${sId}`),
  },

  files: {
    list: (gsId: string, path: string) => get<FileEntry[]>(`/api/gameservers/${gsId}/files`, { path }),
    read: (gsId: string, path: string) => get<{ content: string }>(`/api/gameservers/${gsId}/files/content`, { path }),
    write: (gsId: string, path: string, content: string) => putRaw(`/api/gameservers/${gsId}/files/content?path=${encodeURIComponent(path)}`, content),
    delete: (gsId: string, path: string) => del(`/api/gameservers/${gsId}/files`, { path }),
    mkdir: (gsId: string, path: string) => post(`/api/gameservers/${gsId}/files/mkdir`, { path }),
    rename: (gsId: string, from: string, to: string) => post(`/api/gameservers/${gsId}/files/rename`, { from, to }),
    upload: (gsId: string, path: string, file: File, filename?: string) => uploadFile(`/api/gameservers/${gsId}/files/upload`, path, file, filename),
    downloadUrl: (gsId: string, path: string) => `/api/gameservers/${gsId}/files/download?path=${encodeURIComponent(path)}`,
  },

  games: {
    list: () => get<Game[]>('/api/games'),
    get: (id: string) => get<Game>(`/api/games/${id}`),
    options: (gameId: string, key: string) => get<DynamicOption[]>(`/api/games/${gameId}/options/${key}`),
  },

  clusterStatus: {
    get: () => get<any>('/api/status'),
  },

  settings: {
    get: () => get<Record<string, any>>('/api/settings'),
    update: (data: Record<string, any>) => patch<Record<string, any>>('/api/settings', data),
  },

  tokens: {
    list: () => get<Token[]>('/api/tokens'),
    create: (data: any) => post<any>('/api/tokens', data),
    delete: (id: string) => del(`/api/tokens/${id}`),
    generateClaimCode: (id: string) => post<{ claim_code: string }>(`/api/tokens/${id}/claim-code`),
  },

  webhooks: {
    list: () => get<WebhookEndpoint[]>('/api/webhooks'),
    create: (data: any) => post<WebhookEndpoint>('/api/webhooks', data),
    get: (id: string) => get<WebhookEndpoint>(`/api/webhooks/${id}`),
    update: (id: string, data: any) => patch<WebhookEndpoint>(`/api/webhooks/${id}`, data),
    delete: (id: string) => del(`/api/webhooks/${id}`),
    test: (id: string) => post<{ response_status: number; success: boolean }>(`/api/webhooks/${id}/test`),
    deliveries: (id: string) => get<WebhookDelivery[]>(`/api/webhooks/${id}/deliveries`),
  },

  workers: {
    list: () => get<WorkerView[]>('/api/workers'),
    get: (id: string) => get<WorkerView>(`/api/workers/${id}`),
  },

  mods: {
    config: (gsId: string) => get<ModTabConfig>(`/api/gameservers/${gsId}/mods/config`),
    list: (gsId: string) => get<InstalledMod[]>(`/api/gameservers/${gsId}/mods`),
    search: (gsId: string, params: { category: string; q?: string; version?: string; loader?: string; sort?: string; offset?: number; limit?: number }) =>
      get<ModSearchResults>(`/api/gameservers/${gsId}/mods/search`, params),
    versions: (gsId: string, params: { category: string; source: string; source_id: string; unfiltered?: string }) =>
      get<ModVersion[]>(`/api/gameservers/${gsId}/mods/versions`, params),
    updates: (gsId: string) => get<ModUpdate[]>(`/api/gameservers/${gsId}/mods/updates`),
    install: (gsId: string, data: { category: string; source: string; source_id: string; version_id?: string }) =>
      post<InstalledMod>(`/api/gameservers/${gsId}/mods`, data),
    installPack: (gsId: string, data: { source: string; pack_id: string; version_id?: string }) =>
      post<PackInstallResult>(`/api/gameservers/${gsId}/mods/pack`, data),
    uninstall: (gsId: string, modId: string) => del(`/api/gameservers/${gsId}/mods/${modId}`),
    update: (gsId: string, modId: string) => post<InstalledMod>(`/api/gameservers/${gsId}/mods/${modId}/update`),
    updatePack: (gsId: string, modId: string) => post<InstalledMod>(`/api/gameservers/${gsId}/mods/${modId}/update-pack`),
    updateAll: (gsId: string) => post<ModUpdate[]>(`/api/gameservers/${gsId}/mods/update-all`),
    checkCompatibility: (gsId: string, env: Record<string, string>) =>
      post<ModIssue[]>(`/api/gameservers/${gsId}/mods/check-compatibility`, { env }),
    scan: (gsId: string) => post<ScanResult>(`/api/gameservers/${gsId}/mods/scan`),
    trackFile: (gsId: string, data: { category: string; name: string; path: string }) =>
      post<InstalledMod>(`/api/gameservers/${gsId}/mods/track`, data),
    installURL: (gsId: string, data: { category: string; name: string; url: string }) =>
      post<InstalledMod>(`/api/gameservers/${gsId}/mods/url`, data),
    upload: async (gsId: string, category: string, name: string, file: File): Promise<InstalledMod> => {
      const form = new FormData();
      form.append('category', category);
      form.append('name', name);
      form.append('file', file, file.name);
      const headers: Record<string, string> = {};
      const token = getToken();
      if (token) headers['Authorization'] = `Bearer ${token}`;
      const resp = await fetch(basePath + `/api/gameservers/${gsId}/mods/upload`, { method: 'POST', headers, body: form });
      if (!resp.ok) {
        const json = await resp.json().catch(() => ({ error: 'Upload failed' }));
        throw new ApiClientError(resp.status, json.error || 'Upload failed');
      }
      const json = await resp.json();
      return json.data as InstalledMod;
    },
  },

  events: {
    history: (params?: { type?: string; gameserver_id?: string; limit?: number; offset?: number }) =>
      get<Event[]>('/api/events/history', params),
  },
};

export { ApiClientError };
