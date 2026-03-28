<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type InstalledMod, type ModSearchResult, type ModVersion, type ModTabConfig, type ModUpdate, type ModIssue, type ModCategoryDef } from '$lib/api';
  import { gameserverStore, toast, confirm, onGameserverEvent } from '$lib/stores';

  let { id }: { id: string } = $props();
  const gsId = id;
  const gsState = $derived(gameserverStore.getState(gsId));
  const gameserver = $derived(gsState?.gameserver ?? null);

  // --- State ---
  let config = $state<ModTabConfig | null>(null);
  let installed = $state<InstalledMod[]>([]);
  let updates = $state<ModUpdate[]>([]);
  let loading = $state(true);
  let activeCategory = $state('');

  // Browse loader — local filter, doesn't change the server's actual loader
  let browseLoader = $state('');

  // Search
  let searchQuery = $state('');
  let searchResults = $state<ModSearchResult[]>([]);
  let searchTotal = $state(0);
  let searching = $state(false);
  let searchDebounce: ReturnType<typeof setTimeout>;

  // Version picker
  let versionPickerMod = $state<ModSearchResult | null>(null);
  let versions = $state<ModVersion[]>([]);
  let loadingVersions = $state(false);
  let installingId = $state('');

  // Workshop paste-by-ID
  let workshopId = $state('');
  let addingWorkshop = $state(false);

  // Pack state
  let expandedPacks = $state<Set<string>>(new Set());

  // Compatibility
  let compatIssues = $state<ModIssue[]>([]);
  let changingConfig = $state(false);

  // --- Derived ---
  const categories = $derived(config?.categories ?? []);
  const activeCat = $derived(categories.find(c => c.name === activeCategory));
  const hasWorkshopPasteMode = $derived(
    activeCat?.sources.some(s => s.name === 'workshop' && !s.config?.['has_api_key']) ?? false
  );

  // Group installed mods: packs first, then standalone
  const categoryInstalled = $derived(installed.filter(m => m.category === activeCategory || (m.pack_id && m.category === 'Mods' && activeCategory === 'Mods')));
  const packs = $derived(categoryInstalled.filter(m => m.delivery === 'pack'));
  const packModIds = $derived(new Set(packs.map(p => p.id)));
  const standaloneMods = $derived(categoryInstalled.filter(m => m.delivery !== 'pack' && !m.pack_id));
  const installedSourceIds = $derived(new Set(installed.map(m => m.source_id)));

  const updateMap = $derived(new Map(updates.map(u => [u.mod_id, u])));
  const hasUpdates = $derived(updates.length > 0);

  // SSE
  let unsubSSE: (() => void) | undefined;

  onMount(async () => {
    try {
      const [cfg, mods] = await Promise.all([
        api.mods.config(gsId),
        api.mods.list(gsId),
      ]);
      config = cfg;
      installed = mods;
      if (cfg.loader) {
        browseLoader = cfg.loader.current;
      }
      if (cfg.categories.length > 0) {
        activeCategory = cfg.categories[0].name;
        // Load default/popular results
        loadDefaultResults(cfg.categories[0].name);
      }

      // Check for updates (non-blocking)
      api.mods.updates(gsId).then(u => { updates = u ?? []; }).catch(() => {});
    } catch (e: any) {
      toast(`Failed to load mods: ${e.message}`, 'error');
    } finally {
      loading = false;
    }

    unsubSSE = onGameserverEvent(gsId, (event) => {
      if (event.type === 'mod.installed' || event.type === 'mod.uninstalled') {
        api.mods.list(gsId).then(mods => { installed = mods; }).catch(() => {});
        api.mods.updates(gsId).then(u => { updates = u ?? []; }).catch(() => {});
      }
    });
  });

  onDestroy(() => { unsubSSE?.(); });

  // --- Actions ---

  async function changeVersion(newVersion: string) {
    if (!config?.version || !gameserver) return;
    const env = { ...gameserver.env, [config.version.env]: newVersion };
    await checkAndApplyEnvChange(env);
  }

  function setBrowseLoader(newValue: string) {
    browseLoader = newValue;
    // Refresh search with new loader filter
    if (searchQuery.trim()) {
      doSearch();
    } else {
      loadDefaultResults(activeCategory);
    }
  }

  async function loadDefaultResults(category: string) {
    if (!category) return;
    searching = true;
    try {
      const resp = await api.mods.search(gsId, category, '', 0, 20);
      searchResults = resp.results;
      searchTotal = resp.total;
    } catch {
      // Non-critical — empty state is fine
      searchResults = [];
    } finally {
      searching = false;
    }
  }

  async function checkAndApplyEnvChange(newEnv: Record<string, string>) {
    if (!gameserver) return;
    changingConfig = true;
    compatIssues = [];

    try {
      if (installed.length > 0) {
        const issues = await api.mods.checkCompatibility(gsId, newEnv);
        if (issues.length > 0) {
          compatIssues = issues;
          const deactivated = issues.every(i => i.type === 'deactivated');
          const msg = deactivated
            ? `${issues.length} mod(s) will be deactivated but remain on disk.`
            : `${issues.length} mod(s) are incompatible with this change.`;

          const ok = await confirm({
            title: 'Mod Compatibility',
            message: msg + '\n\nContinue anyway?',
            confirmLabel: 'Continue',
            danger: !deactivated,
          });
          if (!ok) { changingConfig = false; return; }
        }
      }

      await api.gameservers.update(gsId, { env: newEnv });
      toast('Configuration updated. Server will reinstall.', 'info');

      // Refresh config
      config = await api.mods.config(gsId);
      compatIssues = [];
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    } finally {
      changingConfig = false;
    }
  }

  function handleSearchInput() {
    clearTimeout(searchDebounce);
    if (!searchQuery.trim()) {
      searchDebounce = setTimeout(() => loadDefaultResults(activeCategory), 300);
      return;
    }
    searchDebounce = setTimeout(() => doSearch(), 300);
  }

  async function doSearch() {
    if (!activeCategory) return;
    searching = true;
    try {
      const resp = await api.mods.search(gsId, activeCategory, searchQuery.trim(), 0, 20);
      searchResults = resp.results;
      searchTotal = resp.total;
    } catch (e: any) {
      toast(`Search failed: ${e.message}`, 'error');
    } finally {
      searching = false;
    }
  }

  function handleInstallClick(mod: ModSearchResult) {
    if (mod.source === 'umod' || mod.source === 'workshop') {
      installMod(mod);
    } else {
      openVersionPicker(mod);
    }
  }

  async function openVersionPicker(mod: ModSearchResult) {
    versionPickerMod = mod;
    loadingVersions = true;
    versions = [];
    try {
      versions = await api.mods.versions(gsId, activeCategory, mod.source, mod.source_id);
    } catch (e: any) {
      toast(`Failed to load versions: ${e.message}`, 'error');
      versionPickerMod = null;
    } finally {
      loadingVersions = false;
    }
  }

  async function installMod(mod: ModSearchResult, versionId?: string) {
    installingId = mod.source_id;
    try {
      const result = await api.mods.install(gsId, {
        category: activeCategory,
        source: mod.source,
        source_id: mod.source_id,
        version_id: versionId ?? '',
      });
      installed = [result, ...installed];
      versionPickerMod = null;
      toast(`Installed ${mod.name}`, 'success');
    } catch (e: any) {
      toast(`Failed to install: ${e.message}`, 'error');
    } finally {
      installingId = '';
    }
  }

  async function uninstallMod(mod: InstalledMod) {
    const label = mod.delivery === 'pack' ? 'modpack and all its mods' : `"${mod.name}"`;
    if (!await confirm({ title: 'Uninstall', message: `Remove ${label}?`, confirmLabel: 'Uninstall', danger: true })) return;
    try {
      await api.mods.uninstall(gsId, mod.id);
      installed = installed.filter(m => m.id !== mod.id && m.pack_id !== mod.id);
      toast(`Uninstalled ${mod.name}`, 'success');
    } catch (e: any) {
      toast(`Failed to uninstall: ${e.message}`, 'error');
    }
  }

  async function updateMod(mod: InstalledMod) {
    try {
      await api.mods.update(gsId, mod.id);
      installed = await api.mods.list(gsId);
      updates = await api.mods.updates(gsId) ?? [];
      toast(`Updated ${mod.name}`, 'success');
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    }
  }

  async function updateAll() {
    try {
      await api.mods.updateAll(gsId);
      installed = await api.mods.list(gsId);
      updates = [];
      toast('All mods updated', 'success');
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    }
  }

  async function addWorkshopItem() {
    if (!workshopId.trim()) return;
    let itemId = workshopId.trim();
    const match = itemId.match(/id=(\d+)/);
    if (match) itemId = match[1];
    if (!/^\d+$/.test(itemId)) {
      toast('Enter a valid Workshop item ID or URL', 'error');
      return;
    }

    addingWorkshop = true;
    try {
      const result = await api.mods.install(gsId, {
        category: activeCategory,
        source: 'workshop',
        source_id: itemId,
      });
      installed = [result, ...installed];
      workshopId = '';
      toast('Added workshop item', 'success');
    } catch (e: any) {
      toast(`Failed to add: ${e.message}`, 'error');
    } finally {
      addingWorkshop = false;
    }
  }

  function togglePack(packId: string) {
    const next = new Set(expandedPacks);
    next.has(packId) ? next.delete(packId) : next.add(packId);
    expandedPacks = next;
  }

  function packMods(packId: string): InstalledMod[] {
    return installed.filter(m => m.pack_id === packId);
  }

  function fmt(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
    return String(n);
  }
</script>

{#if loading}
  <p class="empty-state">Loading mods...</p>
{:else if !config}
  <p class="empty-state">Mods are not available for this game.</p>
{:else}
  <!-- Config Bar: Version + Loader -->
  {#if config.version || config.loader}
    <div class="config-bar">
      {#if config.version}
        <div class="config-field">
          <span class="config-label">Version</span>
          <select
            class="select config-select"
            value={config.version.current}
            disabled={changingConfig}
            onchange={(e) => changeVersion((e.target as HTMLSelectElement).value)}
          >
            {#each config.version.options as opt}
              <option value={opt.value}>{opt.label || opt.value}</option>
            {/each}
          </select>
        </div>
      {/if}

      {#if config.loader}
        <div class="config-field">
          <span class="config-label">Loader</span>
          {#if config.loader.options.length === 2 && config.loader.options.includes('true') && config.loader.options.includes('false')}
            <!-- Boolean toggle -->
            <button
              class="toggle" class:on={browseLoader === 'true'}
              disabled={changingConfig}
              onclick={() => setBrowseLoader(browseLoader === 'true' ? 'false' : 'true')}
            ></button>
          {:else}
            <!-- Multi-option selector -->
            <div class="loader-pills">
              {#each config.loader.options as opt}
                <button
                  class="loader-pill"
                  class:active={browseLoader === opt}
                  disabled={changingConfig}
                  onclick={() => setBrowseLoader(opt)}
                >{opt}</button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Compatibility Warning -->
  {#if compatIssues.length > 0}
    <div class="compat-banner">
      <svg viewBox="0 0 16 16" fill="currentColor"><path d="M8.982 1.566a1.13 1.13 0 0 0-1.96 0L.165 13.233c-.457.778.091 1.767.98 1.767h13.713c.889 0 1.438-.99.98-1.767L8.982 1.566zM8 5c.535 0 .954.462.9.995l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 5.995A.905.905 0 0 1 8 5zm.002 6a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>
      <span>{compatIssues.length} mod{compatIssues.length !== 1 ? 's' : ''} affected by this change</span>
    </div>
  {/if}

  <!-- Category Tabs -->
  {#if categories.length > 1}
    <div class="cat-tabs">
      {#each categories as cat}
        <button
          class="cat-tab"
          class:active={activeCategory === cat.name}
          onclick={() => { activeCategory = cat.name; searchQuery = ''; loadDefaultResults(cat.name); }}
        >{cat.name}</button>
      {/each}
    </div>
  {/if}

  <!-- Installed -->
  <section class="section">
    <div class="section-header">
      <h3>Installed <span class="count">({categoryInstalled.length})</span></h3>
      {#if hasUpdates}
        <button class="btn-accent btn-sm" onclick={updateAll}>
          <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36a.25.25 0 0 1 .192-.41zm-11 2h3.932a.25.25 0 0 0 .192-.41L2.692 6.23a.25.25 0 0 0-.384 0L.342 8.59A.25.25 0 0 0 .534 9z"/><path d="M8 3c-1.552 0-2.94.707-3.857 1.818a.5.5 0 1 1-.771-.636A6.002 6.002 0 0 1 13.917 7H12.9A5.002 5.002 0 0 0 8 3zM3.1 9a5.002 5.002 0 0 0 8.757 2.182.5.5 0 1 1 .771.636A6.002 6.002 0 0 1 2.083 9H3.1z"/></svg>
          Update All ({updates.length})
        </button>
      {/if}
    </div>

    {#if categoryInstalled.length === 0}
      <p class="empty-state">No mods installed. Search below to get started.</p>
    {:else}
      <div class="mod-list">
        <!-- Packs -->
        {#each packs as pack}
          {@const pm = packMods(pack.id)}
          {@const expanded = expandedPacks.has(pack.id)}
          <div class="mod-row pack-header" onclick={() => togglePack(pack.id)}>
            <div class="mod-row-left">
              <span class="expand-chevron" class:open={expanded}>&#9656;</span>
              <span class="badge pack-badge">pack</span>
              <span class="mod-name">{pack.name}</span>
              {#if pack.version}<span class="mod-ver">{pack.version}</span>{/if}
              <span class="pack-count">{pm.length} mods</span>
            </div>
            <div class="mod-row-right">
              {#if updateMap.has(pack.id)}
                <button class="btn-update" onclick={(e) => { e.stopPropagation(); updateMod(pack); }}>&#8593; {updateMap.get(pack.id)?.latest_version.version}</button>
              {/if}
              <button class="btn-remove" onclick={(e) => { e.stopPropagation(); uninstallMod(pack); }}>&times;</button>
            </div>
          </div>
          {#if expanded}
            {#each pm as mod}
              <div class="mod-row sub-mod">
                <div class="mod-row-left">
                  <span class="mod-name">{mod.name}</span>
                  {#if mod.version}<span class="mod-ver">{mod.version}</span>{/if}
                  {#if mod.auto_installed}<span class="badge dep-badge">dependency</span>{/if}
                </div>
                <div class="mod-row-right">
                  <button class="btn-remove" onclick={() => uninstallMod(mod)}>&times;</button>
                </div>
              </div>
            {/each}
          {/if}
        {/each}

        <!-- Standalone mods -->
        {#each standaloneMods as mod}
          <div class="mod-row">
            <div class="mod-row-left">
              <span class="mod-name">{mod.name}</span>
              {#if mod.version}<span class="mod-ver">{mod.version}</span>{/if}
              <span class="badge source-badge">{mod.source}</span>
              {#if mod.auto_installed}<span class="badge dep-badge">dependency</span>{/if}
              {#if mod.delivery === 'manifest'}<span class="badge manifest-badge">restart required</span>{/if}
            </div>
            <div class="mod-row-right">
              {#if updateMap.has(mod.id)}
                <button class="btn-update" onclick={() => updateMod(mod)}>&#8593; {updateMap.get(mod.id)?.latest_version.version}</button>
              {/if}
              <button class="btn-remove" onclick={() => uninstallMod(mod)}>&times;</button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <!-- Search / Browse -->
  <section class="section">
    <h3 class="section-title">Browse</h3>

    {#if hasWorkshopPasteMode}
      <div class="workshop-paste">
        <input
          class="input"
          type="text"
          placeholder="Paste Steam Workshop URL or item ID..."
          bind:value={workshopId}
          onkeydown={(e) => { if (e.key === 'Enter') addWorkshopItem(); }}
        />
        <button class="btn-solid" onclick={addWorkshopItem} disabled={addingWorkshop || !workshopId.trim()}>
          {addingWorkshop ? 'Adding...' : 'Add'}
        </button>
      </div>
    {:else}
      <div class="search-bar">
        <input
          class="input"
          type="text"
          placeholder="Search {activeCategory.toLowerCase()}..."
          bind:value={searchQuery}
          oninput={handleSearchInput}
          onkeydown={(e) => { if (e.key === 'Enter') doSearch(); }}
        />
        {#if searching}
          <span class="search-spinner">searching...</span>
        {/if}
      </div>
    {/if}

    {#if searchResults.length > 0}
      <div class="results">
        {#each searchResults as mod}
          <div class="result-card">
            <div class="result-head">
              {#if mod.icon_url}
                <img src={mod.icon_url} alt="" class="result-icon" />
              {:else}
                <div class="result-icon result-icon-empty"></div>
              {/if}
              <div class="result-title">
                <span class="mod-name">{mod.name}</span>
                <span class="result-author">{mod.author}</span>
              </div>
              <span class="badge source-badge">{mod.source}</span>
            </div>
            <p class="result-desc">{mod.description}</p>
            <div class="result-foot">
              <span class="result-dl">{fmt(mod.downloads)} downloads</span>
              {#if installedSourceIds.has(mod.source_id)}
                <span class="badge installed-badge">Installed</span>
              {:else}
                <button
                  class="btn-solid btn-sm"
                  onclick={() => handleInstallClick(mod)}
                  disabled={installingId === mod.source_id}
                >{installingId === mod.source_id ? '...' : 'Install'}</button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
      {#if searchTotal > searchResults.length}
        <p class="results-more">Showing {searchResults.length} of {searchTotal}</p>
      {/if}
    {:else if searchQuery.trim() && !searching}
      <p class="empty-state">No results for "{searchQuery}"</p>
    {/if}
  </section>

  <!-- Version Picker Modal -->
  {#if versionPickerMod}
    <div class="overlay" onclick={() => { versionPickerMod = null; }} role="presentation">
      <div class="modal" onclick={(e) => e.stopPropagation()} role="dialog">
        <div class="modal-head">
          <h3>{versionPickerMod.name}</h3>
          <button class="modal-x" onclick={() => { versionPickerMod = null; }}>&times;</button>
        </div>
        <div class="modal-body">
          {#if loadingVersions}
            <p class="empty-state">Loading versions...</p>
          {:else if versions.length === 0}
            <p class="empty-state">No compatible versions found.</p>
          {:else}
            {#each versions as v}
              {@const compatible = !config?.version?.current || (v.game_versions?.includes(config.version.current) ?? true)}
              <div class="ver-row" class:dim={!compatible}>
                <div class="ver-info">
                  <span class="ver-num">{v.version}</span>
                  {#if v.game_version}
                    <span class="badge" class:ver-match={compatible} class:ver-mismatch={!compatible}>{v.game_version}</span>
                  {/if}
                  {#if v.loader}<span class="badge">{v.loader}</span>{/if}
                </div>
                <button
                  class="btn-solid btn-sm"
                  onclick={() => installMod(versionPickerMod!, v.version_id)}
                  disabled={installingId === versionPickerMod!.source_id}
                >{compatible ? 'Install' : 'Install Anyway'}</button>
              </div>
            {/each}
          {/if}
        </div>
      </div>
    </div>
  {/if}
{/if}

<style>
  .empty-state {
    color: var(--text-tertiary); font-size: 0.84rem;
    padding: 32px 0; text-align: center;
  }

  /* Config bar */
  .config-bar {
    display: flex; gap: 20px; align-items: flex-end;
    padding: 16px 20px;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    margin-bottom: 20px;
  }
  .config-field { display: flex; flex-direction: column; gap: 6px; }
  .config-label { font-size: 0.72rem; font-weight: 500; color: var(--text-tertiary); text-transform: uppercase; letter-spacing: 0.06em; }
  .config-select { width: auto; min-width: 140px; padding: 7px 32px 7px 12px; font-size: 0.82rem; }

  .loader-pills { display: flex; gap: 3px; }
  .loader-pill {
    padding: 7px 14px; border-radius: var(--radius-sm);
    font-size: 0.8rem; font-weight: 450;
    background: var(--bg-inset); color: var(--text-tertiary);
    border: 1px solid transparent; cursor: pointer;
    transition: all 0.15s; text-transform: capitalize;
  }
  .loader-pill:hover:not(:disabled) { color: var(--text-secondary); background: var(--bg-hover); }
  .loader-pill.active { background: var(--accent-dim); color: var(--accent); border-color: var(--accent-border); }
  .loader-pill:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Compat banner */
  .compat-banner {
    display: flex; align-items: center; gap: 10px;
    padding: 10px 16px; margin-bottom: 16px;
    border-radius: var(--radius-sm);
    background: rgba(245, 158, 11, 0.06);
    border: 1px solid rgba(245, 158, 11, 0.15);
    color: var(--caution); font-size: 0.82rem;
  }
  .compat-banner svg { width: 14px; height: 14px; flex-shrink: 0; }

  /* Category tabs */
  .cat-tabs { display: flex; gap: 2px; margin-bottom: 20px; }
  .cat-tab {
    padding: 8px 16px; border-radius: var(--radius-sm);
    font-size: 0.82rem; font-weight: 450;
    background: transparent; color: var(--text-tertiary);
    border: 1px solid transparent; cursor: pointer;
    transition: all 0.15s;
  }
  .cat-tab:hover { color: var(--text-secondary); background: var(--bg-hover); }
  .cat-tab.active { color: var(--accent); background: var(--accent-dim); border-color: var(--accent-border); }

  /* Sections */
  .section { margin-bottom: 28px; }
  .section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
  .section-header h3 { font-size: 0.88rem; font-weight: 600; }
  .section-title { font-size: 0.88rem; font-weight: 600; margin-bottom: 12px; }
  .count { color: var(--text-tertiary); font-weight: 400; }

  /* Mod list */
  .mod-list {
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .mod-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 9px 14px;
    border-bottom: 1px solid var(--border-dim);
    font-size: 0.84rem;
    transition: background 0.1s;
  }
  .mod-row:last-child { border-bottom: none; }
  .mod-row:hover { background: var(--bg-hover); }
  .mod-row-left { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
  .mod-row-right { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }

  .pack-header { cursor: pointer; }
  .sub-mod { padding-left: 36px; background: var(--bg-inset); }
  .sub-mod:hover { background: var(--bg-hover); }

  .expand-chevron {
    font-size: 0.7rem; color: var(--text-tertiary);
    transition: transform 0.15s; display: inline-block;
  }
  .expand-chevron.open { transform: rotate(90deg); }

  .mod-name { font-weight: 500; color: var(--text-primary); }
  .mod-ver { font-family: var(--font-mono); font-size: 0.72rem; color: var(--text-tertiary); }
  .pack-count { font-size: 0.72rem; color: var(--text-tertiary); }

  /* Badges */
  .badge {
    font-size: 0.64rem; padding: 2px 6px; border-radius: 3px;
    text-transform: uppercase; letter-spacing: 0.04em; font-weight: 500;
  }
  .source-badge { background: var(--bg-inset); color: var(--text-tertiary); border: 1px solid var(--border-dim); }
  .dep-badge { background: rgba(99, 102, 241, 0.08); color: #818cf8; }
  .pack-badge { background: var(--accent-dim); color: var(--accent); }
  .manifest-badge { background: rgba(245, 158, 11, 0.08); color: var(--caution); }
  .installed-badge { background: var(--live-dim); color: var(--live); }

  /* Buttons */
  .btn-sm { padding: 5px 12px; font-size: 0.76rem; }
  .btn-update {
    padding: 3px 8px; border-radius: 4px; font-size: 0.72rem; font-weight: 500;
    background: var(--accent-dim); color: var(--accent); border: 1px solid var(--accent-border);
    cursor: pointer; transition: all 0.15s;
  }
  .btn-update:hover { background: rgba(232, 114, 42, 0.2); }
  .btn-remove {
    width: 24px; height: 24px; border-radius: 4px;
    background: transparent; border: none;
    color: var(--text-tertiary); font-size: 1.1rem;
    cursor: pointer; display: grid; place-items: center;
    transition: all 0.15s;
  }
  .btn-remove:hover { color: var(--danger); background: rgba(239, 68, 68, 0.08); }

  /* Search */
  .search-bar { position: relative; margin-bottom: 16px; }
  .search-bar .input { padding-right: 80px; }
  .search-spinner {
    position: absolute; right: 14px; top: 50%; transform: translateY(-50%);
    font-size: 0.72rem; color: var(--text-tertiary);
  }

  .workshop-paste { display: flex; gap: 8px; margin-bottom: 16px; }
  .workshop-paste .input { flex: 1; }

  /* Results */
  .results {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 10px;
  }
  .result-card {
    padding: 14px;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    transition: border-color 0.15s;
  }
  .result-card:hover { border-color: var(--border-warm); }
  .result-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
  .result-icon {
    width: 36px; height: 36px; border-radius: 6px;
    object-fit: cover; flex-shrink: 0;
  }
  .result-icon-empty { background: var(--bg-inset); border: 1px solid var(--border-dim); }
  .result-title { flex: 1; min-width: 0; }
  .result-title .mod-name { display: block; font-size: 0.86rem; }
  .result-author { font-size: 0.72rem; color: var(--text-tertiary); }
  .result-desc {
    font-size: 0.76rem; color: var(--text-secondary); line-height: 1.4;
    margin-bottom: 10px;
    display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .result-foot { display: flex; justify-content: space-between; align-items: center; }
  .result-dl { font-size: 0.7rem; color: var(--text-tertiary); }
  .results-more { font-size: 0.76rem; color: var(--text-tertiary); text-align: center; margin-top: 12px; }

  /* Modal */
  .overlay {
    position: fixed; inset: 0; background: rgba(0, 0, 0, 0.65);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    width: 90%; max-width: 480px; max-height: 70vh;
    display: flex; flex-direction: column;
  }
  .modal-head {
    display: flex; justify-content: space-between; align-items: center;
    padding: 14px 18px; border-bottom: 1px solid var(--border-dim);
  }
  .modal-head h3 { font-size: 0.92rem; font-weight: 600; }
  .modal-x {
    background: none; border: none; color: var(--text-tertiary);
    font-size: 1.3rem; cursor: pointer; padding: 0 4px;
  }
  .modal-x:hover { color: var(--text-primary); }
  .modal-body { padding: 14px 18px; overflow-y: auto; display: flex; flex-direction: column; gap: 5px; }

  .ver-row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 8px 10px; border-radius: var(--radius-sm);
    border: 1px solid var(--border-dim);
  }
  .ver-row.dim { opacity: 0.6; }
  .ver-info { display: flex; align-items: center; gap: 6px; }
  .ver-num { font-weight: 500; font-size: 0.82rem; font-family: var(--font-mono); }
  .ver-match { background: var(--live-dim); color: var(--live); border: none; }
  .ver-mismatch { background: rgba(245, 158, 11, 0.08); color: var(--caution); border: none; }

  @media (max-width: 640px) {
    .config-bar { flex-direction: column; align-items: stretch; gap: 12px; }
    .results { grid-template-columns: 1fr; }
    .mod-row { flex-wrap: wrap; gap: 6px; }
  }
</style>
