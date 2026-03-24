<script lang="ts">
  import type { EnvVar, DynamicOption } from '$lib/api';

  let { envDefs, values = $bindable(), dynamicOptions = {}, gridClass = 'env-grid' }:
    {
      envDefs: EnvVar[];
      values: Record<string, string>;
      dynamicOptions?: Record<string, DynamicOption[]>;
      gridClass?: string;
    } = $props();

  // Group env vars by their group field, excluding system/autogenerate
  const groups = $derived(() => {
    const visible = envDefs.filter(e => !e.system && !e.autogenerate && !e.hidden);
    const map: Record<string, EnvVar[]> = {};
    for (const e of visible) {
      const group = e.group || 'General';
      if (!map[group]) map[group] = [];
      map[group].push(e);
    }
    return map;
  });

  function getVal(key: string, fallback: string): string {
    return values[key] ?? fallback ?? '';
  }

  function setVal(key: string, val: string) {
    values[key] = val;
  }

  function toggleBool(key: string, fallback: string) {
    const current = values[key] ?? fallback ?? 'false';
    values[key] = current === 'true' ? 'false' : 'true';
  }

  function isBoolOn(key: string, fallback: string): boolean {
    return (values[key] ?? fallback) === 'true';
  }
</script>

{#each Object.entries(groups()) as [groupName, vars]}
  <div class="env-group">
    <div class="env-group-label">{groupName}</div>
    <div class={gridClass}>
      {#each vars as ev}
        <div class="env-row">
          <label class="label" class:label-required={ev.required}>{ev.label || ev.key}</label>
          {#if ev.type === 'boolean'}
            <div class="bool-row">
              <button class="toggle" class:on={isBoolOn(ev.key, ev.default)} onclick={() => toggleBool(ev.key, ev.default)}></button>
              <span class="bool-label">{isBoolOn(ev.key, ev.default) ? 'Enabled' : 'Disabled'}</span>
            </div>
          {:else if dynamicOptions[ev.key]}
            <select class="select" value={getVal(ev.key, ev.default)} onchange={(e) => setVal(ev.key, (e.target as HTMLSelectElement).value)}>
              {#each dynamicOptions[ev.key] as opt}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          {:else if ev.type === 'select' || (ev.options && ev.options.length > 0)}
            <select class="select" value={getVal(ev.key, ev.default)} onchange={(e) => setVal(ev.key, (e.target as HTMLSelectElement).value)}>
              {#if ev.options}
                {#each ev.options as opt}
                  <option value={opt}>{opt}</option>
                {/each}
              {/if}
            </select>
          {:else if ev.type === 'number'}
            <input class="input input-mono" type="number" value={getVal(ev.key, ev.default)} oninput={(e) => setVal(ev.key, (e.target as HTMLInputElement).value)}>
          {:else}
            <input class="input" type="text" value={getVal(ev.key, ev.default)} oninput={(e) => setVal(ev.key, (e.target as HTMLInputElement).value)}>
          {/if}
        </div>
      {/each}
    </div>
  </div>
{/each}

<style>
  .env-group {
    background: var(--bg-elevated);
    border: 1px solid var(--border-dim);
    border-left: 2px solid rgba(232, 114, 42, 0.2);
    border-radius: var(--radius);
    padding: 18px;
    margin-bottom: 14px;
  }
  .env-group:last-child { margin-bottom: 0; }
  .env-group-label {
    font-size: 0.66rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
    margin-bottom: 14px;
  }

  .env-row { margin-bottom: 14px; }
  .env-row:last-child { margin-bottom: 0; }

  .bool-row { display: flex; align-items: center; gap: 8px; }
  .bool-label { font-size: 0.78rem; color: var(--text-tertiary); }

  /* Default grid — consumers can override via gridClass */
  :global(.env-grid) { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  @media (max-width: 700px) {
    :global(.env-grid) { grid-template-columns: 1fr; }
  }
</style>
