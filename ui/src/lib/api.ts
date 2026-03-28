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

  if (resp.status === 204) return undefined as T;

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

async function uploadFile(path: string, filePath: string, file: File): Promise<void> {
  const form = new FormData();
  form.append('path', filePath);
  form.append('file', file);

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

export interface Gameserver {
  id: string;
  name: string;
  game_id: string;
  status: string;
  error_reason?: string;
  ports: any;
  env: any;
  memory_limit_mb: number;
  cpu_limit: number;
  cpu_enforced: boolean;
  storage_limit_mb?: number;
  backup_limit?: number;
  container_id?: string;
  volume_name: string;
  port_mode: string;
  node_id?: string;
  node?: GameserverNode;
  node_tags: Record<string, string>;
  sftp_username: string;
  installed: boolean;
  auto_restart: boolean;
  connection_address?: string;
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
  volume_size_bytes: number;
  storage_limit_mb?: number;
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
  mods?: { categories?: any[] }; // TODO: proper types when UI is rebuilt
}

// --- Mod system types (TODO: rebuild UI) ---

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

export interface Activity {
  id: string;
  gameserver_id: string | null;
  worker_id: string;
  type: string;
  status: string;
  actor: any;
  data: any;
  error: string;
  started_at: string;
  completed_at: string | null;
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
    bulk: (action: string, ids: string[]) => post<any>('/api/gameservers/bulk', { action, all: ids.length === 0 }),
    regenerateSftpPassword: (id: string) => post<{ sftp_password: string }>(`/api/gameservers/${id}/regenerate-sftp-password`),
    status: (id: string) => get<GameserverStatus>(`/api/gameservers/${id}/status`),
    stats: (id: string) => get<GameserverStats>(`/api/gameservers/${id}/stats`),
    query: (id: string) => get<QueryData>(`/api/gameservers/${id}/query`),
    logs: (id: string, tail?: number) => get<{ lines: string[]; historical?: boolean }>(`/api/gameservers/${id}/logs`, { tail }),
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
    upload: (gsId: string, path: string, file: File) => uploadFile(`/api/gameservers/${gsId}/files/upload`, path, file),
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

  // mods: TODO — rebuild UI properly

  events: {
    history: (params?: { type?: string; gameserver_id?: string; limit?: number; offset?: number }) =>
      get<Activity[]>('/api/events/history', params),
  },
};

export { ApiClientError };
