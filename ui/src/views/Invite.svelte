<script lang="ts">
  import { onMount } from 'svelte';
  import { basePath } from '$lib/base';
  import { setToken } from '$lib/stores/auth';
  import { navigate } from '$lib/router';

  let { code }: { code: string } = $props();

  let status = $state<'loading' | 'success' | 'error'>('loading');
  let errorMessage = $state('');

  onMount(async () => {
    try {
      const resp = await fetch(basePath + '/api/claim', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code }),
      });

      const json = await resp.json();

      if (!resp.ok || json.status === 'error') {
        status = 'error';
        errorMessage = json.error || 'Invalid or expired invite link';
        return;
      }

      // Set the token cookie and redirect to dashboard
      setToken(json.data.token);
      status = 'success';

      setTimeout(() => navigate('/'), 1000);
    } catch (e) {
      status = 'error';
      errorMessage = 'Failed to connect to server';
    }
  });
</script>

<div class="invite-page">
  <div class="invite-card">
    {#if status === 'loading'}
      <div class="invite-icon loading">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M12 2v4m0 12v4m-7.07-3.93l2.83-2.83m8.49-8.49l2.83-2.83M2 12h4m12 0h4m-3.93 7.07l-2.83-2.83M6.34 6.34L3.51 3.51"/>
        </svg>
      </div>
      <h2>Accepting invite...</h2>
      <p class="sub">Setting up your access</p>
    {:else if status === 'success'}
      <div class="invite-icon success">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <polyline points="20 6 9 17 4 12"/>
        </svg>
      </div>
      <h2>You're in</h2>
      <p class="sub">Redirecting to dashboard...</p>
    {:else}
      <div class="invite-icon error">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
        </svg>
      </div>
      <h2>Invite failed</h2>
      <p class="sub">{errorMessage}</p>
      <a href="/" class="back-link">Go to login</a>
    {/if}
  </div>
</div>

<style>
  .invite-page {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .invite-card {
    text-align: center;
    padding: 48px;
    max-width: 360px;
  }

  .invite-icon {
    width: 48px;
    height: 48px;
    margin: 0 auto 20px;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 12px;
  }
  .invite-icon svg {
    width: 24px;
    height: 24px;
  }

  .invite-icon.loading {
    color: var(--accent);
    background: var(--accent-dim);
    animation: spin 1.5s linear infinite;
  }
  .invite-icon.success {
    color: var(--live);
    background: var(--live-dim);
  }
  .invite-icon.error {
    color: var(--danger);
    background: rgba(239, 68, 68, 0.1);
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  h2 {
    font-size: 1.2rem;
    font-weight: 600;
    margin-bottom: 6px;
  }

  .sub {
    font-size: 0.82rem;
    color: var(--text-tertiary);
  }

  .back-link {
    display: inline-block;
    margin-top: 20px;
    font-size: 0.78rem;
    color: var(--accent);
    text-decoration: none;
    font-family: var(--font-mono);
  }
  .back-link:hover {
    text-decoration: underline;
  }
</style>
