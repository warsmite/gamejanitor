<script lang="ts">
  
  import { onMount, onDestroy } from 'svelte';
  import { api, type InstalledMod, type ModSearchResult, type ModVersion, type ModSourceInfo } from '$lib/api';
  import { gameserverStore, toast, confirm, onGameserverEvent } from '$lib/stores';

  let { id }: { id: string } = $props();
  const gsId = id;
  const gsState = $derived(gameserverStore.getState(gsId));
  const gameserver = $derived(gsState?.gameserver ?? null);
  const game = $derived(gameserverStore.gameFor(gameserver?.game_id ?? ''));

  let installed = $state<InstalledMod[]>([]);
  let sources = $state<ModSourceInfo[]>([]);
  let activeSource = $state('');
  let loading = $state(true);
  let searchQuery = $state('');
  let searchResults = $state<ModSearchResult[]>([]);
  let searchTotal = $state(0);
  let searching = $state(false);
  let searchDebounce: ReturnType<typeof setTimeout>;

  // Version picker state
  let versionPickerMod = $state<ModSearchResult | null>(null);
  let versions = $state<ModVersion[]>([]);
  let loadingVersions = $state(false);
  let installingId = $state('');

  // Paste-by-ID state
  let workshopId = $state('');
  let addingWorkshop = $state(false);

  // Loader state
  let changingLoader = $state(false);

  const modSources = $derived(game?.mods?.sources ?? []);
  const loaderConfig = $derived(game?.mods?.loader ?? null);
  const activeSourceConfig = $derived(modSources.find(s => s.type === activeSource));

  // Current loader value from the gameserver env
  const currentLoader = $derived.by(() => {
    if (!loaderConfig || !gameserver) return null;
    const env = gameserver.env || {};
    return env[loaderConfig.env_key] ?? loaderConfig.default;
  });

  // Whether the loader is in a "modded" state (not vanilla/disabled)
  const loaderReady = $derived.by(() => {
    if (!loaderConfig) return true; // No loader needed (e.g., workshop games)
    if (!activeSourceConfig) return true;

    // Check requires_env
    if (activeSourceConfig.requires_env) {
      const env = gameserver?.env || {};
      for (const [key, required] of Object.entries(activeSourceConfig.requires_env)) {
        if (env[key] !== required) return false;
      }
    }

    // Check loaders map
    if (activeSourceConfig.loaders && activeSourceConfig.loader_env) {
      const env = gameserver?.env || {};
      const val = env[activeSourceConfig.loader_env] || '';
      if (!activeSourceConfig.loaders[val]) return false;
    }

    return true;
  });

  const installedIds = $derived(new Set(installed.map(m => m.source_id)));
  const activeSearchMode = $derived(sources.find(s => s.type === activeSource)?.search_mode ?? 'search');
  const serverGameVersion = $derived(sources.find(s => s.type === activeSource)?.game_version ?? '');

  let unsubSSE: (() => void) | undefined;

  onMount(async () => {
    try {
      const [mods, srcs] = await Promise.all([
        api.mods.list(gsId),
        api.mods.sources(gsId),
      ]);
      installed = mods;
      sources = srcs;
      if (srcs.length > 0) activeSource = srcs[0].type;
    } catch (e: any) {
      toast(`Failed to load mods: ${e.message}`, 'error');
    } finally {
      loading = false;
    }

    unsubSSE = onGameserverEvent(gsId, (event) => {
      if (event.type === 'mod.installed' || event.type === 'mod.uninstalled') {
        api.mods.list(gsId).then(mods => { installed = mods; }).catch((e) => { console.warn('Mods: failed to refresh mod list', e); });
      }
    });
  });

  onDestroy(() => {
    unsubSSE?.();
  });

  async function changeLoader(newValue: string) {
    if (!loaderConfig || !gameserver) return;
    const oldValue = currentLoader;
    if (newValue === oldValue) return;

    // Warn about existing mods being incompatible when switching loaders
    if (installed.length > 0) {
      const ok = await confirm({
        title: 'Change Mod Loader',
        message: `Changing the mod loader will reinstall the server. Your ${installed.length} installed mod(s) may not be compatible with the new loader and could stop working.\n\nContinue?`,
        confirmLabel: 'Change & Reinstall',
        danger: true,
      });
      if (!ok) return;
    } else {
      // Still warn about reinstall
      let label = newValue;
      if (loaderConfig.type === 'boolean') {
        label = newValue === 'true' ? `enable ${loaderConfig.label}` : `disable ${loaderConfig.label}`;
      }
      const ok = await confirm({
        title: 'Change Mod Loader',
        message: `This will reinstall the server to set up ${label}. Server data (worlds, configs) will be preserved, but the server binary will be re-downloaded.\n\nContinue?`,
        confirmLabel: 'Reinstall',
        danger: false,
      });
      if (!ok) return;
    }

    changingLoader = true;
    try {
      await api.gameservers.update(gsId, {
        env: { ...gameserver.env, [loaderConfig.env_key]: newValue }
      });
      toast('Server is reinstalling with new mod loader...', 'info');
    } catch (e: any) {
      toast(`Failed to change loader: ${e.message}`, 'error');
    } finally {
      changingLoader = false;
    }
  }

  function handleSearchInput() {
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(() => doSearch(), 300);
  }

  async function doSearch() {
    if (!searchQuery.trim() || !activeSource || !loaderReady) return;
    searching = true;
    try {
      const resp = await api.mods.search(gsId, activeSource, searchQuery.trim(), 0, 20);
      searchResults = resp.results;
      searchTotal = resp.total;
    } catch (e: any) {
      toast(`Search failed: ${e.message}`, 'error');
    } finally {
      searching = false;
    }
  }

  async function installMod(mod: ModSearchResult, versionId?: string, modVersion?: ModVersion) {
    // Check if this mod version requires a different server game version
    if (modVersion && serverGameVersion && activeSourceConfig?.version_env && gameserver) {
      const compatible = modVersion.game_versions?.includes(serverGameVersion) ?? true;
      if (!compatible && modVersion.game_version) {
        const ok = await confirm({
          title: 'Server Version Mismatch',
          message: `"${mod.name}" is for Minecraft ${modVersion.game_version}, but your server is on ${serverGameVersion}. Update the server to ${modVersion.game_version}? The server will re-run its install to download the new version. World data is preserved.`,
          confirmLabel: `Update to ${modVersion.game_version}`,
          danger: false,
        });
        if (!ok) return;

        // Update the server version env var
        try {
          await api.gameservers.update(gsId, {
            env: { ...gameserver.env, [activeSourceConfig.version_env]: modVersion.game_version }
          });
          toast(`Server version updating to ${modVersion.game_version}...`, 'info');
          // Refresh sources to pick up new resolved version
          sources = await api.mods.sources(gsId);
        } catch (e: any) {
          toast(`Failed to update server version: ${e.message}`, 'error');
          return;
        }
      }
    }

    installingId = mod.source_id;
    try {
      const result = await api.mods.install(gsId, {
        source: activeSource,
        source_id: mod.source_id,
        version_id: versionId ?? '',
        name: mod.name,
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

  async function openVersionPicker(mod: ModSearchResult) {
    versionPickerMod = mod;
    loadingVersions = true;
    versions = [];
    try {
      versions = await api.mods.versions(gsId, activeSource, mod.source_id);
    } catch (e: any) {
      toast(`Failed to load versions: ${e.message}`, 'error');
      versionPickerMod = null;
    } finally {
      loadingVersions = false;
    }
  }

  function handleInstallClick(mod: ModSearchResult) {
    if (activeSource === 'umod' || activeSource === 'workshop') {
      installMod(mod);
    } else {
      openVersionPicker(mod);
    }
  }

  async function uninstallMod(mod: InstalledMod) {
    if (!await confirm({ title: 'Uninstall Mod', message: `Remove "${mod.name}"?`, confirmLabel: 'Uninstall', danger: true })) return;
    try {
      await api.mods.uninstall(gsId, mod.id);
      installed = installed.filter(m => m.id !== mod.id);
      toast(`Uninstalled ${mod.name}`, 'success');
    } catch (e: any) {
      toast(`Failed to uninstall: ${e.message}`, 'error');
    }
  }

  async function addWorkshopItem() {
    if (!workshopId.trim()) return;
    let id = workshopId.trim();
    const match = id.match(/id=(\d+)/);
    if (match) id = match[1];
    if (!/^\d+$/.test(id)) {
      toast('Enter a valid Workshop item ID or URL', 'error');
      return;
    }

    addingWorkshop = true;
    try {
      const result = await api.mods.install(gsId, {
        source: 'workshop',
        source_id: id,
        name: `Workshop Item ${id}`,
      });
      installed = [result, ...installed];
      workshopId = '';
      toast(`Added workshop item`, 'success');
    } catch (e: any) {
      toast(`Failed to add: ${e.message}`, 'error');
    } finally {
      addingWorkshop = false;
    }
  }

  function formatDownloads(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
    return String(n);
  }

  function formatDate(d: string): string {
    return new Date(d).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
  }

  function loaderDisplayName(value: string): string {
    return value.charAt(0).toUpperCase() + value.slice(1);
  }
</script>

{#if loading}
  <p class="empty">Loading mods...</p>
{:else}
  <!-- Loader Selector -->
  {#if loaderConfig}
    <section class="loader-section">
      {#if loaderConfig.type === 'boolean'}
        <!-- Boolean toggle (e.g., Oxide) -->
        <div class="loader-toggle">
          <span class="loader-label">{loaderConfig.label}</span>
          <button
            class="toggle"
            class:on={currentLoader === 'true'}
            disabled={changingLoader}
            onclick={() => changeLoader(currentLoader === 'true' ? 'false' : 'true')}
          ></button>
          {#if currentLoader !== 'true'}
            <span class="loader-hint">Enable to install plugins</span>
          {/if}
        </div>
      {:else if loaderConfig.type === 'select' && loaderConfig.options}
        <!-- Select picker (e.g., Minecraft mod loader) -->
        <div class="loader-picker">
          <span class="loader-label">{loaderConfig.label}</span>
          <div class="loader-options">
            {#each loaderConfig.options as opt}
              <button
                class="loader-option"
                class:active={currentLoader === opt}
                disabled={changingLoader}
                onclick={() => changeLoader(opt)}
              >
                {loaderDisplayName(opt)}
              </button>
            {/each}
          </div>
        </div>
        {#if currentLoader === loaderConfig.default && activeSourceConfig?.loaders && !activeSourceConfig.loaders[currentLoader ?? '']}
          <p class="loader-hint-block">Select a mod loader above to browse and install mods.</p>
        {/if}
      {/if}
    </section>
  {/if}

  <!-- Installed Mods -->
  {#if installed.length > 0 || loaderReady}
    <section class="section">
      <h3 class="section-title">Installed ({installed.length})</h3>
      {#if installed.length === 0}
        <p class="empty">No mods installed.{loaderReady ? ' Search below to get started.' : ''}</p>
      {:else}
        <div class="installed-list">
          {#each installed as mod}
            <div class="installed-row">
              <div class="mod-info">
                <span class="mod-name">{mod.name}</span>
                {#if mod.version}
                  <span class="mod-version">{mod.version}</span>
                {/if}
                <span class="mod-source">{mod.source}</span>
                {#if mod.source === 'workshop' && !mod.file_path}
                  <span class="badge-restart">restart required</span>
                {/if}
              </div>
              <div class="mod-meta">
                <span class="mod-date">{formatDate(mod.installed_at)}</span>
                <button class="btn-sm danger" onclick={() => uninstallMod(mod)}>Uninstall</button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/if}

  <!-- Search / Browse (only when loader is ready) -->
  {#if loaderReady}
    <section class="section">
      <h3 class="section-title">Browse Mods</h3>

      {#if sources.length > 1}
        <div class="source-tabs">
          {#each sources as src}
            <button
              class="source-tab"
              class:active={activeSource === src.type}
              onclick={() => { activeSource = src.type; searchResults = []; searchQuery = ''; }}
            >{src.type}</button>
          {/each}
        </div>
      {/if}

      {#if activeSearchMode === 'paste_id'}
        <div class="paste-id">
          <input
            type="text"
            placeholder="Paste Steam Workshop URL or item ID..."
            bind:value={workshopId}
            onkeydown={(e) => { if (e.key === 'Enter') addWorkshopItem(); }}
          />
          <button class="btn-accent" onclick={addWorkshopItem} disabled={addingWorkshop || !workshopId.trim()}>
            {addingWorkshop ? 'Adding...' : 'Add'}
          </button>
        </div>
      {:else}
        <div class="search-bar">
          <input
            type="text"
            placeholder="Search mods..."
            bind:value={searchQuery}
            oninput={handleSearchInput}
            onkeydown={(e) => { if (e.key === 'Enter') doSearch(); }}
          />
          {#if searching}
            <span class="searching">Searching...</span>
          {/if}
        </div>

        {#if searchResults.length > 0}
          <div class="results-grid">
            {#each searchResults as mod}
              <div class="mod-card">
                <div class="mod-card-header">
                  {#if mod.icon_url}
                    <img src={mod.icon_url} alt="" class="mod-icon" />
                  {:else}
                    <div class="mod-icon placeholder">
                      <svg viewBox="0 0 16 16" fill="currentColor"><path d="M6.5 1A1.5 1.5 0 0 0 5 2.5V3H1.5A1.5 1.5 0 0 0 0 4.5v8A1.5 1.5 0 0 0 1.5 14h13a1.5 1.5 0 0 0 1.5-1.5v-8A1.5 1.5 0 0 0 14.5 3H11v-.5A1.5 1.5 0 0 0 9.5 1h-3z"/></svg>
                    </div>
                  {/if}
                  <div class="mod-card-title">
                    <span class="mod-name">{mod.name}</span>
                    <span class="mod-author">by {mod.author}</span>
                  </div>
                </div>
                <p class="mod-desc">{mod.description}</p>
                <div class="mod-card-footer">
                  <span class="mod-downloads">{formatDownloads(mod.downloads)} downloads</span>
                  {#if installedIds.has(mod.source_id)}
                    <span class="badge-installed">Installed</span>
                  {:else}
                    <button
                      class="btn-sm accent"
                      onclick={() => handleInstallClick(mod)}
                      disabled={installingId === mod.source_id}
                    >
                      {installingId === mod.source_id ? 'Installing...' : 'Install'}
                    </button>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
          {#if searchTotal > searchResults.length}
            <p class="results-note">Showing {searchResults.length} of {searchTotal} results</p>
          {/if}
        {:else if searchQuery && !searching}
          <p class="empty">No results for "{searchQuery}"</p>
        {/if}
      {/if}
    </section>
  {/if}

  <!-- Version Picker Modal -->
  {#if versionPickerMod}
    <div class="modal-overlay" onclick={() => { versionPickerMod = null; }} role="presentation">
      <div class="modal" onclick={(e) => e.stopPropagation()} role="dialog">
        <div class="modal-header">
          <h3>Select Version — {versionPickerMod.name}</h3>
          <button class="modal-close" onclick={() => { versionPickerMod = null; }}>&times;</button>
        </div>
        <div class="modal-body">
          {#if loadingVersions}
            <p class="empty">Loading versions...</p>
          {:else if versions.length === 0}
            <p class="empty">No compatible versions found.</p>
          {:else}
            <div class="version-list">
              {#each versions as v}
                {@const isCompatible = !serverGameVersion || (v.game_versions?.includes(serverGameVersion) ?? true)}
                <div class="version-row" class:incompatible={!isCompatible}>
                  <div class="version-info">
                    <span class="version-num">{v.version}</span>
                    {#if v.game_version}
                      <span class="version-game" class:match={isCompatible} class:mismatch={!isCompatible}>{v.game_version}</span>
                    {/if}
                    {#if v.loader}
                      <span class="version-loader">{v.loader}</span>
                    {/if}
                  </div>
                  <button
                    class="btn-sm accent"
                    onclick={() => installMod(versionPickerMod!, v.version_id, v)}
                    disabled={installingId === versionPickerMod!.source_id}
                  >{isCompatible ? 'Install' : 'Install & Update Server'}</button>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
{/if}

<style>
  .section { margin-bottom: 28px; }
  .section-title {
    font-size: 0.9rem; font-weight: 600; color: var(--text-primary);
    margin-bottom: 12px;
  }

  .empty {
    color: var(--text-tertiary); font-size: 0.84rem;
    padding: 24px 0; text-align: center;
  }

  /* Loader section */
  .loader-section {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 10px;
    padding: 16px 20px;
    margin-bottom: 24px;
  }
  .loader-label {
    font-size: 0.84rem; font-weight: 600; color: var(--text-primary);
    margin-right: 12px;
  }
  .loader-toggle {
    display: flex; align-items: center; gap: 12px;
  }
  .loader-hint {
    font-size: 0.78rem; color: var(--text-tertiary);
  }
  .loader-hint-block {
    font-size: 0.8rem; color: var(--text-tertiary);
    margin-top: 12px; padding: 0;
  }
  .loader-picker {
    display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  }
  .loader-options { display: flex; gap: 4px; }
  .loader-option {
    padding: 7px 16px; border-radius: 6px;
    font-size: 0.82rem; font-weight: 450;
    background: var(--bg-inset); color: var(--text-tertiary);
    border: 1px solid transparent; cursor: pointer;
    transition: all 0.15s;
  }
  .loader-option:hover:not(:disabled) { color: var(--text-secondary); background: var(--bg-hover); }
  .loader-option.active {
    background: var(--accent-dim); color: var(--accent);
    border-color: var(--accent-border);
  }
  .loader-option:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Toggle (matches settings page toggle) */
  .toggle {
    width: 40px; height: 22px; border-radius: 11px;
    background: var(--bg-inset); border: 1px solid var(--border-subtle);
    position: relative; cursor: pointer;
    transition: all 0.2s;
    flex-shrink: 0;
  }
  .toggle::after {
    content: ''; position: absolute;
    top: 2px; left: 2px;
    width: 16px; height: 16px; border-radius: 50%;
    background: var(--text-tertiary);
    transition: all 0.2s;
  }
  .toggle.on { background: var(--accent); border-color: var(--accent); }
  .toggle.on::after { left: 20px; background: var(--bg-base); }
  .toggle:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Installed list */
  .installed-list {
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    overflow: hidden;
  }
  .installed-row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-dim);
    font-size: 0.84rem;
  }
  .installed-row:last-child { border-bottom: none; }
  .mod-info { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .mod-name { font-weight: 500; color: var(--text-primary); }
  .mod-version { font-family: var(--font-mono); font-size: 0.76rem; color: var(--text-tertiary); }
  .mod-source {
    font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.05em;
    padding: 2px 6px; border-radius: 4px;
    background: var(--bg-inset); color: var(--text-tertiary);
  }
  .badge-restart {
    font-size: 0.68rem; padding: 2px 6px; border-radius: 4px;
    background: rgba(234, 179, 8, 0.1); color: #eab308;
  }
  .mod-meta { display: flex; align-items: center; gap: 10px; flex-shrink: 0; }
  .mod-date { font-size: 0.76rem; color: var(--text-tertiary); }

  /* Source tabs */
  .source-tabs { display: flex; gap: 4px; margin-bottom: 12px; }
  .source-tab {
    padding: 6px 14px; border-radius: 6px;
    font-size: 0.8rem; font-weight: 450;
    background: var(--bg-inset); color: var(--text-tertiary);
    border: 1px solid transparent; cursor: pointer;
    text-transform: capitalize;
    transition: all 0.15s;
  }
  .source-tab:hover { color: var(--text-secondary); }
  .source-tab.active {
    background: var(--accent-dim); color: var(--accent);
    border-color: var(--accent-border);
  }

  /* Search */
  .search-bar { position: relative; margin-bottom: 16px; }
  .search-bar input {
    width: 100%; padding: 10px 14px;
    border-radius: 8px; border: 1px solid var(--border-subtle);
    background: var(--bg-inset); color: var(--text-primary);
    font-size: 0.84rem;
  }
  .search-bar input:focus { outline: none; border-color: var(--accent); }
  .searching {
    position: absolute; right: 12px; top: 50%; transform: translateY(-50%);
    font-size: 0.76rem; color: var(--text-tertiary);
  }

  /* Paste-by-ID */
  .paste-id { display: flex; gap: 8px; margin-bottom: 16px; }
  .paste-id input {
    flex: 1; padding: 10px 14px;
    border-radius: 8px; border: 1px solid var(--border-subtle);
    background: var(--bg-inset); color: var(--text-primary);
    font-size: 0.84rem;
  }
  .paste-id input:focus { outline: none; border-color: var(--accent); }

  /* Results grid */
  .results-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 12px;
  }
  .mod-card {
    border: 1px solid var(--border-subtle);
    border-radius: 10px; padding: 14px;
    background: var(--bg-surface);
    transition: border-color 0.15s;
  }
  .mod-card:hover { border-color: var(--border-hover); }
  .mod-card-header { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
  .mod-icon {
    width: 40px; height: 40px; border-radius: 8px;
    object-fit: cover; flex-shrink: 0;
  }
  .mod-icon.placeholder {
    display: flex; align-items: center; justify-content: center;
    background: var(--bg-inset); color: var(--text-tertiary);
  }
  .mod-icon.placeholder svg { width: 20px; height: 20px; }
  .mod-card-title { min-width: 0; }
  .mod-card-title .mod-name { display: block; font-weight: 500; font-size: 0.88rem; }
  .mod-card-title .mod-author { font-size: 0.76rem; color: var(--text-tertiary); }
  .mod-desc {
    font-size: 0.78rem; color: var(--text-secondary);
    line-height: 1.4; margin-bottom: 10px;
    display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .mod-card-footer { display: flex; justify-content: space-between; align-items: center; }
  .mod-downloads { font-size: 0.72rem; color: var(--text-tertiary); }
  .badge-installed {
    font-size: 0.72rem; padding: 3px 8px; border-radius: 4px;
    background: rgba(34, 197, 94, 0.1); color: #22c55e;
    font-weight: 500;
  }
  .results-note { font-size: 0.76rem; color: var(--text-tertiary); text-align: center; margin-top: 12px; }

  /* Buttons */
  .btn-sm {
    padding: 5px 12px; border-radius: 6px;
    font-size: 0.76rem; font-weight: 500;
    cursor: pointer; border: none;
    transition: all 0.15s;
  }
  .btn-sm:disabled { opacity: 0.5; cursor: not-allowed; }
  .btn-sm.accent { background: var(--accent); color: var(--bg-base); }
  .btn-sm.accent:hover:not(:disabled) { filter: brightness(1.1); }
  .btn-sm.danger { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
  .btn-sm.danger:hover:not(:disabled) { background: rgba(239, 68, 68, 0.2); }
  .btn-accent {
    padding: 10px 18px; border-radius: 8px;
    font-size: 0.84rem; font-weight: 500;
    background: var(--accent); color: var(--bg-base);
    border: none; cursor: pointer;
    transition: all 0.15s;
  }
  .btn-accent:hover:not(:disabled) { filter: brightness(1.1); }
  .btn-accent:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Version picker modal */
  .modal-overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 12px;
    width: 90%; max-width: 500px; max-height: 70vh;
    display: flex; flex-direction: column;
  }
  .modal-header {
    display: flex; justify-content: space-between; align-items: center;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border-dim);
  }
  .modal-header h3 { font-size: 0.94rem; font-weight: 600; margin: 0; }
  .modal-close {
    background: none; border: none; color: var(--text-tertiary);
    font-size: 1.4rem; cursor: pointer; padding: 0 4px;
  }
  .modal-close:hover { color: var(--text-primary); }
  .modal-body { padding: 16px 20px; overflow-y: auto; }
  .version-list { display: flex; flex-direction: column; gap: 6px; }
  .version-row {
    display: flex; justify-content: space-between; align-items: center;
    padding: 8px 12px; border-radius: 6px;
    border: 1px solid var(--border-dim);
  }
  .version-info { display: flex; align-items: center; gap: 8px; }
  .version-num { font-weight: 500; font-size: 0.84rem; font-family: var(--font-mono); }
  .version-row.incompatible { opacity: 0.7; }
  .version-game, .version-loader {
    font-size: 0.68rem; padding: 2px 5px; border-radius: 3px;
    background: var(--bg-inset); color: var(--text-tertiary);
  }
  .version-game.match { background: rgba(34, 197, 94, 0.1); color: #22c55e; }
  .version-game.mismatch { background: rgba(234, 179, 8, 0.1); color: #eab308; }

  @media (max-width: 700px) {
    .results-grid { grid-template-columns: 1fr; }
    .installed-row { flex-direction: column; align-items: flex-start; gap: 6px; }
    .loader-picker { flex-direction: column; align-items: flex-start; }
  }
</style>
