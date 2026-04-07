<script lang="ts">
  import './styles/tokens.css';
  import { onMount, onDestroy } from 'svelte';
  import { ToastContainer, ConfirmModal } from '$lib/components';
  import { connect, disconnect, enableAutoToasts, initAuth, clearToken, isAdmin, gameserverStore } from '$lib/stores';
  import { api } from '$lib/api';
  import { getRoute, navigate } from '$lib/router';
  import { embedded, basePath } from '$lib/base';

  import Dashboard from './views/Dashboard.svelte';
  import Cluster from './views/Cluster.svelte';
  import Settings from './views/Settings.svelte';
  import NewGameserver from './views/NewGameserver.svelte';
  import Login from './views/Login.svelte';
  import Invite from './views/Invite.svelte';
  import GameserverLayout from './views/gameserver/Layout.svelte';
  import Overview from './views/gameserver/Overview.svelte';
  import Console from './views/gameserver/Console.svelte';
  import Files from './views/gameserver/Files.svelte';
  import Backups from './views/gameserver/Backups.svelte';
  import Schedules from './views/gameserver/Schedules.svelte';
  import Mods from './views/gameserver/Mods.svelte';
  import GameserverSettings from './views/gameserver/GameserverSettings.svelte';

  const route = $derived(getRoute());

  // In embedded mode, the gameserver ID comes from the store (single scoped server), not the URL
  const embeddedId = $derived(embedded ? gameserverStore.list[0]?.id || '' : '');

  // Is this a gameserver sub-route?
  const isGameserverRoute = $derived(
    embedded
      ? route.name.startsWith('gameserver')
      : route.name.startsWith('gameserver') && route.name !== 'newGameserver' && !!route.params.id
  );
  const gameserverId = $derived(embedded ? embeddedId : route.params.id || '');

  let multiNode = $state(false);
  const hasToken = $derived(!!document.cookie.match(/(?:^|; )_token=/));

  function logout() {
    clearToken();
    window.location.reload();
  }

  onMount(() => {
    initAuth();
    connect();
    enableAutoToasts();
    gameserverStore.init();

    // Check if multi-node for cluster nav
    if (!embedded) {
      api.cluster.workers().then(w => { multiNode = w.length > 1; }).catch(() => {});
    }

    // Intercept internal link clicks for client-side navigation
    document.addEventListener('click', handleLinkClick);
  });

  function handleLinkClick(e: MouseEvent) {
    const anchor = (e.target as Element)?.closest('a');
    if (!anchor) return;

    const href = anchor.getAttribute('href');
    if (!href || href.startsWith('http') || href.startsWith('//')) return;
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    if (anchor.target === '_blank') return;

    // Internal link — use pushState
    e.preventDefault();
    navigate(href);
  }

  onDestroy(() => {
    document.removeEventListener('click', handleLinkClick);
    gameserverStore.destroy();
    disconnect();
  });
</script>

{#if !embedded && !gameserverStore.authRequired}
  <nav>
    <div class="n-left">
      <a href="/" class="brand">Game<span class="brand-accent">Janitor</span></a>
      <div class="n-links">
        <a href="/">Dashboard</a>
        {#if multiNode && $isAdmin}
          <a href="/cluster">Cluster</a>
        {/if}
        {#if $isAdmin}
          <a href="/settings">Settings</a>
        {/if}
      </div>
    </div>
    <div class="n-right">
      {#if hasToken}
        <div class="auth authenticated"><span class="auth-dot authenticated"></span>Authenticated</div>
        <button class="logout-btn" onclick={logout}>Sign out</button>
      {:else}
        <div class="auth"><span class="auth-dot"></span>Local</div>
      {/if}
    </div>
  </nav>
{/if}

{#if route.name === 'invite'}
  <Invite code={route.params.code} />
{:else if gameserverStore.authRequired}
  <Login />
{:else if isGameserverRoute}
  <GameserverLayout id={gameserverId}>
    {#snippet children()}
      {#if route.name === 'gameserverOverview'}
        <Overview id={gameserverId} />
      {:else if route.name === 'gameserverConsole'}
        <Console id={gameserverId} />
      {:else if route.name === 'gameserverFiles'}
        <Files id={gameserverId} />
      {:else if route.name === 'gameserverBackups'}
        <Backups id={gameserverId} />
      {:else if route.name === 'gameserverSchedules'}
        <Schedules id={gameserverId} />
      {:else if route.name === 'gameserverMods'}
        <Mods id={gameserverId} />
      {:else if route.name === 'gameserverSettings'}
        <GameserverSettings id={gameserverId} />
      {/if}
    {/snippet}
  </GameserverLayout>
{:else if route.name === 'newGameserver'}
  <NewGameserver />
{:else if route.name === 'cluster'}
  <Cluster />
{:else if route.name === 'settings'}
  <Settings />
{:else}
  <Dashboard />
{/if}

<ToastContainer />
<ConfirmModal />
