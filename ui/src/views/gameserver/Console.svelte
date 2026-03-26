<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { api } from '$lib/api';
  import { toast } from '$lib/stores';

  let { id }: { id: string } = $props();

  let lines = $state<string[]>([]);
  let command = $state('');
  let consoleEl: HTMLDivElement;
  let inputEl: HTMLInputElement;
  let autoScroll = $state(true);
  let commandHistory = $state<string[]>([]);
  let historyIndex = $state(-1);

  let pollInterval: ReturnType<typeof setInterval>;

  onMount(async () => {
    // Load initial logs
    try {
      const result = await api.gameservers.logs(id, 200);
      lines = result.lines || [];
      await tick();
      scrollToBottom();
    } catch (e) { console.warn('Console: failed to load initial logs', e); }

    // Poll for new logs (SSE doesn't stream log lines, only events)
    pollInterval = setInterval(fetchLogs, 2000);

    // Focus the input
    inputEl?.focus();
  });

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
  });

  async function fetchLogs() {
    try {
      const result = await api.gameservers.logs(id, 200);
      if (result.lines && result.lines.length > 0) {
        const newLines = result.lines;
        // Only update if content changed
        if (newLines.length !== lines.length || newLines[newLines.length - 1] !== lines[lines.length - 1]) {
          lines = newLines;
          if (autoScroll) {
            await tick();
            scrollToBottom();
          }
        }
      }
    } catch (e) { console.warn('Console: failed to fetch logs', e); }
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

  // Classify log lines for coloring
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
    <button class="console-clear" onclick={clearConsole}>Clear</button>
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

  .console-output {
    flex: 1;
    overflow-y: auto;
    padding: 16px 18px;
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
    padding: 6px 12px; margin-right: 10px;
    border-radius: 4px;
    background: none; border: 1px solid var(--border-dim);
    color: var(--text-tertiary);
    font-family: var(--font-mono);
    font-size: 0.68rem; cursor: pointer;
    transition: color 0.15s, border-color 0.15s;
    flex-shrink: 0;
  }
  .console-clear:hover { color: var(--text-secondary); border-color: var(--border); }

  @media (max-width: 700px) {
    .console-wrap { height: 400px; }
  }
</style>
