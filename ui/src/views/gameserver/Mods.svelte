<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type ModTabConfig, type InstalledMod, type ModSearchResult, type ModUpdate, type ModIssue } from '$lib/api';
  import { gameserverStore, toast, confirm } from '$lib/stores';
  import { onGameserverEvent } from '$lib/stores/sse';

  let { id }: { id: string } = $props();

  const can = (p: string) => gameserverStore.can(p);
  const canWrite = $derived(can('gameserver.mods.write'));
  const gsState = $derived(gameserverStore.getState(id));
  const gameserver = $derived(gsState?.gameserver ?? null);

  // Config state
  let config = $state<ModTabConfig | null>(null);
  let configLoading = $state(true);

  // Installed mods (all categories)
  let installedMods = $state<InstalledMod[]>([]);
  let installedLoading = $state(true);

  // Updates
  let updates = $state<ModUpdate[]>([]);

  // Active category
  let activeCategory = $state('');

  // Search
  let searchQuery = $state('');
  let searchResults = $state<ModSearchResult[]>([]);
  let searchTotal = $state(0);
  let searchLoading = $state(false);
  let searchTimer: ReturnType<typeof setTimeout> | null = null;
  let searchGeneration = 0;
  const SEARCH_LIMIT = 20;

  // In-flight operation tracking
  let installingIds = $state<Set<string>>(new Set());
  let uninstallingIds = $state<Set<string>>(new Set());
  let updatingIds = $state<Set<string>>(new Set());
  let updatingAll = $state(false);

  // Pack collapse state
  let collapsedPacks = $state<Set<string>>(new Set());
  let expandedPacks = $state<Set<string>>(new Set());

  // Derived: filter installed by active category
  const categoryMods = $derived(
    installedMods.filter(m => m.category === activeCategory)
  );

  // Derived: group installed into packs + standalone
  const groupedInstalled = $derived(() => {
    const packs = new Map<string, { packMod: InstalledMod; children: InstalledMod[] }>();
    const standalone: InstalledMod[] = [];

    for (const mod of categoryMods) {
      if (mod.delivery === 'pack' && !mod.pack_id) {
        // Pack header
        if (!packs.has(mod.id)) {
          packs.set(mod.id, { packMod: mod, children: [] });
        } else {
          packs.get(mod.id)!.packMod = mod;
        }
      } else if (mod.pack_id) {
        // Child of a pack
        if (!packs.has(mod.pack_id)) {
          packs.set(mod.pack_id, { packMod: null!, children: [mod] });
        } else {
          packs.get(mod.pack_id)!.children.push(mod);
        }
      } else {
        standalone.push(mod);
      }
    }

    return { packs: [...packs.values()].filter(p => p.packMod), standalone };
  });

  // Set of "source:source_id" for checking if a search result is already installed
  const installedSourceIds = $derived(
    new Set(installedMods.map(m => `${m.source}:${m.source_id}`))
  );

  // Map mod_id → ModUpdate for badge display
  const updatesMap = $derived(
    new Map(updates.map(u => [u.mod_id, u]))
  );

  // Whether loader should render as a toggle (2 options, one falsy)
  const loaderIsToggle = $derived(() => {
    if (!config?.loader) return false;
    const opts = config.loader.options;
    return opts.length === 2 && (opts[0].value === 'false' || opts[0].value === '' || opts[1].value === 'false' || opts[1].value === '');
  });

  // Loader toggle: which option is the "on" value
  const loaderOnValue = $derived(() => {
    if (!config?.loader) return '';
    const opts = config.loader.options;
    return opts.find(o => o.value !== 'false' && o.value !== '')?.value || opts[1]?.value || '';
  });

  const loaderIsOn = $derived(() => {
    if (!config?.loader) return false;
    return config.loader.current !== 'false' && config.loader.current !== '';
  });

  // Find which loaders enable a given source name
  function loadersForSource(sourceName: string): string[] {
    if (!config?.loader) return [];
    return config.loader.options
      .filter(o => o.mod_sources.includes(sourceName))
      .map(o => o.value);
  }

  // Check if the current loader supports a given source
  function currentLoaderSupportsSource(sourceName: string): boolean {
    if (!config?.loader) return true; // no loader config = all sources available
    const current = config.loader.options.find(o => o.value === config.loader!.current);
    return current?.mod_sources.includes(sourceName) ?? false;
  }

  // Whether the active category uses pack delivery
  const activeCategoryIsPack = $derived(() => {
    if (!config) return false;
    const cat = config.categories.find(c => c.name === activeCategory);
    return cat?.sources.some(s => s.delivery === 'pack') ?? false;
  });

  const updatableCount = $derived(updates.length);

  // SSE subscription
  let unsubSSE: (() => void) | null = null;

  onMount(async () => {
    await loadConfig();
    loadInstalled();
    checkForUpdates();

    unsubSSE = onGameserverEvent(id, (data: any) => {
      if (data.type === 'mod.installed' || data.type === 'mod.uninstalled') {
        loadInstalled();
        checkForUpdates();
      }
    });
  });

  onDestroy(() => {
    unsubSSE?.();
    if (searchTimer) clearTimeout(searchTimer);
  });

  async function loadConfig() {
    configLoading = true;
    try {
      config = await api.mods.config(id);
      if (config.categories.length > 0 && !activeCategory) {
        activeCategory = config.categories[0].name;
        doSearch('', config.categories[0].name, 0, false);
      }
    } catch (e: any) {
      toast(`Failed to load mod config: ${e.message}`, 'error');
    } finally {
      configLoading = false;
    }
  }

  async function loadInstalled() {
    try {
      installedMods = await api.mods.list(id);
    } catch (e: any) {
      console.warn('Mods: failed to load installed', e);
    } finally {
      installedLoading = false;
    }
  }

  async function checkForUpdates() {
    try {
      updates = await api.mods.updates(id);
    } catch {
      // Non-critical — silently ignore
    }
  }

  async function doSearch(query: string, category: string, offset: number, append: boolean) {
    const gen = ++searchGeneration;
    searchLoading = true;
    try {
      const result = await api.mods.search(id, {
        category,
        q: query || undefined,
        offset,
        limit: SEARCH_LIMIT,
      });
      // Stale response check
      if (gen !== searchGeneration) return;
      if (append) {
        searchResults = [...searchResults, ...result.results];
      } else {
        searchResults = result.results;
      }
      searchTotal = result.total;
    } catch (e: any) {
      if (gen !== searchGeneration) return;
      console.warn('Mods: search failed', e);
      if (!append) searchResults = [];
      searchTotal = 0;
    } finally {
      if (gen === searchGeneration) searchLoading = false;
    }
  }

  function handleSearchInput(value: string) {
    searchQuery = value;
    if (searchTimer) clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      doSearch(value, activeCategory, 0, false);
    }, 300);
  }

  function handleCategoryChange(categoryName: string) {
    activeCategory = categoryName;
    searchQuery = '';
    searchResults = [];
    doSearch('', categoryName, 0, false);
  }

  function loadMore() {
    doSearch(searchQuery, activeCategory, searchResults.length, true);
  }

  // Version/loader change with compatibility checking
  async function handleVersionChange(newVersion: string) {
    if (!config?.version || !gameserver) return;
    const currentEnv = typeof gameserver.env === 'string' ? JSON.parse(gameserver.env) : { ...gameserver.env };
    const newEnv = { ...currentEnv, [config.version.env]: newVersion };

    await applyEnvChange(newEnv, `version to ${newVersion}`);
  }

  async function handleLoaderChange(newLoader: string) {
    if (!config?.loader || !gameserver) return;
    const currentEnv = typeof gameserver.env === 'string' ? JSON.parse(gameserver.env) : { ...gameserver.env };
    const newEnv = { ...currentEnv, [config.loader.env]: newLoader };

    await applyEnvChange(newEnv, `loader to ${newLoader || 'none'}`);
  }

  function handleLoaderToggle() {
    if (!config?.loader) return;
    const newValue = loaderIsOn() ? (config.loader.options.find(o => o.value === 'false' || o.value === '')?.value || 'false') : loaderOnValue();
    handleLoaderChange(newValue);
  }

  async function applyEnvChange(newEnv: Record<string, string>, changeLabel: string) {
    try {
      const issues = await api.mods.checkCompatibility(id, newEnv);
      if (issues.length > 0) {
        const issueList = issues.map(i => `${i.mod_name}: ${i.reason}`).join('\n');
        const accepted = await confirm({
          title: 'Compatibility Warning',
          message: `Changing ${changeLabel} will affect ${issues.length} mod${issues.length !== 1 ? 's' : ''}:\n\n${issueList}\n\nContinue?`,
          confirmLabel: 'Continue',
          danger: true,
        });
        if (!accepted) return;
      }
    } catch {
      // Compatibility check failed — proceed anyway
    }

    try {
      await api.gameservers.update(id, { env: newEnv });
      toast('Configuration updated', 'success');
      await loadConfig();
      await loadInstalled();
      checkForUpdates();
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    }
  }

  // Install — handles loader switching, modpack version requirements, and restart
  async function installMod(result: ModSearchResult) {
    const isPack = activeCategoryIsPack();

    // For modpacks: fetch version info to check game version + loader requirements
    if (isPack) {
      const packReqs = await resolvePackRequirements(result);
      if (!packReqs) return; // user cancelled or error

      const { envChanges, descriptions, needsRestart } = packReqs;

      // Build combined confirmation message
      const actions: string[] = [];
      for (const desc of descriptions) actions.push(desc);
      actions.push('Download and install all mods in the pack');
      actions.push('May overwrite config files with pack defaults');
      if (needsRestart) actions.push('Restart the server to apply changes');

      const message = `Install "${result.name}"?\n\nThis will:\n${actions.map(a => `  - ${a}`).join('\n')}`;
      const accepted = await confirm({ title: 'Install Modpack', message, confirmLabel: 'Install' });
      if (!accepted) return;

      // Apply env changes (version + loader) before installing
      if (Object.keys(envChanges).length > 0) {
        try {
          const currentEnv = typeof gameserver!.env === 'string' ? JSON.parse(gameserver!.env) : { ...gameserver!.env };
          await api.gameservers.update(id, { env: { ...currentEnv, ...envChanges } });
          await loadConfig();
        } catch (e: any) {
          toast(`Failed to update configuration: ${e.message}`, 'error');
          return;
        }
      }

      // Install the pack
      const key = `${result.source}:${result.source_id}`;
      installingIds = new Set([...installingIds, key]);
      try {
        await api.mods.installPack(id, { source: result.source, pack_id: result.source_id });
        toast(`Installed ${result.name}`, 'success');
        await loadInstalled();
        checkForUpdates();

        // Trigger update-game to install the loader on next start
        if (needsRestart) {
          try {
            await api.gameservers.updateGame(id);
            toast('Server update started — loader will install on restart', 'info');
          } catch (e: any) {
            toast(`Pack installed, but failed to trigger update: ${e.message}. Manually update the game to apply.`, 'error');
          }
        }
      } catch (e: any) {
        toast(`Failed to install ${result.name}: ${e.message}`, 'error');
      } finally {
        const next = new Set(installingIds);
        next.delete(key);
        installingIds = next;
      }
      return;
    }

    // Single mod install — check loader compatibility
    if (!currentLoaderSupportsSource(result.source)) {
      const compatible = loadersForSource(result.source);
      if (compatible.length === 0) {
        toast(`No loader supports ${result.source} mods for this game`, 'error');
        return;
      }
      const loaderName = compatible[0];
      const accepted = await confirm({
        title: 'Loader Required',
        message: `${result.name} requires ${loaderName}. Switch to ${loaderName}?\n\nThe server will need a restart to apply.`,
        confirmLabel: `Switch to ${loaderName}`,
      });
      if (!accepted) return;

      if (!gameserver || !config?.loader) return;
      const currentEnv = typeof gameserver.env === 'string' ? JSON.parse(gameserver.env) : { ...gameserver.env };
      const newEnv = { ...currentEnv, [config.loader.env]: loaderName };
      try {
        await api.gameservers.update(id, { env: newEnv });
        await loadConfig();
      } catch (e: any) {
        toast(`Failed to switch loader: ${e.message}`, 'error');
        return;
      }
    }

    // Install the mod
    const key = `${result.source}:${result.source_id}`;
    installingIds = new Set([...installingIds, key]);
    try {
      await api.mods.install(id, { category: activeCategory, source: result.source, source_id: result.source_id });
      toast(`Installed ${result.name}`, 'success');
      await loadInstalled();
      checkForUpdates();
    } catch (e: any) {
      toast(`Failed to install ${result.name}: ${e.message}`, 'error');
    } finally {
      const next = new Set(installingIds);
      next.delete(key);
      installingIds = next;
    }
  }

  // Resolve what env changes a modpack needs before installation.
  // Returns null if the user should not proceed (error case).
  async function resolvePackRequirements(result: ModSearchResult): Promise<{
    envChanges: Record<string, string>;
    descriptions: string[];
    needsRestart: boolean;
  } | null> {
    if (!gameserver || !config) return null;

    const envChanges: Record<string, string> = {};
    const descriptions: string[] = [];
    let needsRestart = false;

    // Fetch the latest version to get game_version and loader
    let versions;
    try {
      versions = await api.mods.versions(id, { category: 'Modpacks', source: result.source, source_id: result.source_id });
    } catch (e: any) {
      toast(`Failed to fetch pack info: ${e.message}`, 'error');
      return null;
    }
    if (!versions || versions.length === 0) {
      toast('No versions available for this modpack', 'error');
      return null;
    }

    const packVersion = versions[0];

    // Check game version
    if (packVersion.game_version && config.version) {
      const current = config.version.current;
      if (current !== packVersion.game_version) {
        envChanges[config.version.env] = packVersion.game_version;
        descriptions.push(`Switch Minecraft from ${current || 'latest'} to ${packVersion.game_version}`);
        needsRestart = true;
      }
    }

    // Check loader
    if (packVersion.loader && config.loader) {
      const current = config.loader.current;
      if (current !== packVersion.loader) {
        // Find the loader option that matches — Modrinth returns "fabric", our options have the value
        const matchingOption = config.loader.options.find(o =>
          o.value === packVersion.loader || o.value.toLowerCase() === packVersion.loader.toLowerCase()
        );
        if (matchingOption) {
          envChanges[config.loader.env] = matchingOption.value;
          descriptions.push(`Switch loader from ${current || 'none'} to ${matchingOption.value}`);
          needsRestart = true;
        }
      }
    } else if (packVersion.loader && !config.loader) {
      // Game has no loader config but pack requires one — unusual, just note it
      descriptions.push(`Requires ${packVersion.loader} (not configurable for this game)`);
    }

    return { envChanges, descriptions, needsRestart };
  }

  // Uninstall
  async function uninstallMod(mod: InstalledMod) {
    const label = mod.delivery === 'pack' && !mod.pack_id ? `Remove modpack "${mod.name}" and all its mods?` : `Remove "${mod.name}"?`;
    if (!await confirm({ title: 'Uninstall Mod', message: label, confirmLabel: 'Uninstall', danger: true })) return;

    uninstallingIds = new Set([...uninstallingIds, mod.id]);
    try {
      await api.mods.uninstall(id, mod.id);
      installedMods = installedMods.filter(m => m.id !== mod.id && m.pack_id !== mod.id);
      toast(`Removed ${mod.name}`, 'info');
      checkForUpdates();
    } catch (e: any) {
      toast(`Failed to remove ${mod.name}: ${e.message}`, 'error');
    } finally {
      const next = new Set(uninstallingIds);
      next.delete(mod.id);
      uninstallingIds = next;
    }
  }

  // Update
  async function updateMod(modId: string) {
    updatingIds = new Set([...updatingIds, modId]);
    try {
      await api.mods.update(id, modId);
      toast('Mod updated', 'success');
      await loadInstalled();
      checkForUpdates();
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    } finally {
      const next = new Set(updatingIds);
      next.delete(modId);
      updatingIds = next;
    }
  }

  async function updatePack(packModId: string) {
    updatingIds = new Set([...updatingIds, packModId]);
    try {
      await api.mods.updatePack(id, packModId);
      toast('Modpack updated', 'success');
      await loadInstalled();
      checkForUpdates();
    } catch (e: any) {
      toast(`Failed to update pack: ${e.message}`, 'error');
    } finally {
      const next = new Set(updatingIds);
      next.delete(packModId);
      updatingIds = next;
    }
  }

  async function updateAllMods() {
    updatingAll = true;
    try {
      await api.mods.updateAll(id);
      toast('All mods updated', 'success');
      await loadInstalled();
      checkForUpdates();
    } catch (e: any) {
      toast(`Failed to update: ${e.message}`, 'error');
    } finally {
      updatingAll = false;
    }
  }

  // Helpers
  function findModName(modId: string): string {
    return installedMods.find(m => m.id === modId)?.name || 'unknown';
  }

  function formatDownloads(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`;
    return String(n);
  }

  function togglePackCollapse(packId: string) {
    const next = new Set(collapsedPacks);
    if (next.has(packId)) next.delete(packId);
    else next.add(packId);
    collapsedPacks = next;
  }

  function togglePackExpand(packId: string) {
    const next = new Set(expandedPacks);
    if (next.has(packId)) next.delete(packId);
    else next.add(packId);
    expandedPacks = next;
  }

  // Version picker grouping
  function hasGroups(options: { group?: string }[]): boolean {
    return options.some(o => o.group);
  }

  function groupOptions(options: { value: string; label: string; group?: string }[]): Map<string, typeof options> {
    const groups = new Map<string, typeof options>();
    for (const opt of options) {
      const g = opt.group || '';
      if (!groups.has(g)) groups.set(g, []);
      groups.get(g)!.push(opt);
    }
    return groups;
  }
</script>

{#if configLoading}
  <p class="loading-text">Loading...</p>
{:else if !config || config.categories.length === 0}
  <p class="loading-text">Mods not available for this game.</p>
{:else}
  <div class="mods-page">

    <!-- Config Header: Version + Loader pickers -->
    {#if config.version || config.loader}
      <div class="config-header">
        {#if config.version}
          <div class="config-field">
            <label class="label">Version</label>
            {#if hasGroups(config.version.options)}
              <select class="select" value={config.version.current}
                onchange={(e) => handleVersionChange((e.target as HTMLSelectElement).value)}
                disabled={!canWrite}>
                {#each groupOptions(config.version.options) as [group, opts]}
                  {#if group}
                    <optgroup label={group}>
                      {#each opts as opt}
                        <option value={opt.value} selected={opt.value === config.version!.current}>{opt.label}</option>
                      {/each}
                    </optgroup>
                  {:else}
                    {#each opts as opt}
                      <option value={opt.value} selected={opt.value === config.version!.current}>{opt.label}</option>
                    {/each}
                  {/if}
                {/each}
              </select>
            {:else}
              <select class="select" value={config.version.current}
                onchange={(e) => handleVersionChange((e.target as HTMLSelectElement).value)}
                disabled={!canWrite}>
                {#each config.version.options as opt}
                  <option value={opt.value} selected={opt.value === config.version!.current}>{opt.label}</option>
                {/each}
              </select>
            {/if}
          </div>
        {/if}

        {#if config.loader}
          <div class="config-field">
            <label class="label">Loader</label>
            {#if loaderIsToggle()}
              <div class="toggle-row">
                <button class="toggle" class:on={loaderIsOn()} onclick={handleLoaderToggle} disabled={!canWrite}></button>
                <span class="toggle-label">{loaderOnValue()}</span>
              </div>
            {:else}
              <select class="select" value={config.loader.current}
                onchange={(e) => handleLoaderChange((e.target as HTMLSelectElement).value)}
                disabled={!canWrite}>
                {#each config.loader.options as opt}
                  <option value={opt.value} selected={opt.value === config.loader!.current}>{opt.value || '(none)'}</option>
                {/each}
              </select>
            {/if}
          </div>
        {/if}
      </div>
    {/if}

    <!-- Category Tabs -->
    {#if config.categories.length > 1}
      <div class="category-tabs">
        {#each config.categories as cat}
          <button class="cat-tab" class:active={activeCategory === cat.name}
            onclick={() => handleCategoryChange(cat.name)}>
            {cat.name}
          </button>
        {/each}
      </div>
    {/if}

    <!-- Installed Section -->
    {#if categoryMods.length > 0}
      <div class="section-header">
        <span class="section-label">Installed ({categoryMods.length})</span>
        {#if canWrite && updatableCount > 0}
          <button class="btn-update-all" onclick={updateAllMods} disabled={updatingAll}>
            {updatingAll ? 'Updating...' : `Update All (${updatableCount})`}
          </button>
        {/if}
      </div>
      <div class="mods-panel">
        <!-- Pack groups -->
        {#each groupedInstalled().packs as pack (pack.packMod.id)}
          <div class="pack-group">
            <div class="mod-row pack-header" onclick={() => togglePackCollapse(pack.packMod.id)}>
              <div class="mod-info">
                <div class="mod-name-row">
                  <span class="pack-icon">◆</span>
                  <span class="mod-name">{pack.packMod.name}</span>
                  <span class="source-badge">{pack.packMod.source}</span>
                  <span class="pack-badge">modpack</span>
                  {#if updatesMap.has(pack.packMod.id)}
                    <span class="update-badge">↑ {updatesMap.get(pack.packMod.id)?.latest_version.version}</span>
                  {/if}
                </div>
                <div class="mod-meta">
                  {pack.children.length} mod{pack.children.length !== 1 ? 's' : ''} · {pack.packMod.version || 'latest'}
                </div>
              </div>
              <div class="mod-actions" onclick={(e) => e.stopPropagation()}>
                {#if canWrite && updatesMap.has(pack.packMod.id)}
                  <button class="mod-act update" onclick={() => updatePack(pack.packMod.id)}
                    disabled={updatingIds.has(pack.packMod.id)}>
                    {updatingIds.has(pack.packMod.id) ? 'Updating...' : 'Update Pack'}
                  </button>
                {/if}
                {#if canWrite}
                  <button class="mod-act danger" onclick={() => uninstallMod(pack.packMod)}
                    disabled={uninstallingIds.has(pack.packMod.id)}>✕</button>
                {/if}
                <span class="collapse-chevron" class:collapsed={collapsedPacks.has(pack.packMod.id)}>▾</span>
              </div>
            </div>
            {#if !collapsedPacks.has(pack.packMod.id)}
              {#each (expandedPacks.has(pack.packMod.id) ? pack.children : pack.children.slice(0, 5)) as child (child.id)}
                <div class="mod-row pack-child">
                  <div class="mod-info">
                    <div class="mod-name-row">
                      <span class="mod-name">{child.name}</span>
                      <span class="mod-version">{child.version}</span>
                      {#if child.auto_installed && child.depends_on}
                        <span class="dep-badge">dependency of {findModName(child.depends_on)}</span>
                      {/if}
                    </div>
                  </div>
                  {#if canWrite}
                    <div class="mod-actions">
                      <button class="mod-act danger" onclick={() => uninstallMod(child)}
                        disabled={uninstallingIds.has(child.id)}>✕</button>
                    </div>
                  {/if}
                </div>
              {/each}
              {#if pack.children.length > 5 && !expandedPacks.has(pack.packMod.id)}
                <button class="mod-row pack-child more-mods" onclick={() => togglePackExpand(pack.packMod.id)}>
                  + {pack.children.length - 5} more from {pack.packMod.name}
                </button>
              {:else if pack.children.length > 5 && expandedPacks.has(pack.packMod.id)}
                <button class="mod-row pack-child more-mods" onclick={() => togglePackExpand(pack.packMod.id)}>
                  Show less
                </button>
              {/if}
            {/if}
          </div>
        {/each}

        <!-- Standalone mods -->
        {#each groupedInstalled().standalone as mod (mod.id)}
          <div class="mod-row">
            <div class="mod-info">
              <div class="mod-name-row">
                <span class="mod-name">{mod.name}</span>
                <span class="mod-version">{mod.version}</span>
                <span class="source-badge">{mod.source}</span>
                {#if mod.auto_installed && mod.depends_on}
                  <span class="dep-badge">dependency of {findModName(mod.depends_on)}</span>
                {/if}
                {#if updatesMap.has(mod.id)}
                  <span class="update-badge">↑ {updatesMap.get(mod.id)?.latest_version.version}</span>
                {/if}
              </div>
            </div>
            <div class="mod-actions">
              {#if canWrite && updatesMap.has(mod.id)}
                <button class="mod-act update" onclick={() => updateMod(mod.id)}
                  disabled={updatingIds.has(mod.id)}>
                  {updatingIds.has(mod.id) ? 'Updating...' : 'Update'}
                </button>
              {/if}
              {#if canWrite}
                <button class="mod-act danger" onclick={() => uninstallMod(mod)}
                  disabled={uninstallingIds.has(mod.id)}>✕</button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <!-- Browse/Search Section -->
    <div class="section-header browse-header">
      <span class="section-label">Browse</span>
    </div>
    <div class="search-bar">
      <input class="input" type="text" placeholder="Search mods..."
        value={searchQuery}
        oninput={(e) => handleSearchInput((e.target as HTMLInputElement).value)} />
    </div>

    <div class="mods-panel">
      {#if searchLoading && searchResults.length === 0}
        <div class="mod-row"><span class="mod-name" style="color:var(--text-tertiary)">Searching...</span></div>
      {:else if searchResults.length === 0 && !searchLoading}
        <div class="mod-row"><span class="mod-name" style="color:var(--text-tertiary)">No results found</span></div>
      {:else}
        {#each searchResults as result (`${result.source}:${result.source_id}`)}
          <div class="mod-row search-result">
            {#if result.icon_url}
              <img class="mod-icon" src={result.icon_url} alt="" loading="lazy" />
            {:else}
              <div class="mod-icon placeholder">
                <span>{result.name.charAt(0)}</span>
              </div>
            {/if}
            <div class="mod-info">
              <div class="mod-name-row">
                <span class="mod-name">{result.name}</span>
                <span class="source-badge">{result.source}</span>
                <span class="mod-downloads">{formatDownloads(result.downloads)}</span>
              </div>
              <div class="mod-meta">
                by {result.author}
              </div>
              {#if result.description}
                <div class="mod-description">{result.description.length > 120 ? result.description.slice(0, 120) + '...' : result.description}</div>
              {/if}
            </div>
            <div class="mod-actions search-actions">
              {#if installedSourceIds.has(`${result.source}:${result.source_id}`)}
                <span class="installed-label">Installed</span>
              {:else if canWrite}
                <button class="btn-install" onclick={() => installMod(result)}
                  disabled={installingIds.has(`${result.source}:${result.source_id}`)}>
                  {installingIds.has(`${result.source}:${result.source_id}`) ? 'Installing...' : 'Install'}
                </button>
              {/if}
            </div>
          </div>
        {/each}

        {#if searchResults.length < searchTotal}
          <div class="load-more-row">
            <button class="btn-accent" onclick={loadMore} disabled={searchLoading}>
              {searchLoading ? 'Loading...' : 'Load More'}
            </button>
            <span class="results-count">
              Showing {searchResults.length} of {searchTotal}
            </span>
          </div>
        {/if}
      {/if}
    </div>
  </div>
{/if}

<style>
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .loading-text {
    color: var(--text-tertiary); text-align: center; padding: 40px;
    font-size: 0.85rem;
  }

  .mods-page {
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }

  /* Config Header */
  .config-header {
    display: flex; gap: 16px; align-items: flex-end;
    margin-bottom: 18px;
    padding: 18px 20px;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
  }
  .config-field {
    display: flex; flex-direction: column; gap: 6px;
    min-width: 160px;
  }
  .config-field .label {
    font-size: 0.68rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
  }
  .config-field .select {
    padding: 8px 12px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm); color: var(--text-primary);
    font-family: var(--font-body); font-size: 0.85rem; outline: none;
  }
  .config-field .select:focus { border-color: var(--accent-border); }
  .config-field .select:disabled { opacity: 0.5; }

  .toggle-row {
    display: flex; align-items: center; gap: 10px;
    font-size: 0.85rem; color: var(--text-secondary);
    height: 36px;
  }
  .toggle-label { text-transform: capitalize; }

  /* Category Tabs */
  .category-tabs {
    display: flex; gap: 2px;
    margin-bottom: 18px;
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    padding: 3px;
    border: 1px solid var(--border-dim);
  }
  .cat-tab {
    padding: 7px 16px;
    font-size: 0.8rem; font-weight: 450;
    color: var(--text-tertiary);
    background: none; border: none; border-radius: 4px;
    cursor: pointer; transition: color 0.15s, background 0.15s;
  }
  .cat-tab:hover { color: var(--text-secondary); }
  .cat-tab.active {
    color: var(--text-primary);
    background: var(--bg-elevated);
    box-shadow: 0 1px 2px rgba(0,0,0,0.2);
  }

  /* Section headers */
  .section-header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 10px;
  }
  .browse-header { margin-top: 24px; }
  .section-label {
    font-size: 0.68rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--text-tertiary);
  }
  .btn-update-all {
    padding: 5px 12px; border-radius: 4px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--caution); background: rgba(245,158,11,0.08);
    border: 1px solid rgba(245,158,11,0.15);
    cursor: pointer; transition: background 0.15s;
  }
  .btn-update-all:hover { background: rgba(245,158,11,0.14); }
  .btn-update-all:disabled { opacity: 0.5; pointer-events: none; }

  /* Mods Panel */
  .mods-panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
  }

  /* Mod Rows */
  .mod-row {
    display: flex; align-items: center;
    padding: 12px 18px;
    gap: 12px;
    transition: background 0.12s;
  }
  .mod-row:hover { background: var(--bg-elevated); }
  .mod-row + .mod-row { border-top: 1px solid var(--border-dim); }

  .mod-info { flex: 1; min-width: 0; }
  .mod-name-row {
    display: flex; align-items: center; gap: 8px;
    flex-wrap: wrap;
  }
  .mod-name {
    font-size: 0.86rem; font-weight: 500;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .mod-version {
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }
  .mod-meta {
    font-size: 0.72rem; color: var(--text-tertiary);
    margin-top: 2px;
  }
  .mod-description {
    font-size: 0.74rem; color: var(--text-tertiary);
    margin-top: 3px; line-height: 1.4;
  }
  .mod-downloads {
    font-size: 0.68rem; font-family: var(--font-mono);
    color: var(--text-tertiary); opacity: 0.7;
  }

  /* Badges */
  .source-badge {
    display: inline-block;
    padding: 1px 7px; border-radius: 100px;
    font-size: 0.62rem; font-family: var(--font-mono);
    font-weight: 500; text-transform: uppercase; letter-spacing: 0.04em;
    background: var(--bg-elevated);
    border: 1px solid var(--border-dim);
    color: var(--text-tertiary);
  }
  .pack-badge {
    display: inline-block;
    padding: 1px 7px; border-radius: 100px;
    font-size: 0.62rem; font-family: var(--font-mono);
    font-weight: 500; text-transform: uppercase; letter-spacing: 0.04em;
    background: rgba(232,114,42,0.08);
    border: 1px solid rgba(232,114,42,0.15);
    color: var(--accent);
  }
  .update-badge {
    display: inline-block;
    padding: 1px 7px; border-radius: 100px;
    font-size: 0.62rem; font-family: var(--font-mono);
    font-weight: 500;
    background: rgba(245,158,11,0.08);
    border: 1px solid rgba(245,158,11,0.15);
    color: var(--caution);
  }
  .dep-badge {
    font-size: 0.68rem; font-style: italic;
    color: var(--text-tertiary); opacity: 0.7;
  }
  .installed-label {
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); opacity: 0.6;
    padding: 5px 10px;
  }

  /* Mod Actions */
  .mod-actions {
    display: flex; gap: 2px; flex-shrink: 0;
    align-items: center;
    opacity: 0; transition: opacity 0.15s;
  }
  .mod-row:hover .mod-actions { opacity: 1; }
  .search-actions { opacity: 1; }

  .mod-act {
    padding: 5px 10px; border-radius: 4px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); background: none; border: none;
    cursor: pointer; transition: color 0.15s, background 0.15s;
  }
  .mod-act:hover { color: var(--accent); background: var(--accent-subtle); }
  .mod-act.update { color: var(--caution); }
  .mod-act.update:hover { background: rgba(245,158,11,0.06); }
  .mod-act.danger:hover { color: var(--danger); background: rgba(239,68,68,0.06); }
  .mod-act:disabled { opacity: 0.4; pointer-events: none; }

  .btn-install {
    padding: 6px 14px; border-radius: 6px;
    font-size: 0.78rem; font-weight: 500;
    color: var(--accent); background: var(--accent-dim);
    border: 1px solid var(--accent-border);
    cursor: pointer; transition: background 0.15s;
    white-space: nowrap;
  }
  .btn-install:hover { background: rgba(232,114,42,0.18); }
  .btn-install:disabled { opacity: 0.5; pointer-events: none; }

  /* Pack rows */
  .pack-header { cursor: pointer; }
  .pack-icon {
    color: var(--accent); font-size: 0.72rem;
  }
  .pack-child {
    padding-left: 36px;
    background: rgba(0,0,0,0.1);
  }
  .pack-child:hover { background: var(--bg-elevated); }
  .more-mods {
    font-size: 0.74rem; color: var(--text-tertiary);
    cursor: pointer; border: none; background: none;
    width: 100%; text-align: left;
    border-top: 1px solid var(--border-dim);
  }
  .more-mods:hover { color: var(--accent); background: var(--bg-elevated); }

  .collapse-chevron {
    font-size: 0.72rem; color: var(--text-tertiary);
    transition: transform 0.15s;
    display: inline-block;
    margin-left: 4px;
  }
  .collapse-chevron.collapsed { transform: rotate(-90deg); }

  /* Search */
  .search-bar { margin-bottom: 12px; }
  .search-bar .input {
    width: 100%; padding: 10px 14px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm); color: var(--text-primary);
    font-family: var(--font-body); font-size: 0.85rem; outline: none;
  }
  .search-bar .input:focus { border-color: var(--accent-border); }

  .search-result {
    padding: 14px 18px;
  }
  .mod-icon {
    width: 40px; height: 40px; border-radius: 8px;
    object-fit: cover; flex-shrink: 0;
    background: var(--bg-inset);
  }
  .mod-icon.placeholder {
    display: flex; align-items: center; justify-content: center;
    font-size: 0.9rem; font-weight: 600; color: var(--text-tertiary);
    border: 1px solid var(--border-dim);
  }

  /* Load More */
  .load-more-row {
    display: flex; align-items: center; gap: 12px;
    padding: 14px 18px;
    border-top: 1px solid var(--border-dim);
  }
  .results-count {
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }

  @media (max-width: 700px) {
    .config-header { flex-direction: column; align-items: stretch; }
    .config-field { min-width: 0; }
    .mod-actions { opacity: 1; }
    .mod-name-row { flex-wrap: wrap; }
    .search-result { flex-wrap: wrap; }
  }
</style>
