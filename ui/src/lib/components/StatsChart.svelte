<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { api, type StatsHistoryPoint } from '$lib/api';
  import { gameserverStore } from '$lib/stores';

  let { id }: { id: string } = $props();

  let period = $state<'1h' | '24h' | '7d'>('1h');
  let data = $state<StatsHistoryPoint[]>([]);
  let loading = $state(true);
  let hoverIndex = $state<number | null>(null);
  let refreshInterval: ReturnType<typeof setInterval> | null = null;

  // Live stats from the store (updated by SSE every 5s)
  const gsState = $derived(gameserverStore.getState(id));
  const liveStats = $derived(gsState?.stats ?? null);

  // Track previous cumulative net bytes for rate computation
  let prevNetRx = 0;
  let prevNetTx = 0;
  let prevNetTs = 0;

  // Append live stats to the chart on the 1h view
  $effect(() => {
    if (!liveStats || period !== '1h') return;

    const now = Date.now();
    let rxRate = 0;
    let txRate = 0;

    // Compute net rate from consecutive cumulative byte counters
    if (prevNetTs > 0 && liveStats.net_rx_bytes >= prevNetRx) {
      const dt = (now - prevNetTs) / 1000;
      if (dt > 0) {
        rxRate = (liveStats.net_rx_bytes - prevNetRx) / dt;
        txRate = (liveStats.net_tx_bytes - prevNetTx) / dt;
      }
    }
    prevNetRx = liveStats.net_rx_bytes;
    prevNetTx = liveStats.net_tx_bytes;
    prevNetTs = now;

    const point: StatsHistoryPoint = {
      timestamp: new Date().toISOString(),
      cpu_percent: liveStats.cpu_percent ?? 0,
      memory_usage_mb: liveStats.memory_usage_mb ?? 0,
      memory_limit_mb: liveStats.memory_limit_mb ?? 0,
      net_rx_bytes_per_sec: rxRate,
      net_tx_bytes_per_sec: txRate,
      volume_size_bytes: liveStats.volume_size_bytes ?? 0,
    };

    const current = untrack(() => data);
    data = [...current, point].slice(-720);
  });

  async function fetchData() {
    try {
      data = await api.gameservers.statsHistory(id, period);
    } catch (e) {
      console.warn('StatsChart: failed to load', e);
      data = [];
    }
    loading = false;
  }

  function setupRefresh() {
    if (refreshInterval) clearInterval(refreshInterval);
    // Full refetch periodically to pick up downsampled data and real net rates
    if (period === '1h') refreshInterval = setInterval(fetchData, 30_000);
    else if (period === '24h') refreshInterval = setInterval(fetchData, 60_000);
  }

  function switchPeriod(p: '1h' | '24h' | '7d') {
    period = p;
    loading = true;
    fetchData();
    setupRefresh();
  }

  onMount(() => {
    fetchData();
    setupRefresh();
  });

  onDestroy(() => {
    if (refreshInterval) clearInterval(refreshInterval);
  });

  // --- Chart geometry ---
  const W = 600;
  const H = 80;
  const PAD_L = 0;
  const PAD_R = 0;

  function polyline(points: { x: number; y: number }[]): string {
    if (points.length === 0) return '';
    return points.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
  }

  function scale(values: number[], height: number): { x: number; y: number }[] {
    if (values.length === 0) return [];
    const max = Math.max(...values, 1); // avoid zero max
    const xStep = (W - PAD_L - PAD_R) / Math.max(values.length - 1, 1);
    return values.map((v, i) => ({
      x: PAD_L + i * xStep,
      y: height - (v / max) * (height - 4) - 2,
    }));
  }

  // --- Derived chart data ---
  const cpuPoints = $derived(scale(data.map(d => d.cpu_percent), H));
  const memPoints = $derived(scale(data.map(d => d.memory_usage_mb), H));
  const rxPoints = $derived(scale(data.map(d => d.net_rx_bytes_per_sec), H));
  const txPoints = $derived(scale(data.map(d => d.net_tx_bytes_per_sec), H));

  const cpuMax = $derived(data.length ? Math.max(...data.map(d => d.cpu_percent), 1) : 100);
  const memMax = $derived(data.length ? Math.max(...data.map(d => d.memory_usage_mb), 1) : 1);
  const memLimit = $derived(data.length && data[0].memory_limit_mb > 0 ? data[0].memory_limit_mb : null);
  const netMax = $derived(data.length ? Math.max(...data.map(d => Math.max(d.net_rx_bytes_per_sec, d.net_tx_bytes_per_sec)), 1) : 1);

  // Hover crosshair x position
  const hoverX = $derived(hoverIndex !== null && data.length > 1
    ? PAD_L + hoverIndex * ((W - PAD_L - PAD_R) / Math.max(data.length - 1, 1))
    : null);

  const hoverData = $derived(hoverIndex !== null && hoverIndex < data.length ? data[hoverIndex] : null);

  function formatTime(iso: string): string {
    const d = new Date(iso);
    if (period === '7d') return d.toLocaleDateString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: period === '1h' ? '2-digit' : undefined });
  }

  function formatBytes(bps: number): string {
    if (bps < 1024) return `${Math.round(bps)} B/s`;
    if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(1)} KB/s`;
    return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
  }

  function formatMB(mb: number): string {
    if (mb < 1024) return `${Math.round(mb)} MB`;
    return `${(mb / 1024).toFixed(1)} GB`;
  }

  function handleMouseMove(e: MouseEvent, el: SVGSVGElement) {
    const rect = el.getBoundingClientRect();
    const x = (e.clientX - rect.left) / rect.width * W;
    const step = (W - PAD_L - PAD_R) / Math.max(data.length - 1, 1);
    const idx = Math.round((x - PAD_L) / step);
    hoverIndex = Math.max(0, Math.min(data.length - 1, idx));
  }

  function handleMouseLeave() {
    hoverIndex = null;
  }

  // Grid lines (horizontal) — 3 lines at 25%, 50%, 75%
  const gridYs = [0.25, 0.5, 0.75].map(f => H - f * (H - 4) - 2);
</script>

  <div class="stats-charts">
    <div class="chart-header">
      <span class="chart-title">Resource History</span>
      <div class="period-pills">
        {#each ['1h', '24h', '7d'] as p}
          <button
            class="pill"
            class:active={period === p}
            onclick={() => switchPeriod(p as any)}
          >{p}</button>
        {/each}
      </div>
    </div>

    {#if loading}
      <div class="chart-loading">Loading...</div>
    {:else if data.length === 0}
      <div class="chart-loading">No data for this period yet.</div>
    {:else}

      <!-- CPU -->
      <div class="chart-row">
        <div class="chart-label-col">
          <span class="chart-label cpu">CPU</span>
          <span class="chart-value">
            {hoverData ? `${hoverData.cpu_percent.toFixed(1)}%` : (data.length ? `${data[data.length - 1].cpu_percent.toFixed(1)}%` : '—')}
          </span>
        </div>
        <div class="chart-svg-wrap">
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <svg viewBox="0 0 {W} {H}" preserveAspectRatio="none"
            onmousemove={(e) => handleMouseMove(e, e.currentTarget as SVGSVGElement)}
            onmouseleave={handleMouseLeave}>
            {#each gridYs as gy}
              <line x1="0" y1={gy} x2={W} y2={gy} class="grid-line" />
            {/each}
            <path d={polyline(cpuPoints)} class="line accent" />
            {#if hoverX !== null}
              <line x1={hoverX} y1="0" x2={hoverX} y2={H} class="crosshair" />
              <circle cx={hoverX} cy={cpuPoints[hoverIndex!]?.y ?? 0} r="3" class="dot accent" />
            {/if}
          </svg>
        </div>
      </div>

      <!-- Memory -->
      <div class="chart-row">
        <div class="chart-label-col">
          <span class="chart-label mem">MEM</span>
          <span class="chart-value">
            {hoverData ? formatMB(hoverData.memory_usage_mb) : (data.length ? formatMB(data[data.length - 1].memory_usage_mb) : '—')}
          </span>
        </div>
        <div class="chart-svg-wrap">
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <svg viewBox="0 0 {W} {H}" preserveAspectRatio="none"
            onmousemove={(e) => handleMouseMove(e, e.currentTarget as SVGSVGElement)}
            onmouseleave={handleMouseLeave}>
            {#each gridYs as gy}
              <line x1="0" y1={gy} x2={W} y2={gy} class="grid-line" />
            {/each}
            {#if memLimit}
              <line x1="0" y1={H - (memLimit / memMax) * (H - 4) - 2} x2={W} y2={H - (memLimit / memMax) * (H - 4) - 2} class="limit-line" />
            {/if}
            <path d={polyline(memPoints)} class="line mem" />
            {#if hoverX !== null}
              <line x1={hoverX} y1="0" x2={hoverX} y2={H} class="crosshair" />
              <circle cx={hoverX} cy={memPoints[hoverIndex!]?.y ?? 0} r="3" class="dot mem" />
            {/if}
          </svg>
        </div>
      </div>

      <!-- Network I/O -->
      <div class="chart-row">
        <div class="chart-label-col">
          <span class="chart-label net">NET</span>
          <span class="chart-value">
            {#if hoverData}
              <span class="net-in">In {formatBytes(hoverData.net_rx_bytes_per_sec)}</span>
              <span class="net-out">Out {formatBytes(hoverData.net_tx_bytes_per_sec)}</span>
            {:else if data.length}
              <span class="net-in">In {formatBytes(data[data.length - 1].net_rx_bytes_per_sec)}</span>
              <span class="net-out">Out {formatBytes(data[data.length - 1].net_tx_bytes_per_sec)}</span>
            {:else}
              —
            {/if}
          </span>
        </div>
        <div class="chart-svg-wrap">
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <svg viewBox="0 0 {W} {H}" preserveAspectRatio="none"
            onmousemove={(e) => handleMouseMove(e, e.currentTarget as SVGSVGElement)}
            onmouseleave={handleMouseLeave}>
            {#each gridYs as gy}
              <line x1="0" y1={gy} x2={W} y2={gy} class="grid-line" />
            {/each}
            <path d={polyline(rxPoints)} class="line live" />
            <path d={polyline(txPoints)} class="line caution" />
            {#if hoverX !== null}
              <line x1={hoverX} y1="0" x2={hoverX} y2={H} class="crosshair" />
              <circle cx={hoverX} cy={rxPoints[hoverIndex!]?.y ?? 0} r="3" class="dot live" />
              <circle cx={hoverX} cy={txPoints[hoverIndex!]?.y ?? 0} r="3" class="dot caution" />
            {/if}
          </svg>
        </div>
      </div>

      <!-- Timestamp -->
      <div class="chart-timestamp">
        {#if hoverData}
          {formatTime(hoverData.timestamp)}
        {:else if data.length}
          {formatTime(data[data.length - 1].timestamp)}
        {/if}
      </div>

    {/if}
  </div>

<style>
  .stats-charts {
    padding: 14px 18px 12px;
  }

  .chart-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 16px;
  }

  .chart-title {
    font-size: 0.66rem;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: var(--text-tertiary);
  }

  .period-pills {
    display: flex;
    gap: 2px;
    background: var(--bg-elevated);
    border-radius: 5px;
    padding: 2px;
    border: 1px solid var(--border-dim);
  }

  .pill {
    all: unset;
    font-size: 0.6rem;
    font-family: var(--font-mono);
    padding: 3px 10px;
    border-radius: 3px;
    color: var(--text-tertiary);
    cursor: pointer;
    transition: all 0.15s;
  }
  .pill:hover { color: var(--text-secondary); }
  .pill.active {
    background: var(--accent-dim);
    color: var(--accent);
  }

  .chart-loading {
    font-size: 0.72rem;
    color: var(--text-tertiary);
    font-family: var(--font-mono);
    padding: 20px 0;
    text-align: center;
  }

  .chart-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 8px;
  }

  .chart-label-col {
    width: 72px;
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .chart-label {
    font-size: 0.56rem;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--text-tertiary);
  }
  .chart-label.cpu { color: var(--accent); }
  .chart-label.mem { color: #8b5cf6; }
  .chart-label.net { color: var(--live); }

  .chart-value {
    font-size: 0.68rem;
    font-family: var(--font-mono);
    font-weight: 500;
    color: var(--text-secondary);
    line-height: 1.3;
    font-variant-numeric: tabular-nums;
  }

  .net-in, .net-out {
    display: block;
    font-size: 0.62rem;
  }
  .net-in { color: var(--live); }
  .net-out { color: var(--caution); }

  .chart-svg-wrap {
    flex: 1;
    min-width: 0;
    height: 48px;
    background: rgba(255, 255, 255, 0.02);
    border-radius: var(--radius-sm);
    padding: 6px 8px;
  }

  .chart-svg-wrap svg {
    width: 100%;
    height: 100%;
    display: block;
    cursor: crosshair;
  }

  .chart-svg-wrap :global(.grid-line) {
    stroke: var(--border-dim);
    stroke-width: 0.5;
  }

  .chart-svg-wrap :global(.limit-line) {
    stroke: var(--danger);
    stroke-width: 0.5;
    stroke-dasharray: 4 4;
    opacity: 0.4;
  }

  .chart-svg-wrap :global(.line) {
    fill: none;
    stroke-width: 1.5;
    vector-effect: non-scaling-stroke;
  }
  .chart-svg-wrap :global(.line.accent) { stroke: var(--accent); }
  .chart-svg-wrap :global(.line.mem) { stroke: #8b5cf6; }
  .chart-svg-wrap :global(.line.live) { stroke: var(--live); }
  .chart-svg-wrap :global(.line.caution) { stroke: var(--caution); opacity: 0.6; }

  .chart-svg-wrap :global(.crosshair) {
    stroke: var(--text-tertiary);
    stroke-width: 0.5;
    vector-effect: non-scaling-stroke;
    opacity: 0.5;
  }

  .chart-svg-wrap :global(.dot) {
    vector-effect: non-scaling-stroke;
  }
  .chart-svg-wrap :global(.dot.accent) { fill: var(--accent); }
  .chart-svg-wrap :global(.dot.mem) { fill: #8b5cf6; }
  .chart-svg-wrap :global(.dot.live) { fill: var(--live); }
  .chart-svg-wrap :global(.dot.caution) { fill: var(--caution); }

  .chart-timestamp {
    text-align: right;
    font-size: 0.58rem;
    font-family: var(--font-mono);
    color: var(--text-tertiary);
    margin-top: 2px;
    opacity: 0.7;
  }

  @media (max-width: 700px) {
    .chart-label-col { width: 54px; }
    .chart-value { font-size: 0.6rem; }
  }
</style>
