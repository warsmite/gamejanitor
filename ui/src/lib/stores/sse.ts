// Single SSE connection to /api/events.
// Components subscribe to filtered events. Reconnects with backoff on failure.

import { basePath } from '../base';
import { toast } from './toasts';

type EventCallback = (data: any) => void;

interface Subscription {
  id: number;
  type: string | null; // null = all events
  gameserverId: string | null; // null = all gameservers
  callback: EventCallback;
}

let nextSubId = 0;
let eventSource: EventSource | null = null;
let subscriptions: Subscription[] = [];
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let backoff = 1000;
const maxBackoff = 30000;

export function connect() {
  if (eventSource) return;

  eventSource = new EventSource(basePath + '/api/events');

  eventSource.onopen = () => {
    backoff = 1000; // reset on successful connect
  };

  eventSource.onerror = () => {
    eventSource?.close();
    eventSource = null;

    // Reconnect with exponential backoff
    if (reconnectTimer) clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => {
      connect();
    }, backoff);
    backoff = Math.min(backoff * 2, maxBackoff);
  };

  // Listen for all named events by overriding onmessage for unnamed,
  // and using addEventListener for known event types
  eventSource.onmessage = (e) => {
    dispatch(null, e.data);
  };

  // SSE sends named events (event: type\ndata: json)
  // We need to listen for each event type, but we don't know them all upfront.
  // Instead, use a MutationObserver-style approach: listen to raw messages.
  // Actually, EventSource only fires 'message' for unnamed events.
  // For named events, we need to add listeners. Since we know the event types
  // from the API, register them all.
  const eventTypes = [
    'gameserver.create', 'gameserver.update', 'gameserver.delete',
    'gameserver.start', 'gameserver.stop', 'gameserver.restart',
    'gameserver.update_game', 'gameserver.reinstall', 'gameserver.migrate',
    'gameserver.depot_downloading',
    'gameserver.depot_complete', 'gameserver.depot_cached',
    'gameserver.operation',
    'gameserver.image_pulling', 'gameserver.image_pulled',
    'gameserver.container_creating', 'gameserver.container_started',
    'gameserver.ready', 'gameserver.container_stopping', 'gameserver.container_stopped',
    'gameserver.container_exited', 'gameserver.error',
    'backup.create', 'backup.delete', 'backup.restore',
    'backup.completed', 'backup.failed',
    'backup.restore.completed', 'backup.restore.failed',
    'worker.connected', 'worker.disconnected',
    'schedule.create', 'schedule.update', 'schedule.delete',
    'schedule.task.completed', 'schedule.task.failed',
    'gameserver.stats', 'gameserver.query',
    'mod.installed', 'mod.uninstalled',
  ];

  for (const type of eventTypes) {
    eventSource.addEventListener(type, (e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data);
        dispatch(type, data);
      } catch (err) {
        console.warn('sse: malformed event data for', type, err);
      }
    });
  }
}

export function disconnect() {
  if (reconnectTimer) clearTimeout(reconnectTimer);
  eventSource?.close();
  eventSource = null;
}

function dispatch(type: string | null, data: any) {
  // Parse if string
  if (typeof data === 'string') {
    try { data = JSON.parse(data); } catch (e) { console.warn('sse: failed to parse event data', e); return; }
  }

  const eventType = type || data?.type;
  if (eventType && !data?.type) {
    data.type = eventType;
  }
  const gameserverId = data?.gameserver_id;

  for (const sub of subscriptions) {
    // Filter by event type
    if (sub.type && sub.type !== eventType) {
      // Check glob match (e.g. "gameserver.*")
      if (!sub.type.endsWith('.*') || !eventType?.startsWith(sub.type.slice(0, -1))) {
        continue;
      }
    }
    // Filter by gameserver ID
    if (sub.gameserverId && sub.gameserverId !== gameserverId) continue;

    sub.callback(data);
  }
}

// Subscribe to events. Returns an unsubscribe function.
export function onEvent(type: string | null, callback: EventCallback): () => void {
  const id = nextSubId++;
  subscriptions.push({ id, type, gameserverId: null, callback });
  return () => { subscriptions = subscriptions.filter(s => s.id !== id); };
}

// Subscribe to events for a specific gameserver.
export function onGameserverEvent(gsId: string, callback: EventCallback): () => void {
  const id = nextSubId++;
  subscriptions.push({ id, type: null, gameserverId: gsId, callback });
  return () => { subscriptions = subscriptions.filter(s => s.id !== id); };
}

// Auto-toast for important events
let autoToastsEnabled = false;
export function enableAutoToasts() {
  if (autoToastsEnabled) return;
  autoToastsEnabled = true;
  // Only toast for async outcomes and errors — status changes are visible in the UI
  onEvent('gameserver.error', (data) => {
    toast(`Server error: ${data.reason || 'unknown'}`, 'error');
  });

  onEvent('backup.completed', () => {
    toast('Backup completed', 'success');
  });

  onEvent('backup.failed', (data) => {
    toast(`Backup failed: ${data.error || 'unknown error'}`, 'error');
  });

  onEvent('backup.restore.completed', () => {
    toast('Restore completed', 'success');
  });

  onEvent('backup.restore.failed', (data) => {
    toast(`Restore failed: ${data.error || 'unknown error'}`, 'error');
  });
}
