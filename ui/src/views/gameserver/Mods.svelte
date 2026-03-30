<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type ModTabConfig, type InstalledMod, type ModSearchResult, type ModUpdate, type ModIssue, type PackInstallResult, type ScanResult, type UntrackedFile } from '$lib/api';
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

  // Browse filters (independent from server config — for browsing only)
  let browseVersion = $state('');  // empty = use server's current version
  let browseLoader = $state('');   // empty = use server's current loader
  let browseSort = $state('downloads');

  // In-flight operation tracking
  let installingIds = $state<Set<string>>(new Set());
  let uninstallingIds = $state<Set<string>>(new Set());
  let updatingIds = $state<Set<string>>(new Set());
  let updatingAll = $state(false);

  // Pack collapse state
  let collapsedPacks = $state<Set<string>>(new Set());
  let expandedPacks = $state<Set<string>>(new Set());

  // Scan state
  let scanResult = $state<ScanResult | null>(null);
  let scanning = $state(false);

  // URL install state
  let showURLForm = $state(false);
  let urlInput = $state('');
  let urlNameInput = $state('');
  let installingURL = $state(false);

  // Upload state
  let uploadInput: HTMLInputElement | null = null;

  // All installed mods — shown regardless of which browse category is selected
  const categoryMods = $derived(installedMods);

  // Derived: group installed into packs + standalone
  // Pack headers may be in a different category (e.g., "Modpacks") than their children ("Mods"),
  // so we look up pack headers from all installed mods, not just the active category.
  const groupedInstalled = $derived(() => {
    const packs = new Map<string, { packMod: InstalledMod; children: InstalledMod[] }>();
    const standalone: InstalledMod[] = [];

    // Index all pack headers across all categories
    const packHeaders = new Map<string, InstalledMod>();
    for (const mod of installedMods) {
      if (mod.delivery === 'pack' && !mod.pack_id) {
        packHeaders.set(mod.id, mod);
      }
    }

    for (const mod of categoryMods) {
      if (mod.delivery === 'pack' && !mod.pack_id) {
        // Pack header in this category
        if (!packs.has(mod.id)) {
          packs.set(mod.id, { packMod: mod, children: [] });
        } else {
          packs.get(mod.id)!.packMod = mod;
        }
      } else if (mod.pack_id) {
        // Child of a pack — look up the pack header from all mods
        if (!packs.has(mod.pack_id)) {
          const header = packHeaders.get(mod.pack_id);
          packs.set(mod.pack_id, { packMod: header ?? null!, children: [mod] });
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
        version: browseVersion || undefined,
        loader: browseLoader || undefined,
        sort: browseSort || undefined,
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
    searchPage = 1;
    if (searchTimer) clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      doSearch(value, activeCategory, 0, false);
    }, 300);
  }

  function handleCategoryChange(categoryName: string) {
    activeCategory = categoryName;
    searchQuery = '';
    searchPage = 1;
    searchResults = [];
    doSearch('', categoryName, 0, false);
  }

  let searchPage = $state(1);
  const totalPages = $derived(Math.ceil(searchTotal / SEARCH_LIMIT) || 1);

  function goToPage(page: number) {
    searchPage = page;
    doSearch(searchQuery, activeCategory, (page - 1) * SEARCH_LIMIT, false);
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

  // Install a mod or modpack
  async function installMod(result: ModSearchResult) {
    const isPack = activeCategoryIsPack();

    // Modpack install flow
    if (isPack) {
      // Pre-check: fetch pack version to detect version/loader changes before installing
      let packVersion: { game_version: string; loader: string } | null = null;
      try {
        const versions = await api.mods.versions(id, { category: activeCategory, source: result.source, source_id: result.source_id, unfiltered: 'true' });
        if (versions?.length > 0) packVersion = versions[0];
      } catch { /* non-critical — proceed with generic confirmation */ }

      const currentVersion = config?.version?.current || '';
      const currentLoader = config?.loader?.current || '';
      const willChangeVersion = packVersion?.game_version && packVersion.game_version !== currentVersion;
      const willChangeLoader = packVersion?.loader && packVersion.loader !== currentLoader;
      const isDowngrade = willChangeVersion && currentVersion && gameserver?.installed;

      // Build confirmation message
      const actions: string[] = [];
      actions.push('Download and install all mods in the pack');
      if (willChangeVersion) actions.push(`Switch game version from ${currentVersion || 'current'} to ${packVersion!.game_version}`);
      if (willChangeLoader) actions.push(`Switch loader to ${packVersion!.loader}`);
      actions.push('May overwrite config files with pack defaults');

      if (isDowngrade) {
        // Version downgrade — show warning with reinstall option
        const choice = await confirm({
          title: 'Version Change Warning',
          message: `Install "${result.name}"?\n\nThis will:\n${actions.map(a => `• ${a}`).join('\n')}\n\nYour server has existing world data from ${currentVersion}. Changing to ${packVersion!.game_version} may make it incompatible. You should reinstall the server after installing this pack for a clean start.`,
          confirmLabel: 'Install Anyway',
          danger: true,
        });
        if (!choice) return;
      } else {
        const accepted = await confirm({
          title: 'Install Modpack',
          message: `Install "${result.name}"?\n\nThis will:\n${actions.map(a => `• ${a}`).join('\n')}`,
          confirmLabel: 'Install',
        });
        if (!accepted) return;
      }

      const key = `${result.source}:${result.source_id}`;
      installingIds = new Set([...installingIds, key]);
      try {
        const packResult = await api.mods.installPack(id, { source: result.source, pack_id: result.source_id });
        toast(`Installed ${result.name} (${packResult.mod_count} mods)`, 'success');

        // Show what the backend changed
        const changes: string[] = [];
        if (packResult.version_changed) changes.push(`Version set to ${packResult.version_changed}`);
        if (packResult.loader_changed) changes.push(`Loader set to ${packResult.loader_changed}`);
        if (changes.length > 0) toast(changes.join('. '), 'info');

        if (packResult.version_downgrade) {
          // Offer reinstall via modal
          const doReinstall = await confirm({
            title: 'Reinstall Recommended',
            message: `The game version was changed from ${currentVersion} to ${packResult.version_changed}. Existing world data may be incompatible.\n\nReinstall the server for a clean start? This will wipe all server data (mods will be re-downloaded automatically).`,
            confirmLabel: 'Reinstall',
            danger: true,
          });
          if (doReinstall) {
            try {
              await api.gameservers.reinstall(id);
              toast('Reinstall started — mods will be restored on next start', 'info');
            } catch (e: any) {
              toast(`Reinstall failed: ${e.message}`, 'error');
            }
          }
        } else if (packResult.needs_restart) {
          toast('Start or restart the server to apply changes', 'info');
        }

        await loadConfig();
        await loadInstalled();
        checkForUpdates();
      } catch (e: any) {
        const msg: string = e.message || '';
        if (msg.includes('already installed')) {
          await confirm({
            title: 'Modpack Already Installed',
            message: msg + '\n\nUninstall the existing modpack first, then try again.',
            confirmLabel: 'OK',
          });
        } else {
          toast(`Failed to install ${result.name}: ${msg}`, 'error');
        }
      } finally {
        const next = new Set(installingIds);
        next.delete(key);
        installingIds = next;
      }
      return;
    }

    // Single mod install
    const key = `${result.source}:${result.source_id}`;
    installingIds = new Set([...installingIds, key]);
    try {
      await api.mods.install(id, { category: activeCategory, source: result.source, source_id: result.source_id });
      toast(`Installed ${result.name}`, 'success');
      await loadInstalled();
      checkForUpdates();
    } catch (e: any) {
      const msg: string = e.message || '';

      if (msg.includes('no compatible version found')) {
        // Version mismatch — show modal with explanation
        await confirm({
          title: 'Incompatible Version',
          message: `${result.name}: ${msg}.\n\nTo install this mod, switch your game version from the picker above to one this mod supports, then try again.`,
          confirmLabel: 'OK',
        });
      } else if (msg.includes('not available') && !currentLoaderSupportsSource(result.source)) {
        // Loader mismatch — show modal with explanation
        const compatible = loadersForSource(result.source);
        const loaderHint = compatible.length > 0 ? `\n\nThis mod requires one of: ${compatible.join(', ')}. Switch your loader from the picker above, then try again.` : '';
        await confirm({
          title: 'Loader Required',
          message: `${result.name} isn't available with your current loader.${loaderHint}`,
          confirmLabel: 'OK',
        });
      } else {
        toast(`Failed to install ${result.name}: ${msg}`, 'error');
      }
    } finally {
      const next = new Set(installingIds);
      next.delete(key);
      installingIds = next;
    }
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

  // Scan
  async function runScan() {
    scanning = true;
    try {
      scanResult = await api.mods.scan(id);
    } catch (e: any) {
      toast(`Scan failed: ${e.message}`, 'error');
    } finally {
      scanning = false;
    }
  }

  async function trackFile(file: UntrackedFile) {
    try {
      await api.mods.trackFile(id, { category: file.category, name: file.name, path: file.path });
      toast(`Tracking ${file.name}`, 'success');
      await loadInstalled();
      await runScan();
    } catch (e: any) {
      toast(`Failed to track: ${e.message}`, 'error');
    }
  }

  function dismissScan() {
    scanResult = null;
  }

  // URL install
  async function installFromURL() {
    if (!urlInput || !urlNameInput) return;
    installingURL = true;
    try {
      await api.mods.installURL(id, { category: activeCategory, name: urlNameInput, url: urlInput });
      toast(`Installed ${urlNameInput}`, 'success');
      urlInput = '';
      urlNameInput = '';
      showURLForm = false;
      await loadInstalled();
    } catch (e: any) {
      toast(`Failed to install from URL: ${e.message}`, 'error');
    } finally {
      installingURL = false;
    }
  }

  // Upload
  async function handleUpload(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    try {
      await api.mods.upload(id, activeCategory, file.name, file);
      toast(`Uploaded ${file.name}`, 'success');
      await loadInstalled();
    } catch (err: any) {
      toast(`Upload failed: ${err.message}`, 'error');
    } finally {
      input.value = '';
    }
  }
</script>

{#if configLoading}
  <p class="loading-text">Loading...</p>
{:else if !config || (config.categories.length === 0 && !config.loader && !config.version)}
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
            <select class="select" value={config.loader.current}
              onchange={(e) => handleLoaderChange((e.target as HTMLSelectElement).value)}
              disabled={!canWrite}>
              {#each config.loader.options as opt}
                <option value={opt.value} selected={opt.value === config.loader!.current}>
                  {opt.value === 'false' || opt.value === '' ? '(disabled)' : opt.value}
                </option>
              {/each}
            </select>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Installed Section -->
    <div class="section-header">
      <span class="section-label">Installed ({categoryMods.length})</span>
      <div class="section-actions">
        <button class="btn-scan" onclick={runScan} disabled={scanning}>
          {scanning ? 'Scanning...' : 'Scan for local mods'}
        </button>
        {#if canWrite && updatableCount > 0}
          <button class="btn-update-all" onclick={updateAllMods} disabled={updatingAll}>
            {updatingAll ? 'Updating...' : `Update All (${updatableCount})`}
          </button>
        {/if}
      </div>
    </div>

    <!-- Scan Results -->
    {#if scanResult}
      <div class="scan-panel">
        <div class="scan-header">
          <span class="section-label">Scan Results</span>
          <button class="mod-act" onclick={dismissScan}>Dismiss</button>
        </div>
        {#if scanResult.untracked.length > 0}
          <div class="scan-group">
            <div class="scan-group-label">Untracked ({scanResult.untracked.length})</div>
            {#each scanResult.untracked as file}
              <div class="mod-row">
                <div class="mod-info">
                  <div class="mod-name-row">
                    <span class="mod-name">{file.name}</span>
                    <span class="mod-version">{file.category}</span>
                  </div>
                  <div class="mod-meta">{file.path}</div>
                </div>
                {#if canWrite}
                  <div class="mod-actions" style="opacity:1;">
                    <button class="btn-install" onclick={() => trackFile(file)}>Track</button>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        {#if scanResult.missing.length > 0}
          <div class="scan-group">
            <div class="scan-group-label">Missing ({scanResult.missing.length})</div>
            {#each scanResult.missing as mod}
              <div class="mod-row">
                <div class="mod-info">
                  <div class="mod-name-row">
                    <span class="mod-name">{mod.name}</span>
                    <span class="source-badge">{mod.source}</span>
                  </div>
                  <div class="mod-meta">{mod.file_path}</div>
                </div>
                {#if canWrite}
                  <div class="mod-actions" style="opacity:1;">
                    <button class="mod-act danger" onclick={() => uninstallMod(mod)}>Remove</button>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        {#if scanResult.untracked.length === 0 && scanResult.missing.length === 0}
          <div class="mod-row"><span class="mod-name" style="color:var(--text-tertiary)">All mods accounted for</span></div>
        {/if}
      </div>
    {/if}

    {#if categoryMods.length > 0}
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
      {#if canWrite}
        <div class="section-actions">
          <button class="btn-scan" onclick={() => showURLForm = !showURLForm}>
            {showURLForm ? 'Cancel' : 'Install from URL'}
          </button>
          <button class="btn-scan" onclick={() => uploadInput?.click()}>Upload</button>
          <input type="file" bind:this={uploadInput} onchange={handleUpload} style="display:none;" />
        </div>
      {/if}
    </div>

    {#if showURLForm}
      <div class="url-form">
        <input class="input" type="text" placeholder="Mod name" bind:value={urlNameInput} />
        <input class="input" type="url" placeholder="https://example.com/mod.jar" bind:value={urlInput} />
        <button class="btn-install" onclick={installFromURL} disabled={installingURL || !urlInput || !urlNameInput}>
          {installingURL ? 'Installing...' : 'Install'}
        </button>
      </div>
    {/if}

    <div class="browse-filters">
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
      {#if config.version}
        <select class="select filter-select" value={browseVersion}
          onchange={(e) => { browseVersion = (e.target as HTMLSelectElement).value; doSearch(searchQuery, activeCategory, 0, false); }}>
          <option value="">All versions</option>
          {#each config.version.options as opt}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      {/if}
      {#if config.loader}
        <select class="select filter-select" value={browseLoader}
          onchange={(e) => { browseLoader = (e.target as HTMLSelectElement).value; doSearch(searchQuery, activeCategory, 0, false); }}>
          <option value="">All loaders</option>
          {#each config.loader.options as opt}
            {#if opt.mod_sources.length > 0}
              <option value={opt.value}>{opt.value}</option>
            {/if}
          {/each}
        </select>
      {/if}
      <select class="select filter-select" value={browseSort}
        onchange={(e) => { browseSort = (e.target as HTMLSelectElement).value; doSearch(searchQuery, activeCategory, 0, false); }}>
        <option value="downloads">Most popular</option>
        <option value="relevance">Relevance</option>
        <option value="updated">Recently updated</option>
        <option value="newest">Newest</option>
      </select>
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
                {#if result.author}by {result.author}{/if}
                {#if result.loaders?.length}
                  {#if result.author}<span class="mod-meta-sep">·</span>{/if}
                  {#each result.loaders as loader}
                    <span class="loader-badge">{loader}</span>
                  {/each}
                {/if}
                {#if result.game_versions?.length}
                  <span class="mod-meta-sep">·</span>
                  <span class="version-info">{[...result.game_versions].reverse().slice(0, 3).join(', ')}{result.game_versions.length > 3 ? ` +${result.game_versions.length - 3}` : ''}</span>
                {/if}
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

        {#if totalPages > 1}
          <div class="pagination-row">
            <button class="page-btn" onclick={() => goToPage(searchPage - 1)} disabled={searchPage <= 1 || searchLoading}>←</button>
            <span class="page-info">Page {searchPage} of {totalPages}</span>
            <button class="page-btn" onclick={() => goToPage(searchPage + 1)} disabled={searchPage >= totalPages || searchLoading}>→</button>
            <span class="results-count">{searchTotal} results</span>
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

  /* Category Tabs — inline with browse filters */
  .category-tabs {
    display: flex; gap: 2px;
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    padding: 3px;
    border: 1px solid var(--border-dim);
  }
  .cat-tab {
    padding: 5px 12px;
    font-size: 0.78rem; font-weight: 450;
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
    font-size: 0.82rem; font-weight: 550;
    color: var(--text-secondary);
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

  /* Pagination */
  .pagination-row {
    display: flex; align-items: center; gap: 10px;
    padding: 12px 18px;
    border-top: 1px solid var(--border-dim);
  }
  .page-btn {
    padding: 5px 12px; border-radius: 4px;
    font-size: 0.78rem; font-family: var(--font-mono);
    color: var(--text-secondary); background: var(--bg-elevated);
    border: 1px solid var(--border-dim);
    cursor: pointer; transition: color 0.15s, border-color 0.15s;
  }
  .page-btn:hover { color: var(--text-primary); border-color: var(--border); }
  .page-btn:disabled { opacity: 0.3; pointer-events: none; }
  .page-info {
    font-size: 0.76rem; font-family: var(--font-mono);
    color: var(--text-secondary);
  }
  .results-count {
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); margin-left: auto;
  }

  @media (max-width: 700px) {
    .config-header { flex-direction: column; align-items: stretch; }
    .config-field { min-width: 0; }
    .mod-actions { opacity: 1; }
    .mod-name-row { flex-wrap: wrap; }
    .search-result { flex-wrap: wrap; }
    .section-actions { flex-wrap: wrap; }
    .url-form { flex-direction: column; }
  }

  /* Scan */
  .section-actions { display: flex; gap: 6px; align-items: center; }
  .btn-scan {
    padding: 4px 10px; border-radius: 4px;
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); background: var(--bg-elevated);
    border: 1px solid var(--border-dim);
    cursor: pointer; transition: color 0.15s, border-color 0.15s;
  }
  .btn-scan:hover { color: var(--text-secondary); border-color: var(--border); }
  .btn-scan:disabled { opacity: 0.5; pointer-events: none; }

  .scan-panel {
    background: var(--bg-surface);
    border: 1px solid var(--accent-border);
    border-radius: var(--radius);
    overflow: hidden;
    margin-bottom: 16px;
  }
  .scan-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 10px 18px;
    border-bottom: 1px solid var(--border-dim);
  }
  .scan-group { border-top: 1px solid var(--border-dim); }
  .scan-group:first-child { border-top: none; }
  .scan-group-label {
    padding: 8px 18px;
    font-size: 0.68rem; font-family: var(--font-mono);
    text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--text-tertiary);
    background: var(--bg-inset);
  }

  /* URL install form */
  .url-form {
    display: flex; gap: 8px; align-items: center;
    margin-bottom: 12px;
    padding: 12px 16px;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
  }
  .url-form .input { flex: 1; }

  /* Browse filters */
  .browse-filters {
    display: flex; gap: 8px; align-items: center;
    margin-bottom: 10px;
  }
  .filter-select {
    padding: 6px 10px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm); color: var(--text-secondary);
    font-family: var(--font-body); font-size: 0.78rem; outline: none;
  }
  .filter-select:focus { border-color: var(--accent-border); }

  .loader-badge {
    display: inline-block;
    padding: 0 5px; border-radius: 3px;
    font-size: 0.62rem; font-family: var(--font-mono);
    background: var(--bg-elevated); border: 1px solid var(--border-dim);
    color: var(--text-tertiary);
  }
  .version-info {
    font-size: 0.66rem; font-family: var(--font-mono);
    color: var(--text-tertiary); opacity: 0.8;
  }
  .mod-meta-sep { opacity: 0.3; font-size: 0.72rem; }
</style>
