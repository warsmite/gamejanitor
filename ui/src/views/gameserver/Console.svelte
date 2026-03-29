<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { api } from '$lib/api';
  import { basePath } from '$lib/base';
  import { toast, gameserverStore } from '$lib/stores';

  let { id }: { id: string } = $props();

  let lines = $state<string[]>([]);
  let command = $state('');
  let consoleEl: HTMLDivElement;
  let inputEl: HTMLInputElement;
  let autoScroll = $state(true);
  let connected = $state(false);
  let commandHistory = $state<string[]>([]);
  let historyIndex = $state(-1);

  // Session history
  let sessions = $state<{ index: number; mod_time: string }[]>([]);
  let activeSession = $state<number | null>(null); // null = live, number = historical

  let eventSource: EventSource | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let backoff = 1000;
  let destroyed = false;

  // Batch incoming lines to avoid per-message DOM updates during log floods
  let pendingLines: string[] = [];
  let flushScheduled = false;

  const gsState = $derived(gameserverStore.getState(id));
  const status = $derived(gsState?.gameserver?.status ?? 'stopped');
  const isStreamable = $derived(
    ['installing', 'starting', 'started', 'running', 'stopping'].includes(status)
  );

  // React to status changes: connect when streamable, disconnect when not
  $effect(() => {
    if (destroyed || activeSession !== null) return;
    if (isStreamable && !eventSource) {
      connectStream();
    } else if (!isStreamable && eventSource) {
      disconnect();
    }
  });

  onMount(async () => {
    await loadSessions();
    await loadHistoricalLogs();
    if (isStreamable) connectStream();
    inputEl?.focus();
  });

  onDestroy(() => {
    destroyed = true;
    disconnect();
  });

  async function loadSessions() {
    try {
      sessions = await api.gameservers.logSessions(id);
    } catch {
      sessions = [];
    }
  }

  async function loadHistoricalLogs(session?: number) {
    try {
      const result = await api.gameservers.logs(id, { tail: 500, session });
      if (result.lines && result.lines.length > 0) {
        lines = result.lines;
        await tick();
        scrollToBottom();
      }
    } catch {
      // Server may not have logs yet
    }
  }

  async function switchSession(session: number | null) {
    if (session === activeSession) return;

    activeSession = session;
    lines = [];

    if (session === null) {
      // Back to live
      await loadHistoricalLogs();
      if (isStreamable) connectStream();
    } else {
      // View historical session
      disconnect();
      await loadHistoricalLogs(session);
    }
  }

  function connectStream() {
    if (eventSource || destroyed || activeSession !== null) return;

    const tail = lines.length === 0 ? 200 : 0;
    const url = basePath + `/api/gameservers/${id}/logs/stream?tail=${tail}`;
    eventSource = new EventSource(url);
    backoff = 1000;

    eventSource.onopen = () => {
      connected = true;
      backoff = 1000;
    };

    eventSource.onmessage = (e) => {
      pendingLines.push(e.data);
      if (!flushScheduled) {
        flushScheduled = true;
        requestAnimationFrame(flushLines);
      }
    };

    // EventSource fires onerror on both network failure and server-side close.
    // Either way, disconnect and let the $effect reconnect when status is streamable.
    eventSource.onerror = () => {
      disconnect();
      // Only schedule reconnect if server is still supposed to be running —
      // if it stopped, the $effect handles reconnection when it starts again.
      if (isStreamable) scheduleReconnect();
    };
  }

  async function flushLines() {
    flushScheduled = false;
    if (pendingLines.length === 0) return;

    // Apply all pending lines in one batch
    const batch = pendingLines;
    pendingLines = [];

    lines.push(...batch);
    if (lines.length > 2000) lines = lines.slice(-1500);

    if (autoScroll) {
      await tick();
      scrollToBottom();
    }
  }

  function disconnect() {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
    connected = false;
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  function scheduleReconnect() {
    if (destroyed || reconnectTimer || activeSession !== null) return;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (!destroyed && isStreamable && activeSession === null) connectStream();
    }, backoff);
    backoff = Math.min(backoff * 2, 15000);
  }

  function scrollToBottom() {
    if (consoleEl) {
      consoleEl.scrollTop = consoleEl.scrollHeight;
    }
  }

  function handleScroll() {
    if (!consoleEl) return;
    const atBottom = consoleEl.scrollHeight - consoleEl.scrollTop - consoleEl.clientHeight < 30;
    autoScroll = atBottom;
  }

  async function sendCommand() {
    const cmd = command.trim();
    if (!cmd) return;

    commandHistory = [cmd, ...commandHistory.slice(0, 49)];
    historyIndex = -1;
    command = '';

    try {
      await api.gameservers.command(id, cmd);
    } catch (e: any) {
      toast(`Command failed: ${e.message}`, 'error');
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      sendCommand();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (historyIndex < commandHistory.length - 1) {
        historyIndex++;
        command = commandHistory[historyIndex];
      }
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (historyIndex > 0) {
        historyIndex--;
        command = commandHistory[historyIndex];
      } else {
        historyIndex = -1;
        command = '';
      }
    }
  }

  function clearConsole() {
    lines = [];
  }

  function formatSessionTime(iso: string): string {
    return new Date(iso).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  function lineClass(line: string): string {
    const lower = line.toLowerCase();
    if (lower.includes('warn') || lower.includes("can't keep up")) return 'log-warn';
    if (lower.includes('error') || lower.includes('exception')) return 'log-error';
    if (lower.includes('joined the game') || lower.includes('logged in with entity')) return 'log-join';
    if (lower.includes('left the game') || lower.includes('lost connection')) return 'log-leave';
    if (/^<\w+>/.test(line.replace(/^\[[\d:]+\]\s*(\[.*?\]\s*)*/, ''))) return 'log-chat';
    if (lower.includes('[server]') && (lower.includes('starting') || lower.includes('done'))) return 'log-system';
    return 'log-info';
  }
</script>

<div class="console-wrap">
  <div class="console-header">
    <div class="header-left">
      {#if activeSession === null}
        <span class="console-status" class:live={connected}>{connected ? 'Live' : 'Disconnected'}</span>
      {:else}
        <span class="console-status">Session {activeSession}</span>
      {/if}
      {#if sessions.length > 0}
        <select class="session-select" value={activeSession ?? 'live'} onchange={(e) => {
          const v = (e.target as HTMLSelectElement).value;
          switchSession(v === 'live' ? null : parseInt(v));
        }}>
          <option value="live">Live</option>
          {#each sessions as s}
            <option value={s.index}>
              {s.index === 0 ? 'Current' : `Session ${s.index}`} — {formatSessionTime(s.mod_time)}
            </option>
          {/each}
        </select>
      {/if}
    </div>
    <button class="console-clear" onclick={clearConsole}>Clear</button>
  </div>
  <div class="console-output" bind:this={consoleEl} onscroll={handleScroll}>
    {#each lines as line}
      <div class="log-line {lineClass(line)}">{line}</div>
    {:else}
      <div class="log-empty">No log output yet. Start the server to see console output.</div>
    {/each}
  </div>
  <div class="console-input-bar">
    <span class="console-prompt">&gt;</span>
    <input
      class="console-input"
      type="text"
      placeholder="Type a command..."
      bind:value={command}
      bind:this={inputEl}
      onkeydown={handleKeydown}
      disabled={activeSession !== null}
    >
  </div>
</div>

<style>
  .console-wrap {
    background: var(--bg-inset);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
    display: flex; flex-direction: column;
    height: 520px;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .console-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-dim);
    flex-shrink: 0;
  }
  .header-left {
    display: flex; align-items: center; gap: 12px;
  }
  .console-status {
    font-family: var(--font-mono);
    font-size: 0.66rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
  }
  .console-status.live { color: var(--live); }

  .session-select {
    font-family: var(--font-mono);
    font-size: 0.68rem;
    background: var(--bg-surface);
    color: var(--text-secondary);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    padding: 3px 8px;
    cursor: pointer;
    outline: none;
  }
  .session-select:hover { border-color: var(--border); }
  .session-select:focus { border-color: var(--accent); }

  .console-output {
    flex: 1;
    overflow-y: auto;
    padding: 12px 18px;
    font-family: var(--font-mono);
    font-size: 0.78rem;
    line-height: 1.65;
    color: var(--text-secondary);
  }
  .console-output::-webkit-scrollbar { width: 5px; }
  .console-output::-webkit-scrollbar-track { background: transparent; }
  .console-output::-webkit-scrollbar-thumb { background: var(--border); border-radius: 3px; }

  .log-line { white-space: pre-wrap; word-break: break-all; }
  .log-info { color: var(--text-secondary); }
  .log-warn { color: var(--caution); }
  .log-error { color: var(--danger); }
  .log-join { color: var(--live); }
  .log-leave { color: var(--idle); }
  .log-chat { color: var(--text-primary); }
  .log-system { color: var(--accent); opacity: 0.7; }
  .log-empty { color: var(--text-tertiary); padding: 20px 0; }

  .console-input-bar {
    display: flex; align-items: center;
    padding: 0;
    border-top: 1px solid var(--border-dim);
    background: #07070a;
    flex-shrink: 0;
  }
  .console-prompt {
    padding: 0 0 0 16px;
    font-family: var(--font-mono);
    font-size: 0.82rem;
    color: var(--accent);
    user-select: none;
    flex-shrink: 0;
  }
  .console-input {
    flex: 1;
    padding: 12px 8px;
    background: transparent;
    border: none; outline: none;
    font-family: var(--font-mono);
    font-size: 0.82rem;
    color: var(--text-primary);
  }
  .console-input::placeholder { color: var(--text-tertiary); opacity: 0.4; }
  .console-input:disabled { opacity: 0.3; }

  .console-clear {
    padding: 4px 10px;
    border-radius: 4px;
    background: none; border: 1px solid var(--border-dim);
    color: var(--text-tertiary);
    font-family: var(--font-mono);
    font-size: 0.66rem; cursor: pointer;
    transition: color 0.15s, border-color 0.15s;
    flex-shrink: 0;
  }
  .console-clear:hover { color: var(--text-secondary); border-color: var(--border); }

  @media (max-width: 700px) {
    .console-wrap { height: 400px; }
  }
</style>
