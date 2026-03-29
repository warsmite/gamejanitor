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

  let eventSource: EventSource | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let backoff = 1000;
  let destroyed = false;

  const gsState = $derived(gameserverStore.getState(id));
  const status = $derived(gsState?.gameserver?.status ?? 'stopped');
  const isStreamable = $derived(
    ['installing', 'starting', 'started', 'running', 'stopping'].includes(status)
  );

  // Auto-connect/reconnect when status becomes streamable
  $effect(() => {
    if (isStreamable && !eventSource && !destroyed) {
      connectStream();
    }
  });

  onMount(async () => {
    // Load historical logs first (works even when server is stopped)
    await loadHistoricalLogs();
    if (isStreamable) connectStream();
    inputEl?.focus();
  });

  onDestroy(() => {
    destroyed = true;
    disconnect();
  });

  async function loadHistoricalLogs() {
    try {
      const result = await api.gameservers.logs(id, 200);
      if (result.lines && result.lines.length > 0) {
        lines = result.lines;
        await tick();
        scrollToBottom();
      }
    } catch {
      // Server may not have logs yet — not an error
    }
  }

  function connectStream() {
    if (eventSource || destroyed) return;

    // On reconnect, only request tail if we have no lines yet (avoid duplicates)
    const tail = lines.length === 0 ? 200 : 0;
    const url = basePath + `/api/gameservers/${id}/logs/stream?tail=${tail}`;
    eventSource = new EventSource(url);
    backoff = 1000;

    eventSource.onopen = () => {
      connected = true;
      backoff = 1000;
    };

    eventSource.onmessage = async (e) => {
      lines.push(e.data);
      // Prevent unbounded memory growth
      if (lines.length > 2000) lines = lines.slice(-1500);
      if (autoScroll) {
        await tick();
        scrollToBottom();
      }
    };

    eventSource.onerror = () => {
      disconnect();
      scheduleReconnect();
    };
  }

  function disconnect() {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
    connected = false;
  }

  function scheduleReconnect() {
    if (destroyed || reconnectTimer) return;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (!destroyed && isStreamable) connectStream();
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
    <span class="console-status" class:live={connected}>{connected ? 'Live' : 'Disconnected'}</span>
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
  .console-status {
    font-family: var(--font-mono);
    font-size: 0.66rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
  }
  .console-status.live { color: var(--live); }

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
