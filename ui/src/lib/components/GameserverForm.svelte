<script lang="ts">
  import type { Game, EnvVar, DynamicOption } from '$lib/api';
  import { ResourceSlider, EnvEditor } from '$lib/components';

  let {
    game,
    serverName = $bindable(''),
    memoryMb = $bindable(2048),
    storageLimitMb = $bindable(0),
    cpuLimit = $bindable(0),
    cpuEnforced = $bindable(false),
    backupLimit = $bindable(0),
    portMode = $bindable('auto'),
    manualPorts = $bindable([]),
    autoRestart = $bindable(true),
    envValues = $bindable({}),
    dynamicOptions = {},
    globalMaxBackups = 10,
    showNameField = true,
  }: {
    game: Game;
    serverName?: string;
    memoryMb?: number;
    storageLimitMb?: number;
    cpuLimit?: number;
    cpuEnforced?: boolean;
    backupLimit?: number;
    portMode?: string;
    manualPorts?: { name: string; host_port: number; instance_port: number; protocol: string }[];
    autoRestart?: boolean;
    envValues?: Record<string, string>;
    dynamicOptions?: Record<string, DynamicOption[]>;
    globalMaxBackups?: number;
    showNameField?: boolean;
  } = $props();

  let advancedOpen = $state(false);

  const belowRecommended = $derived(memoryMb > 0 && memoryMb < game.recommended_memory_mb);
  const cpuDisplay = $derived(cpuLimit === 0 ? 'Unlimited' : `${cpuLimit} cores`);
  const backupDisplay = $derived(backupLimit === 0 ? `Global (${globalMaxBackups})` : `${backupLimit} max`);

  function memoryLabel(mb: number): string {
    if (mb === 0) return 'Unlimited';
    if (mb < 1024) return `${mb} MB`;
    return `${(mb / 1024).toFixed(mb % 1024 === 0 ? 0 : 1)} GB`;
  }

  function storageLabel(mb: number): string {
    if (mb === 0) return 'Unlimited';
    if (mb >= 1024000) return '1 TB';
    if (mb >= 1024) return `${Math.round(mb / 1024)} GB`;
    return `${mb} MB`;
  }

  // Group env vars: required+notice first, then by group, then ungrouped
  const envGroups = $derived(() => {
    const userEnvs = game.default_env.filter((e: EnvVar) => !e.system);
    const groups: { name: string; vars: EnvVar[] }[] = [];
    const grouped = new Map<string, EnvVar[]>();
    const ungrouped: EnvVar[] = [];

    for (const env of userEnvs) {
      if (env.group) {
        if (!grouped.has(env.group)) grouped.set(env.group, []);
        grouped.get(env.group)!.push(env);
      } else {
        ungrouped.push(env);
      }
    }

    const required = ungrouped.filter((e: EnvVar) => e.required);
    const optional = ungrouped.filter((e: EnvVar) => !e.required);

    if (required.length > 0) groups.push({ name: '', vars: required });
    for (const [name, vars] of grouped) groups.push({ name, vars });
    if (optional.length > 0) groups.push({ name: '', vars: optional });

    return groups;
  });
</script>

<div class="gs-form">
  <!-- Server Name -->
  {#if showNameField}
    <div class="form-row">
      <label class="label label-required">Server Name</label>
      <input class="input" type="text" placeholder="e.g. survival-smp" bind:value={serverName}>
    </div>
  {/if}

  <!-- Memory -->
  <div class="form-row">
    <ResourceSlider label="Memory" bind:value={memoryMb} min={0} max={16384} step={256} display={memoryLabel} />
    <div class="resource-warning" class:visible={belowRecommended}>
      <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8.982 1.566a1.13 1.13 0 0 0-1.96 0L.165 13.233c-.457.778.091 1.767.98 1.767h13.713c.889 0 1.438-.99.98-1.767L8.982 1.566zM8 5c.535 0 .954.462.9.995l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 5.995A.905.905 0 0 1 8 5zm.002 6a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>
      Below recommended ({game.recommended_memory_mb >= 1024 ? `${game.recommended_memory_mb / 1024} GB` : `${game.recommended_memory_mb} MB`})
    </div>
  </div>

  <!-- Storage -->
  <div class="form-row">
    <ResourceSlider label="Storage" bind:value={storageLimitMb} min={0} max={1024000} step={10240} display={storageLabel} />
    <div class="field-hint">Soft limit — used for placement and monitoring</div>
  </div>

  <!-- Required env vars with notices (e.g. EULA) -->
  {#each envGroups() as group}
    {#each group.vars.filter((v: EnvVar) => v.required && v.notice) as env}
      <div class="eula-callout">
        <div class="eula-left">
          <div class="eula-title">{env.label || env.key}</div>
          <div class="eula-notice">{@html env.notice?.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank">$1</a>') || ''}</div>
          <div class="eula-required">Required</div>
        </div>
        <button class="toggle" class:on={envValues[env.key] === 'true'} onclick={() => envValues[env.key] = envValues[env.key] === 'true' ? 'false' : 'true'}></button>
      </div>
    {/each}
  {/each}

  <hr class="form-divider">

  <!-- Game Settings: env vars grouped -->
  <EnvEditor envDefs={game.default_env} bind:values={envValues} {dynamicOptions} gridClass="form-grid" />

  <!-- Advanced -->
  <button class="advanced-toggle" onclick={() => advancedOpen = !advancedOpen}>
    <svg class="adv-chevron" class:open={advancedOpen} viewBox="0 0 16 16" fill="currentColor"><path fill-rule="evenodd" d="M4.646 1.646a.5.5 0 0 1 .708 0l6 6a.5.5 0 0 1 0 .708l-6 6a.5.5 0 0 1-.708-.708L10.293 8 4.646 2.354a.5.5 0 0 1 0-.708z"/></svg>
    Advanced
    {#if !advancedOpen}
      <span class="adv-hint">Ports, resources, placement...</span>
    {/if}
  </button>

  {#if advancedOpen}
    <div class="advanced-content">
      <!-- Ports -->
      <div class="env-group">
        <div class="env-group-label">Ports</div>
        <div class="form-row">
          <label class="label">Manual Port Assignment</label>
          <div class="toggle-row">
            <button class="toggle" class:on={portMode === 'manual'} onclick={() => portMode = portMode === 'auto' ? 'manual' : 'auto'}></button>
            <span class="toggle-label">{portMode === 'auto' ? 'Auto (allocated from port range)' : 'Manual'}</span>
          </div>
        </div>
        {#if portMode === 'manual'}
          <div class="port-table" style="margin-top:14px;">
            {#each manualPorts as port, i}
              <div class="port-row">
                <span class="port-name">{port.name}</span>
                <div class="port-field">
                  <label class="port-label">Host</label>
                  <input class="input input-mono" type="number" style="width:100px;" bind:value={manualPorts[i].host_port}>
                </div>
                <div class="port-field">
                  <label class="port-label">Instance</label>
                  <input class="input input-mono" type="number" style="width:100px;" bind:value={manualPorts[i].instance_port}>
                </div>
                <span class="port-proto">{port.protocol}</span>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Resources -->
      <div class="env-group">
        <div class="env-group-label">Resources</div>
        <div class="form-grid">
          <div class="form-row">
            <div class="resource-header">
              <span class="label">CPU Limit</span>
              <span class="resource-value dim">{cpuDisplay}</span>
            </div>
            <div class="input-with-suffix">
              <input class="input input-mono" type="number" min="0" step="0.5" placeholder="Unlimited"
                value={cpuLimit || ''}
                oninput={(e) => cpuLimit = parseFloat((e.target as HTMLInputElement).value) || 0}>
              <span class="input-suffix">cores</span>
            </div>
          </div>
          <div class="form-row">
            <label class="label">Enforce CPU Limit</label>
            <div class="toggle-row">
              <button class="toggle" class:on={cpuEnforced} onclick={() => cpuEnforced = !cpuEnforced}></button>
              <span class="toggle-label">{cpuEnforced ? 'Hard limit (Docker enforced)' : 'Soft limit (scheduling only)'}</span>
            </div>
          </div>
        </div>
        <div class="form-grid" style="margin-top:14px;">
          <div class="form-row">
            <div class="resource-header">
              <span class="label">Backup Limit</span>
              <span class="resource-value dim">{backupDisplay}</span>
            </div>
            <div class="input-with-suffix">
              <input class="input input-mono" type="number" min="0" placeholder={`Global (${globalMaxBackups})`}
                value={backupLimit || ''}
                oninput={(e) => backupLimit = parseInt((e.target as HTMLInputElement).value) || 0}>
              <span class="input-suffix">max</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Placement -->
      <div class="env-group">
        <div class="env-group-label">Placement</div>
        <div class="form-grid">
          <div class="form-row">
            <label class="label">Node ID</label>
            <input class="input input-mono" type="text" placeholder="Auto (best available)" disabled>
            <span class="field-hint">Single-node mode — auto placement</span>
          </div>
          <div class="form-row">
            <label class="label">Node Tags</label>
            <input class="input input-mono" type="text" placeholder="e.g. ssd, eu-west" disabled>
            <span class="field-hint">Requires multi-node deployment</span>
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- Auto-restart -->
  <div class="form-row" style="margin-top:8px;">
    <div class="toggle-row">
      <button class="toggle" class:on={autoRestart} onclick={() => autoRestart = !autoRestart}></button>
      <span class="toggle-label">Auto-restart on crash</span>
    </div>
  </div>
</div>

<style>
  .gs-form { position: relative; z-index: 1; }

  .form-row { margin-bottom: 18px; }
  :global(.form-grid) { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .form-divider { border: none; border-top: 1px solid var(--border-dim); margin: 22px 0; }

  .resource-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .resource-header .label { margin-bottom: 0; }
  .resource-value { font-size: 0.78rem; font-family: var(--font-mono); font-weight: 500; color: var(--text-primary); }
  .resource-value.dim { color: var(--text-tertiary); font-size: 0.72rem; }
  .resource-warning { font-size: 0.72rem; color: var(--accent); margin-top: 6px; display: flex; align-items: center; gap: 5px; visibility: hidden; }
  .resource-warning.visible { visibility: visible; }
  .resource-warning svg { width: 12px; height: 12px; flex-shrink: 0; }

  .field-hint { font-size: 0.68rem; color: var(--text-tertiary); opacity: 0.6; margin-top: 4px; }

  .toggle-label { font-size: 0.78rem; color: var(--text-tertiary); }

  .eula-callout { background: var(--bg-elevated); border: 1px solid rgba(232,114,42,0.15); border-radius: var(--radius); padding: 16px 18px; margin-bottom: 18px; display: flex; align-items: center; justify-content: space-between; gap: 16px; position: relative; overflow: hidden; }
  .eula-callout::before { content: ''; position: absolute; top: 0; left: 10%; right: 10%; height: 1px; background: linear-gradient(90deg, transparent, var(--accent), transparent); opacity: 0.15; }
  .eula-left { display: flex; flex-direction: column; gap: 4px; }
  .eula-title { font-size: 0.84rem; font-weight: 550; }
  .eula-notice { font-size: 0.73rem; color: var(--text-tertiary); line-height: 1.4; }
  .eula-notice :global(a) { color: var(--accent); text-decoration: none; }
  .eula-notice :global(a:hover) { text-decoration: underline; }
  .eula-required { font-size: 0.62rem; font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.08em; color: var(--accent); opacity: 0.8; }

  .input-with-suffix { position: relative; }
  .input-with-suffix input { padding-right: 50px; }
  .input-suffix { position: absolute; right: 12px; top: 50%; transform: translateY(-50%); font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); pointer-events: none; }

  .env-group { background: var(--bg-elevated); border: 1px solid var(--border-dim); border-left: 2px solid rgba(232,114,42,0.2); border-radius: var(--radius); padding: 18px; margin-bottom: 14px; }
  .env-group-label { font-size: 0.66rem; font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.1em; color: var(--text-tertiary); margin-bottom: 14px; }

  .port-row { display: flex; align-items: center; gap: 12px; padding: 8px 0; }
  .port-row + .port-row { border-top: 1px solid var(--border-dim); }
  .port-name { font-size: 0.82rem; font-weight: 500; min-width: 60px; }
  .port-field { display: flex; flex-direction: column; gap: 3px; }
  .port-label { font-size: 0.65rem; font-family: var(--font-mono); color: var(--text-tertiary); text-transform: uppercase; letter-spacing: 0.08em; }
  .port-proto { font-size: 0.72rem; font-family: var(--font-mono); color: var(--text-tertiary); text-transform: uppercase; }

  .advanced-toggle {
    display: flex; align-items: center; gap: 6px;
    font-size: 0.8rem; color: var(--text-tertiary);
    cursor: pointer; background: none; border: none;
    font-family: var(--font-body); padding: 12px 0;
    transition: color 0.15s; margin-top: 8px;
  }
  .advanced-toggle:hover { color: var(--text-secondary); }
  .adv-chevron { width: 14px; height: 14px; transition: transform 0.2s; }
  .adv-chevron.open { transform: rotate(90deg); }
  .adv-hint { font-size: 0.72rem; color: var(--text-tertiary); opacity: 0.5; margin-left: 4px; }

  .advanced-content { animation: slide-down 0.25s ease-out; padding-top: 10px; }
  @keyframes slide-down { from { opacity: 0; transform: translateY(-6px); } to { opacity: 1; transform: translateY(0); } }

  input:disabled { opacity: 0.4; cursor: not-allowed; }

  @media (max-width: 620px) { :global(.form-grid) { grid-template-columns: 1fr; } }
</style>
