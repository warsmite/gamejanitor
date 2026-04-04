<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Token, type WebhookEndpoint, type WorkerView, type Event } from '$lib/api';
  import { toast, confirm, prompt, setToken, gameserverStore, isAdmin } from '$lib/stores';
  import { getRoute, navigate } from '$lib/router';

  const canEdit = $derived($isAdmin);
  const canTokens = $derived($isAdmin);
  const canWebhooks = $derived($isAdmin);
  const canNodes = $derived($isAdmin);

  let loading = $state(true);
  let saving = $state(false);
  const validSections = ['general', 'security', 'tokens', 'webhooks', 'workers', 'events'];
  const initialSection = getRoute().params.section || 'general';
  let activeSection = $state(validSections.includes(initialSection) ? initialSection : 'general');

  // Settings state (editable only — read-only config comes from status)
  let settings = $state<Record<string, any>>({});
  let config = $state<Record<string, any>>({});

  // Tokens state
  let tokens = $state<Token[]>([]);
  let workerTokens = $state<Token[]>([]);
  let showCreateToken = $state(false);
  let newTokenName = $state('');
  let newTokenRole = $state('admin');
  let newTokenCanCreate = $state(false);
  let newTokenMaxGs = $state('');
  let newTokenMaxMem = $state('');
  let newTokenMaxCpu = $state('');
  let newTokenMaxStorage = $state('');
  let creatingToken = $state(false);
  let revealedToken = $state('');

  // Webhooks state
  let webhooks = $state<WebhookEndpoint[]>([]);
  let showCreateWebhook = $state(false);
  let newWebhookUrl = $state('');
  let newWebhookDesc = $state('');
  let newWebhookEvents = $state<string[]>([]);
  let creatingWebhook = $state(false);

  // Workers state
  let workers = $state<WorkerView[]>([]);

  // Events state
  let events = $state<Event[]>([]);
  let eventFilter = $state('');
  let eventGsFilter = $state('');
  let eventsPage = $state(0);
  let eventsHasMore = $state(true);

  const sections = [
    { id: 'general', label: 'General', icon: 'M8 4.754a3.246 3.246 0 1 0 0 6.492 3.246 3.246 0 0 0 0-6.492zM5.754 8a2.246 2.246 0 1 1 4.492 0 2.246 2.246 0 0 1-4.492 0z M9.796 1.343c-.527-1.79-3.065-1.79-3.592 0l-.094.319a.873.873 0 0 1-1.255.52l-.292-.16c-1.64-.892-3.433.902-2.54 2.541l.159.292a.873.873 0 0 1-.52 1.255l-.319.094c-1.79.527-1.79 3.065 0 3.592l.319.094a.873.873 0 0 1 .52 1.255l-.16.292c-.892 1.64.901 3.434 2.541 2.54l.292-.159a.873.873 0 0 1 1.255.52l.094.319c.527 1.79 3.065 1.79 3.592 0l.094-.319a.873.873 0 0 1 1.255-.52l.292.16c1.64.893 3.434-.902 2.54-2.541l-.159-.292a.873.873 0 0 1 .52-1.255l.319-.094c1.79-.527 1.79-3.065 0-3.592l-.319-.094a.873.873 0 0 1-.52-1.255l.16-.292c.893-1.64-.902-3.433-2.541-2.54l-.292.159a.873.873 0 0 1-1.255-.52l-.094-.319zm-2.633.283c.246-.835 1.428-.835 1.674 0l.094.319a1.873 1.873 0 0 0 2.693 1.115l.291-.16c.764-.415 1.6.42 1.184 1.185l-.159.292a1.873 1.873 0 0 0 1.116 2.692l.318.094c.835.246.835 1.428 0 1.674l-.319.094a1.873 1.873 0 0 0-1.115 2.693l.16.291c.415.764-.421 1.6-1.185 1.184l-.291-.159a1.873 1.873 0 0 0-2.693 1.116l-.094.318c-.246.835-1.428.835-1.674 0l-.094-.319a1.873 1.873 0 0 0-2.692-1.115l-.292.16c-.764.415-1.6-.421-1.184-1.185l.159-.291A1.873 1.873 0 0 0 1.945 8.93l-.319-.094c-.835-.246-.835-1.428 0-1.674l.319-.094A1.873 1.873 0 0 0 3.06 4.377l-.16-.292c-.415-.764.42-1.6 1.185-1.184l.292.159a1.873 1.873 0 0 0 2.692-1.115l.094-.319z' },
    { id: 'security', label: 'Security', icon: 'M8 1a2 2 0 0 1 2 2v4H6V3a2 2 0 0 1 2-2zm3 6V3a3 3 0 0 0-6 0v4a2 2 0 0 0-2 2v5a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2z' },
    { id: 'tokens', label: 'Tokens', icon: 'M5.338 1.59a61.44 61.44 0 0 0-2.837.856.481.481 0 0 0-.328.39c-.554 4.157.726 7.19 2.253 9.188a10.725 10.725 0 0 0 2.287 2.233c.346.244.652.42.893.533.12.057.218.095.293.118a.55.55 0 0 0 .101.025.615.615 0 0 0 .1-.025c.076-.023.174-.061.294-.118.24-.113.547-.29.893-.533a10.726 10.726 0 0 0 2.287-2.233c1.527-1.997 2.807-5.031 2.253-9.188a.48.48 0 0 0-.328-.39c-.651-.213-1.75-.56-2.837-.855C9.552 1.29 8.531 1.067 8 1.067c-.53 0-1.552.223-2.662.524z' },
    { id: 'webhooks', label: 'Webhooks', icon: 'M4.715 6.542 3.343 7.914a3 3 0 1 0 4.243 4.243l1.828-1.829A3 3 0 0 0 8.586 5.5L8 6.086a1.002 1.002 0 0 0-.154.199 2 2 0 0 1 .861 3.337L6.88 11.45a2 2 0 1 1-2.83-2.83l.793-.792a4.018 4.018 0 0 1-.128-1.287z M6.586 4.672A3 3 0 0 0 7.414 9.5l.775-.776a2 2 0 0 1-.896-3.346L9.12 3.55a2 2 0 1 1 2.83 2.83l-.793.792c.112.42.155.855.128 1.287l1.372-1.372a3 3 0 1 0-4.243-4.243L6.586 4.672z' },
    { id: 'workers', label: 'Workers', icon: 'M4 1a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V2a1 1 0 0 0-1-1H4zm0 1h8v12H4V2zm2 1a.5.5 0 0 0 0 1h4a.5.5 0 0 0 0-1H6zm0 2a.5.5 0 0 0 0 1h4a.5.5 0 0 0 0-1H6zm0 2a.5.5 0 0 0 0 1h4a.5.5 0 0 0 0-1H6zm-1 5a1 1 0 1 0 2 0 1 1 0 0 0-2 0z' },
    { id: 'events', label: 'Events', icon: 'M8.515 1.019A7 7 0 0 0 8 1V0a8 8 0 0 1 .589.022l-.074.997zm2.004.45a7.003 7.003 0 0 0-.985-.299l.219-.976c.383.086.76.2 1.126.342l-.36.933zm1.37.71a7.01 7.01 0 0 0-.439-.27l.493-.87a8.025 8.025 0 0 1 .979.654l-.615.789a6.996 6.996 0 0 0-.418-.302zm1.834 1.79a6.99 6.99 0 0 0-.653-.796l.724-.69c.27.285.52.59.747.91l-.818.576zm.744 1.352a7.08 7.08 0 0 0-.214-.468l.893-.45a7.976 7.976 0 0 1 .45 1.088l-.95.313a7.023 7.023 0 0 0-.179-.483zm.53 2.507a6.991 6.991 0 0 0-.1-1.025l.985-.17c.067.386.106.778.116 1.17l-1 .025zm-.131 1.538c.033-.17.06-.339.081-.51l.993.123a7.957 7.957 0 0 1-.23 1.155l-.964-.267c.046-.165.086-.332.12-.501zm-.952 2.379c.184-.29.346-.594.486-.908l.914.405c-.16.36-.345.706-.555 1.038l-.845-.535zm-.964 1.205c.122-.122.239-.248.35-.378l.758.653a8.073 8.073 0 0 1-.401.432l-.707-.707z M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1z' },
  ];

  onMount(async () => {
    try {
      const [s, status] = await Promise.all([
        api.settings.get(),
        api.clusterStatus.get(),
      ]);
      settings = s;
      config = status.config || {};
    } catch (e: any) {
      toast(`Failed to load settings: ${e.message}`, 'error');
    } finally {
      loading = false;
    }

    // Lazy-load initial section if deep-linked
    if (activeSection !== 'general') {
      switchSection(activeSection);
    }
  });

  async function saveSettings() {
    saving = true;
    try {
      settings = await api.settings.update(settings);
      toast('Settings saved', 'success');
    } catch (e: any) {
      toast(`Failed to save: ${e.message}`, 'error');
    } finally {
      saving = false;
    }
  }

  // Lazy-load section data on first visit
  let loadedSections = $state<Set<string>>(new Set(['general']));

  async function switchSection(id: string) {
    activeSection = id;
    window.history.replaceState(null, '', id === 'general' ? '/settings' : `/settings/${id}`);
    if (loadedSections.has(id)) return;
    loadedSections.add(id);

    try {
      if (id === 'tokens') {
        [tokens, workerTokens] = await Promise.all([
          api.tokens.list(),
          api.tokens.list(), // worker tokens use same endpoint for now
        ]);
      } else if (id === 'webhooks') {
        webhooks = await api.webhooks.list();
      } else if (id === 'workers') {
        workers = await api.workers.list();
      } else if (id === 'events') {
        await loadEvents();
      }
    } catch (e: any) {
      toast(`Failed to load ${id}: ${e.message}`, 'error');
    }
  }

  // Token actions
  async function createToken() {
    creatingToken = true;
    try {
      const body: any = { name: newTokenName, role: newTokenRole };
      if (newTokenRole === 'user') {
        body.can_create = newTokenCanCreate;
        if (newTokenMaxGs) body.max_gameservers = parseInt(newTokenMaxGs);
        if (newTokenMaxMem) body.max_memory_mb = parseInt(newTokenMaxMem);
        if (newTokenMaxCpu) body.max_cpu = parseFloat(newTokenMaxCpu);
        if (newTokenMaxStorage) body.max_storage_mb = parseInt(newTokenMaxStorage);
      }
      const result = await api.tokens.create(body);
      revealedToken = result.raw_token || result.token || '';
      tokens = await api.tokens.list();
      showCreateToken = false;
      newTokenName = '';
      newTokenMaxGs = '';
      newTokenMaxMem = '';
      newTokenMaxCpu = '';
      newTokenMaxStorage = '';
    } catch (e: any) {
      toast(`Failed to create token: ${e.message}`, 'error');
    } finally {
      creatingToken = false;
    }
  }

  async function shareToken(id: string) {
    try {
      // Generate (or regenerate) a claim code
      const result = await api.tokens.generateClaimCode(id);
      const link = `${window.location.origin}/invite/${result.claim_code}`;

      try {
        await navigator.clipboard.writeText(link);
      } catch {
        const ta = document.createElement('textarea');
        ta.value = link;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      toast('Invite link copied to clipboard', 'success');

      // Update the token in the list to show it has a claim code
      const idx = tokens.findIndex(t => t.id === id);
      if (idx >= 0) {
        tokens[idx] = { ...tokens[idx], claim_code: result.claim_code };
      }
    } catch (e: any) {
      toast(`Failed to generate invite link: ${e.message}`, 'error');
    }
  }

  async function deleteToken(id: string, name: string) {
    if (!await confirm({ title: 'Delete Token', message: `Delete token "${name}"? Any integrations using it will stop working.`, confirmLabel: 'Delete', danger: true })) return;
    try {
      await api.tokens.delete(id);
      tokens = tokens.filter(t => t.id !== id);
    } catch (e: any) {
      toast(`Failed to delete: ${e.message}`, 'error');
    }
  }

  // Webhook actions
  async function createWebhook() {
    creatingWebhook = true;
    try {
      await api.webhooks.create({ url: newWebhookUrl, description: newWebhookDesc, events: newWebhookEvents, enabled: true });
      webhooks = await api.webhooks.list();
      showCreateWebhook = false;
      newWebhookUrl = '';
      newWebhookDesc = '';
      newWebhookEvents = [];
    } catch (e: any) {
      toast(`Failed to create webhook: ${e.message}`, 'error');
    } finally {
      creatingWebhook = false;
    }
  }

  async function toggleWebhook(wh: WebhookEndpoint) {
    try {
      await api.webhooks.update(wh.id, { enabled: !wh.enabled });
      webhooks = webhooks.map(w => w.id === wh.id ? { ...w, enabled: !w.enabled } : w);
    } catch (e: any) {
      toast(`Failed to toggle: ${e.message}`, 'error');
    }
  }

  async function deleteWebhook(wh: WebhookEndpoint) {
    if (!await confirm({ title: 'Delete Webhook', message: `Delete webhook to "${wh.url}"?`, confirmLabel: 'Delete', danger: true })) return;
    try {
      await api.webhooks.delete(wh.id);
      webhooks = webhooks.filter(w => w.id !== wh.id);
    } catch (e: any) {
      toast(`Failed to delete: ${e.message}`, 'error');
    }
  }

  async function testWebhook(wh: WebhookEndpoint) {
    try {
      const result = await api.webhooks.test(wh.id);
      toast(result.success ? `Test delivered (${result.response_status})` : `Test failed (${result.response_status})`, result.success ? 'success' : 'error');
    } catch (e: any) {
      toast(`Test failed: ${e.message}`, 'error');
    }
  }

  // Events
  async function loadEvents() {
    try {
      const params: any = { limit: 50, offset: eventsPage * 50 };
      if (eventFilter) params.type = eventFilter;
      if (eventGsFilter) params.gameserver_id = eventGsFilter;
      const result = await api.events.history(params);
      events = result;
      eventsHasMore = result.length === 50;
    } catch (e: any) {
      toast(`Failed to load events: ${e.message}`, 'error');
    }
  }

  function formatDate(iso: string | undefined): string {
    if (!iso) return '—';
    return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit' });
  }

  function shortDate(iso: string): string {
    return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
  }
</script>

<main class="settings-layout">
  <!-- Sidebar -->
  <aside class="sidebar">
    <div class="sidebar-title">Settings</div>
    {#each sections as section}
      {#if section.id === 'general' || section.id === 'security' || section.id === 'events'
        || (section.id === 'tokens' && canTokens)
        || (section.id === 'webhooks' && canWebhooks)
        || (section.id === 'workers' && canNodes)}
        <button
          class="sidebar-link"
          class:active={activeSection === section.id}
          onclick={() => switchSection(section.id)}
        >
          <svg viewBox="0 0 16 16" fill="currentColor"><path d={section.icon}/></svg>
          {section.label}
        </button>
      {/if}
    {/each}
  </aside>

  <!-- Content -->
  <div class="content">
    {#if loading}
      <p class="loading-text">Loading settings...</p>

    {:else if activeSection === 'general'}
      <!-- ═══════════ GENERAL ═══════════ -->
      <div class="section" style="animation-delay:0s">
        <div class="section-head">
          <h2>General</h2>
          <p class="section-desc">Network, ports, and backup configuration.</p>
        </div>

        <div class="panel info-panel">
          <div class="s-title">Server Info</div>
          <div class="info-grid">
            <div class="info-item">
              <span class="info-label">Bind</span>
              <span class="info-value">{config.bind || '—'}:{config.port || '—'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">SFTP</span>
              <span class="info-value">:{config.sftp_port || '—'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">gRPC</span>
              <span class="info-value">:{config.grpc_port || '—'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">Data Dir</span>
              <span class="info-value">{config.data_dir || '—'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">Runtime</span>
              <span class="info-value">{config.runtime || 'auto'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">Backups</span>
              <span class="info-value">{config.backup_store_type || 'local'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">Mode</span>
              <span class="info-value">{config.controller && config.worker ? 'Controller + Worker' : config.controller ? 'Controller' : 'Worker'}</span>
            </div>
            <div class="info-item">
              <span class="info-label">Web UI</span>
              <span class="info-value">{config.web_ui ? 'Enabled' : 'Disabled'}</span>
            </div>
          </div>
        </div>

        <div class="panel" style="margin-top:14px;">
          <div class="s-title">Network</div>
          <div class="field">
            <label class="label">Connection Address</label>
            <input class="input input-mono" type="text" placeholder="e.g. play.example.com or 203.0.113.10" bind:value={settings.connection_address}>
            <span class="field-hint">Public IP or hostname players use to connect. Shown in the dashboard.</span>
          </div>

          <div class="s-title" style="margin-top:24px;">Ports</div>
          <div class="field">
            <label class="label">Port Mode</label>
            <select class="select" bind:value={settings.port_mode}>
              <option value="auto">Auto — allocate from range</option>
              <option value="manual">Manual — user specifies ports</option>
            </select>
          </div>
          {#if settings.port_mode === 'auto'}
            <div class="field-grid">
              <div class="field">
                <label class="label">Range Start</label>
                <input class="input input-mono" type="number" bind:value={settings.port_range_start}>
              </div>
              <div class="field">
                <label class="label">Range End</label>
                <input class="input input-mono" type="number" bind:value={settings.port_range_end}>
              </div>
            </div>
          {/if}

          <div class="s-title" style="margin-top:24px;">Retention</div>
          <div class="field-grid">
            <div class="field">
              <label class="label">Max Backups</label>
              <input class="input input-mono" type="number" bind:value={settings.max_backups}>
              <span class="field-hint">Per gameserver. 0 = unlimited.</span>
            </div>
            <div class="field">
              <label class="label">Event Retention</label>
              <div class="input-with-suffix">
                <input class="input input-mono" type="number" bind:value={settings.event_retention_days}>
                <span class="input-suffix">days</span>
              </div>
            </div>
          </div>

          <div class="s-title" style="margin-top:24px;">Resource Requirements</div>
          <span class="field-hint" style="margin-bottom:12px; display:block;">Require operators to set limits when creating gameservers.</span>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.require_memory_limit} onclick={() => settings.require_memory_limit = !settings.require_memory_limit}></button>
            <div class="toggle-info">
              <span class="toggle-label">Require memory limit</span>
            </div>
          </div>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.require_cpu_limit} onclick={() => settings.require_cpu_limit = !settings.require_cpu_limit}></button>
            <div class="toggle-info">
              <span class="toggle-label">Require CPU limit</span>
            </div>
          </div>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.require_storage_limit} onclick={() => settings.require_storage_limit = !settings.require_storage_limit}></button>
            <div class="toggle-info">
              <span class="toggle-label">Require storage limit</span>
            </div>
          </div>

          <div class="s-title" style="margin-top:24px;">Integrations</div>
          <div class="field" style="margin-bottom:12px;">
            <label class="label">Steam Web API Key</label>
            <input class="input input-mono" type="password" placeholder="Not configured" bind:value={settings.steam_api_key} autocomplete="off">
            <span class="field-hint">Enables Steam Workshop mod search for CS2, Garry's Mod, and ARK. <a href="https://steamcommunity.com/dev/apikey" target="_blank" style="color:var(--accent);">Get a key</a></span>
          </div>

          {#if canEdit}
            <div class="save-row">
              <button class="btn-solid" onclick={saveSettings} disabled={saving}>
                {saving ? 'Saving...' : 'Save Changes'}
              </button>
            </div>
          {/if}
        </div>
      </div>

    {:else if activeSection === 'security'}
      <!-- ═══════════ SECURITY ═══════════ -->
      <div class="section" style="animation-delay:0s">
        <div class="section-head">
          <h2>Security</h2>
          <p class="section-desc">Authentication, rate limiting, and proxy settings.</p>
        </div>

        <div class="panel">
          <div class="s-title">Authentication</div>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.auth_enabled} onclick={() => settings.auth_enabled = !settings.auth_enabled}></button>
            <div class="toggle-info">
              <span class="toggle-label">Enable authentication</span>
              <span class="toggle-desc">Require API tokens for all requests.</span>
            </div>
          </div>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.localhost_bypass} onclick={() => settings.localhost_bypass = !settings.localhost_bypass}></button>
            <div class="toggle-info">
              <span class="toggle-label">Localhost bypass</span>
              <span class="toggle-desc">Allow unauthenticated access from 127.0.0.1.</span>
            </div>
          </div>

          <div class="s-title" style="margin-top:24px;">Rate Limiting</div>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.rate_limit_enabled} onclick={() => settings.rate_limit_enabled = !settings.rate_limit_enabled}></button>
            <div class="toggle-info">
              <span class="toggle-label">Enable rate limiting</span>
            </div>
          </div>
          {#if settings.rate_limit_enabled}
            <div class="field-grid" style="margin-top:12px;">
              <div class="field">
                <label class="label">Per IP</label>
                <div class="input-with-suffix">
                  <input class="input input-mono" type="number" bind:value={settings.rate_limit_per_ip}>
                  <span class="input-suffix">req/s</span>
                </div>
              </div>
              <div class="field">
                <label class="label">Per Token</label>
                <div class="input-with-suffix">
                  <input class="input input-mono" type="number" bind:value={settings.rate_limit_per_token}>
                  <span class="input-suffix">req/s</span>
                </div>
              </div>
              <div class="field">
                <label class="label">Login</label>
                <div class="input-with-suffix">
                  <input class="input input-mono" type="number" bind:value={settings.rate_limit_login}>
                  <span class="input-suffix">req/s</span>
                </div>
              </div>
            </div>
          {/if}

          <div class="s-title" style="margin-top:24px;">Proxy</div>
          <div class="toggle-field">
            <button class="toggle" class:on={settings.trust_proxy_headers} onclick={() => settings.trust_proxy_headers = !settings.trust_proxy_headers}></button>
            <div class="toggle-info">
              <span class="toggle-label">Trust proxy headers</span>
              <span class="toggle-desc">Use X-Forwarded-For for IP detection. Enable only behind a reverse proxy.</span>
            </div>
          </div>

          {#if canEdit}
            <div class="save-row">
              <button class="btn-solid" onclick={saveSettings} disabled={saving}>
                {saving ? 'Saving...' : 'Save Changes'}
              </button>
            </div>
          {/if}
        </div>
      </div>

    {:else if activeSection === 'tokens'}
      <!-- ═══════════ TOKENS ═══════════ -->
      <div class="section" style="animation-delay:0s">
        <div class="section-head">
          <h2>API Tokens</h2>
          <p class="section-desc">Manage access tokens for the API.</p>
          <button class="btn-accent" onclick={() => showCreateToken = true} style="margin-top:8px;">
            <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
            Create Token
          </button>
        </div>

        {#if revealedToken}
          <div class="reveal-banner">
            <div class="reveal-title">Token Created — copy now, shown only once</div>
            <div class="reveal-value">{revealedToken}</div>
            <div style="display:flex; gap:8px; margin-top:8px;">
              <button class="btn-accent" onclick={() => { navigator.clipboard.writeText(revealedToken); toast('Copied', 'success'); }}>Copy Token</button>
              <button class="btn-solid" onclick={() => { setToken(revealedToken); window.location.reload(); }}>Login as this token</button>
              <button class="btn-accent" onclick={() => revealedToken = ''}>Dismiss</button>
            </div>
          </div>
        {/if}

        {#if showCreateToken}
          <div class="panel" style="margin-bottom:14px;">
            <div class="s-title">New Token</div>
            <div class="field-grid">
              <div class="field">
                <label class="label">Name</label>
                <input class="input" type="text" placeholder="e.g. my-friend" bind:value={newTokenName}>
              </div>
              <div class="field">
                <label class="label">Role</label>
                <select class="select" bind:value={newTokenRole}>
                  <option value="admin">Admin (full cluster access)</option>
                  <option value="user">User (per-server access via grants)</option>
                </select>
              </div>
            </div>
            {#if newTokenRole === 'user'}
              <div class="field-hint" style="margin: 4px 0 10px;">
                User tokens see only servers they create or are granted access to.
              </div>
              <div class="toggle-row" style="margin: 10px 0;">
                <button class="toggle" class:on={newTokenCanCreate} onclick={() => newTokenCanCreate = !newTokenCanCreate}></button>
                <span class="toggle-label">Can create gameservers</span>
              </div>
              {#if newTokenCanCreate}
                <div class="s-title" style="font-size:0.78rem; margin-top:8px;">Resource Quotas</div>
                <div class="field-hint" style="margin: 2px 0 8px;">Leave empty for unlimited.</div>
                <div class="field-grid" style="grid-template-columns: 1fr 1fr;">
                  <div class="field">
                    <label class="label">Max Gameservers</label>
                    <input class="input" type="number" min="1" placeholder="Unlimited" bind:value={newTokenMaxGs}>
                  </div>
                  <div class="field">
                    <label class="label">Max Memory (MB)</label>
                    <input class="input" type="number" min="1" placeholder="Unlimited" bind:value={newTokenMaxMem}>
                  </div>
                  <div class="field">
                    <label class="label">Max CPU</label>
                    <input class="input" type="number" min="0.5" step="0.5" placeholder="Unlimited" bind:value={newTokenMaxCpu}>
                  </div>
                  <div class="field">
                    <label class="label">Max Storage (MB)</label>
                    <input class="input" type="number" min="1" placeholder="Unlimited" bind:value={newTokenMaxStorage}>
                  </div>
                </div>
              {/if}
            {/if}
            <div class="panel-actions">
              <button class="btn-solid" onclick={createToken} disabled={creatingToken || !newTokenName} style="font-size:0.82rem;">
                {creatingToken ? 'Creating...' : 'Create'}
              </button>
              <button class="btn-accent" onclick={() => showCreateToken = false} style="font-size:0.82rem;">Cancel</button>
            </div>
          </div>
        {/if}

        <div class="panel">
          {#if tokens.length === 0}
            <div class="empty-row">No API tokens yet.</div>
          {:else}
            {#each tokens as token (token.id)}
              <div class="list-row">
                <div class="list-info">
                  <div class="list-name">{token.name}</div>
                  <div class="list-meta">
                    <span class="scope-badge">{token.role}</span>
                    {#if token.role === 'user'}
                      <span class="meta-sep">·</span>
                      {#if token.can_create}
                        <span class="quota-tag">can create</span>
                        {#if token.max_gameservers}<span class="quota-tag">{token.max_gameservers} servers</span>{/if}
                        {#if token.max_memory_mb}<span class="quota-tag">{token.max_memory_mb} MB</span>{/if}
                        {#if token.max_cpu}<span class="quota-tag">{token.max_cpu} CPU</span>{/if}
                      {:else}
                        <span class="quota-tag dim">access only</span>
                      {/if}
                    {/if}
                    <span class="meta-sep">·</span>
                    Created {shortDate(token.created_at)}
                    {#if token.last_used_at}
                      <span class="meta-sep">·</span>
                      Last used {shortDate(token.last_used_at)}
                    {/if}
                  </div>
                </div>
                <div class="list-actions">
                  {#if token.role === 'user'}
                    <button class="act" onclick={() => shareToken(token.id)}>
                      {token.claim_code ? 'Copy Link' : 'Share'}
                    </button>
                  {/if}
                  <button class="act danger" onclick={() => deleteToken(token.id, token.name)}>Delete</button>
                </div>
              </div>
            {/each}
          {/if}
        </div>
      </div>

    {:else if activeSection === 'webhooks'}
      <!-- ═══════════ WEBHOOKS ═══════════ -->
      <div class="section" style="animation-delay:0s">
        <div class="section-head">
          <h2>Webhooks</h2>
          <p class="section-desc">Push events to external services.</p>
          <button class="btn-accent" onclick={() => showCreateWebhook = true} style="margin-top:8px;">
            <svg viewBox="0 0 16 16" fill="currentColor" width="13" height="13"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
            Add Endpoint
          </button>
        </div>

        {#if showCreateWebhook}
          <div class="panel" style="margin-bottom:14px;">
            <div class="s-title">New Webhook</div>
            <div class="field">
              <label class="label">URL</label>
              <input class="input input-mono" type="url" placeholder="https://example.com/webhook" bind:value={newWebhookUrl}>
            </div>
            <div class="field">
              <label class="label">Description</label>
              <input class="input" type="text" placeholder="Optional description" bind:value={newWebhookDesc}>
            </div>
            <div class="panel-actions">
              <button class="btn-solid" onclick={createWebhook} disabled={creatingWebhook || !newWebhookUrl} style="font-size:0.82rem;">
                {creatingWebhook ? 'Creating...' : 'Create'}
              </button>
              <button class="btn-accent" onclick={() => showCreateWebhook = false} style="font-size:0.82rem;">Cancel</button>
            </div>
          </div>
        {/if}

        <div class="panel">
          {#if webhooks.length === 0}
            <div class="empty-row">No webhook endpoints configured.</div>
          {:else}
            {#each webhooks as wh (wh.id)}
              <div class="list-row">
                <div class="list-info">
                  <div class="list-name mono">{wh.url}</div>
                  <div class="list-meta">
                    {wh.description || 'No description'}
                    <span class="meta-sep">·</span>
                    {wh.events.length} event{wh.events.length !== 1 ? 's' : ''}
                  </div>
                </div>
                <div class="list-actions">
                  <button class="act" onclick={() => testWebhook(wh)}>Test</button>
                  <button class="act danger" onclick={() => deleteWebhook(wh)}>Delete</button>
                  <button class="toggle" class:on={wh.enabled} onclick={() => toggleWebhook(wh)}></button>
                </div>
              </div>
            {/each}
          {/if}
        </div>
      </div>

    {:else if activeSection === 'workers'}
      <!-- ═══════════ WORKERS ═══════════ -->
      <div class="section" style="animation-delay:0s">
        <div class="section-head">
          <h2>Workers</h2>
          <p class="section-desc">Connected worker nodes and resource allocation.</p>
        </div>

        <div class="panel">
          {#if workers.length === 0}
            <div class="empty-row">No workers connected. Running in standalone mode.</div>
          {:else}
            {#each workers as w (w.id)}
              <div class="list-row">
                <div class="list-info">
                  <div class="list-name mono">{w.id.slice(0, 8)}</div>
                  <div class="list-meta">
                    {w.lan_ip}
                    <span class="meta-sep">·</span>
                    {w.gameserver_count} server{w.gameserver_count !== 1 ? 's' : ''}
                    <span class="meta-sep">·</span>
                    {w.allocated_memory_mb} / {w.memory_total_mb} MB
                    <span class="meta-sep">·</span>
                    <span class:live={w.status === 'online'} class:idle={w.status !== 'online'}>{w.status}</span>
                  </div>
                </div>
                <div class="list-actions">
                  {#if w.cordoned}
                    <span class="scope-badge caution">cordoned</span>
                  {/if}
                </div>
              </div>
            {/each}
          {/if}
        </div>
      </div>

    {:else if activeSection === 'events'}
      <!-- ═══════════ EVENTS ═══════════ -->
      <div class="section" style="animation-delay:0s">
        <div class="section-head">
          <h2>Event History</h2>
          <p class="section-desc">Audit log of all system events.</p>
        </div>

        <div class="event-filters">
          <input class="input input-mono" type="text" placeholder="Filter by type (e.g. gameserver.*)" bind:value={eventFilter} style="max-width:260px;">
          <input class="input input-mono" type="text" placeholder="Gameserver ID" bind:value={eventGsFilter} style="max-width:220px;">
          <button class="btn-accent" onclick={() => { eventsPage = 0; loadEvents(); }} style="font-size:0.78rem;">Filter</button>
        </div>

        <div class="panel">
          {#if events.length === 0}
            <div class="empty-row">No events found.</div>
          {:else}
            {#each events as event (event.id)}
              <div class="event-row">
                <div class="event-type">{event.type}</div>
                <div class="event-meta">
                  {#if event.gameserver_id}
                    <span class="event-gs">{event.gameserver_id.slice(0, 8)}</span>
                  {/if}
                  <span class="event-time">{shortDate(event.created_at)}</span>
                </div>
              </div>
            {/each}
          {/if}
        </div>

        {#if events.length > 0}
          <div class="pagination">
            <button class="btn-accent" disabled={eventsPage === 0} onclick={() => { eventsPage--; loadEvents(); }} style="font-size:0.78rem;">Previous</button>
            <span class="page-info">Page {eventsPage + 1}</span>
            <button class="btn-accent" disabled={!eventsHasMore} onclick={() => { eventsPage++; loadEvents(); }} style="font-size:0.78rem;">Next</button>
          </div>
        {/if}
      </div>
    {/if}
  </div>
</main>

<style>
  /* ═══════════ LAYOUT ═══════════ */
  .settings-layout {
    display: flex;
    max-width: 1000px;
    margin: 0 auto;
    padding: 28px 24px 60px;
    gap: 0;
  }

  /* ═══════════ SIDEBAR ═══════════ */
  .sidebar {
    position: sticky; top: 82px;
    width: 190px; flex-shrink: 0;
    height: fit-content;
    padding-right: 24px;
    border-right: 1px solid var(--border-dim);
  }
  .sidebar-title {
    font-size: 1.15rem; font-weight: 600;
    letter-spacing: -0.02em;
    margin-bottom: 18px;
    padding-left: 10px;
  }
  .sidebar-link {
    display: flex; align-items: center; gap: 9px;
    width: 100%;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: none; border: none;
    font-family: var(--font-body);
    font-size: 0.82rem; font-weight: 450;
    color: var(--text-tertiary);
    cursor: pointer;
    transition: color 0.15s, background 0.15s;
    text-align: left;
  }
  .sidebar-link:hover { color: var(--text-secondary); background: var(--bg-hover); }
  .sidebar-link.active {
    color: var(--accent);
    background: var(--accent-subtle);
  }
  .sidebar-link svg { width: 15px; height: 15px; flex-shrink: 0; opacity: 0.5; }
  .sidebar-link.active svg { opacity: 1; }

  /* ═══════════ CONTENT ═══════════ */
  .content {
    flex: 1; min-width: 0;
    padding-left: 28px;
    max-width: 700px;
  }

  .section {
    animation: section-in 0.35s cubic-bezier(0.16, 1, 0.3, 1) both;
  }
  @keyframes section-in {
    from { opacity: 0; transform: translateY(6px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .section-head {
    margin-bottom: 18px;
  }
  .section-head h2 {
    font-size: 1.1rem; font-weight: 600;
    letter-spacing: -0.02em;
  }
  .section-desc {
    font-size: 0.82rem; color: var(--text-tertiary);
    margin-top: 4px;
  }

  .loading-text {
    color: var(--text-tertiary); font-size: 0.85rem;
    padding: 40px 0; text-align: center;
  }

  /* ═══════════ PANELS ═══════════ */
  .panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    padding: 22px;
    position: relative;
    overflow: hidden;
  }
  .panel::before {
    content: ''; position: absolute; inset: 0;
    background: radial-gradient(ellipse 90% 40% at 50% 0%, rgba(232,114,42,0.012) 0%, transparent 50%);
    pointer-events: none;
  }

  .s-title {
    font-size: 0.82rem; font-weight: 550;
    color: var(--text-secondary);
    margin-bottom: 14px;
    padding-bottom: 8px;
    border-bottom: 1px solid var(--border-dim);
    position: relative; z-index: 1;
  }

  /* ═══════════ INFO PANEL ═══════════ */
  .info-panel { padding: 18px 22px; }
  .info-grid {
    display: grid; grid-template-columns: 1fr 1fr;
    gap: 0; position: relative; z-index: 1;
  }
  .info-item {
    display: flex; justify-content: space-between; align-items: center;
    padding: 7px 0;
    border-bottom: 1px solid var(--border-dim);
  }
  .info-item:nth-last-child(-n+2) { border-bottom: none; }
  .info-item:nth-child(odd) { padding-right: 16px; border-right: 1px solid var(--border-dim); }
  .info-item:nth-child(even) { padding-left: 16px; }
  .info-label {
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .info-value {
    font-size: 0.78rem; font-family: var(--font-mono);
    color: var(--text-secondary);
  }

  /* ═══════════ FORM FIELDS ═══════════ */
  .field { margin-bottom: 16px; position: relative; z-index: 1; }
  .field:last-child { margin-bottom: 0; }
  .field-grid {
    display: grid; grid-template-columns: 1fr 1fr;
    gap: 14px; position: relative; z-index: 1;
    margin-bottom: 16px;
  }
  .field-grid .field { margin-bottom: 0; }
  .field-hint {
    font-size: 0.68rem; color: var(--text-tertiary);
    opacity: 0.7; margin-top: 4px;
  }
  .input-with-suffix { position: relative; }
  .input-with-suffix .input { padding-right: 50px; }
  .input-suffix {
    position: absolute; right: 14px; top: 50%; transform: translateY(-50%);
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); pointer-events: none;
  }

  /* Toggle rows */
  .toggle-field {
    display: flex; align-items: flex-start; gap: 12px;
    padding: 10px 0;
    position: relative; z-index: 1;
  }
  .toggle-field + .toggle-field { border-top: 1px solid var(--border-dim); }
  .toggle-info { display: flex; flex-direction: column; gap: 2px; }
  .toggle-label { font-size: 0.84rem; font-weight: 450; color: var(--text-primary); }
  .toggle-desc { font-size: 0.72rem; color: var(--text-tertiary); }

  .save-row {
    display: flex; justify-content: flex-end;
    margin-top: 22px; padding-top: 16px;
    border-top: 1px solid var(--border-dim);
    position: relative; z-index: 1;
  }

  .panel-actions {
    display: flex; gap: 8px;
    margin-top: 14px;
    position: relative; z-index: 1;
  }

  /* ═══════════ LIST ROWS ═══════════ */
  .list-row {
    display: flex; align-items: center;
    padding: 12px 0; gap: 14px;
    position: relative; z-index: 1;
  }
  .list-row + .list-row { border-top: 1px solid var(--border-dim); }
  .list-info { flex: 1; min-width: 0; }
  .list-name { font-size: 0.86rem; font-weight: 500; }
  .list-name.mono { font-family: var(--font-mono); font-size: 0.8rem; }
  .list-meta {
    font-size: 0.72rem; font-family: var(--font-mono);
    color: var(--text-tertiary); margin-top: 3px;
    display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  }
  .meta-sep { opacity: 0.3; }
  .list-actions {
    display: flex; align-items: center; gap: 6px;
    flex-shrink: 0;
  }

  .act {
    padding: 4px 10px; border-radius: 4px;
    font-size: 0.7rem; font-family: var(--font-mono);
    color: var(--text-tertiary); background: none; border: none;
    cursor: pointer; transition: color 0.15s, background 0.15s;
  }
  .act:hover { color: var(--accent); background: var(--accent-subtle); }
  .act.danger:hover { color: var(--danger); background: rgba(239,68,68,0.06); }

  .scope-badge {
    display: inline-flex; padding: 2px 7px; border-radius: 4px;
    font-size: 0.62rem; font-family: var(--font-mono); font-weight: 500;
    text-transform: uppercase; letter-spacing: 0.04em;
    background: var(--accent-dim); color: var(--accent);
  }
  .scope-badge.caution { background: rgba(245,158,11,0.08); color: var(--caution); }
  .quota-tag {
    font-size: 0.7rem; font-family: var(--font-mono); color: var(--muted);
  }
  .quota-tag.dim { opacity: 0.6; font-style: italic; }

  .empty-row {
    padding: 20px 0;
    font-size: 0.82rem; color: var(--text-tertiary);
    text-align: center;
    position: relative; z-index: 1;
  }

  /* ═══════════ REVEAL BANNER (token) ═══════════ */
  .reveal-banner {
    background: var(--bg-elevated);
    border: 1px solid var(--accent-border);
    border-radius: var(--radius);
    padding: 16px 20px;
    margin-bottom: 14px;
    animation: section-in 0.25s ease-out;
  }
  .reveal-title {
    font-size: 0.78rem; font-weight: 500; color: var(--accent);
    margin-bottom: 8px;
  }
  .reveal-value {
    font-family: var(--font-mono); font-size: 0.82rem;
    color: var(--text-primary);
    padding: 8px 12px;
    background: var(--bg-inset);
    border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm);
    word-break: break-all;
  }

  /* ═══════════ EVENTS ═══════════ */
  .event-filters {
    display: flex; gap: 8px; margin-bottom: 14px;
    align-items: center; flex-wrap: wrap;
  }

  .event-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 0; gap: 12px;
    position: relative; z-index: 1;
  }
  .event-row + .event-row { border-top: 1px solid var(--border-dim); }
  .event-type {
    font-family: var(--font-mono); font-size: 0.78rem;
    color: var(--text-secondary);
  }
  .event-meta {
    display: flex; align-items: center; gap: 10px;
    font-size: 0.68rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }
  .event-gs {
    padding: 1px 6px; border-radius: 3px;
    background: var(--bg-elevated); border: 1px solid var(--border-dim);
  }

  .pagination {
    display: flex; align-items: center; justify-content: center;
    gap: 12px; margin-top: 14px;
  }
  .page-info {
    font-size: 0.74rem; font-family: var(--font-mono);
    color: var(--text-tertiary);
  }

  /* Status text helpers */
  .live { color: var(--live); }
  .idle { color: var(--idle); }

  /* ═══════════ MOBILE ═══════════ */
  @media (max-width: 720px) {
    .settings-layout {
      flex-direction: column;
      gap: 0;
    }
    .sidebar {
      position: static;
      width: 100%;
      border-right: none;
      border-bottom: 1px solid var(--border-dim);
      padding-right: 0;
      padding-bottom: 14px;
      margin-bottom: 20px;
    }
    .content { padding-left: 0; max-width: 100%; }
    .field-grid { grid-template-columns: 1fr; }
  }
</style>
