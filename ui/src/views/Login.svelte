<script lang="ts">
  import { setToken, clearToken } from '$lib/stores';
  import { embedded } from '$lib/base';
  import { basePath } from '$lib/base';

  let tokenInput = $state('');
  let error = $state('');
  let submitting = $state(false);

  async function handleSubmit() {
    const raw = tokenInput.trim();
    if (!raw) {
      error = 'Please enter a token';
      return;
    }

    submitting = true;
    error = '';

    try {
      // Validate token before saving — don't set cookie until we know it works
      const resp = await fetch(basePath + '/api/gameservers', {
        headers: { 'Authorization': `Bearer ${raw}` },
      });
      if (resp.status === 401) {
        error = 'Invalid or expired token';
        submitting = false;
        return;
      }
      // Token works — save to cookie and reload
      setToken(raw);
      window.location.reload();
    } catch {
      error = 'Could not reach the server';
      submitting = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') handleSubmit();
  }
</script>

{#if embedded}
  <div class="session-expired">
    <div class="expired-icon">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>
      </svg>
    </div>
    <h2>Session Expired</h2>
    <p>Your session has expired. Return to the dashboard to continue.</p>
    <a href="{window.location.origin}" class="btn-solid">Return to Dashboard</a>
  </div>
{:else}
  <div class="login-wrap">
    <div class="login-card">
      <div class="login-brand">
        <span class="brand">Game<span class="brand-accent">Janitor</span></span>
      </div>

      <h1>Sign in</h1>
      <p class="login-desc">Paste your API token to access the panel.</p>

      <div class="form-group">
        <input
          class="token-input"
          type="password"
          placeholder="gj_..."
          bind:value={tokenInput}
          onkeydown={handleKeydown}
          disabled={submitting}
          autocomplete="off"
          spellcheck="false"
        >
      </div>

      {#if error}
        <div class="login-error">{error}</div>
      {/if}

      <button class="btn-solid login-btn" onclick={handleSubmit} disabled={submitting || !tokenInput.trim()}>
        {submitting ? 'Verifying...' : 'Sign in'}
      </button>

      <div class="login-hint">
        <p>Locked out? Create a token directly via the database:</p>
        <code>gamejanitor tokens offline create --name admin --type admin</code>
      </div>
    </div>
  </div>
{/if}

<style>
  .login-wrap {
    display: grid; place-items: center;
    min-height: 100vh; padding: 24px;
    background: var(--bg-base, #09090c);
  }
  .login-card {
    width: 380px; max-width: 100%;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    padding: 36px 32px;
    animation: fade-up 0.5s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(12px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .login-brand {
    margin-bottom: 28px;
  }

  h1 { font-size: 1.2rem; font-weight: 600; margin-bottom: 6px; }
  .login-desc { font-size: 0.82rem; color: var(--text-tertiary); margin-bottom: 22px; }

  .form-group { margin-bottom: 14px; }

  .token-input {
    width: 100%; padding: 11px 14px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm); color: var(--text-primary);
    font-family: var(--font-mono); font-size: 0.85rem; outline: none;
    transition: border-color 0.2s;
  }
  .token-input:focus { border-color: var(--accent-border); }
  .token-input::placeholder { color: var(--text-tertiary); opacity: 0.4; }
  .token-input:disabled { opacity: 0.5; }

  .login-error {
    font-size: 0.78rem; color: var(--danger);
    margin-bottom: 14px; padding: 8px 12px;
    background: rgba(239,68,68,0.06); border: 1px solid rgba(239,68,68,0.15);
    border-radius: var(--radius-sm);
  }

  .login-btn {
    width: 100%; padding: 11px; font-size: 0.86rem;
    border-radius: var(--radius-sm); cursor: pointer;
    background: var(--accent); color: #fff; border: none;
    font-weight: 520; transition: opacity 0.2s;
  }
  .login-btn:hover { opacity: 0.9; }
  .login-btn:disabled { opacity: 0.4; cursor: not-allowed; }

  .login-hint {
    margin-top: 24px; padding-top: 20px;
    border-top: 1px solid var(--border-dim);
    font-size: 0.72rem; color: var(--text-tertiary);
  }
  .login-hint p { margin-bottom: 8px; }
  .login-hint code {
    display: block; padding: 8px 12px;
    background: var(--bg-inset); border: 1px solid var(--border-dim);
    border-radius: var(--radius-sm); font-family: var(--font-mono);
    font-size: 0.72rem; color: var(--accent); word-break: break-all;
  }

  /* Embedded session expired */
  .session-expired {
    display: flex; flex-direction: column; align-items: center;
    justify-content: center; min-height: 60vh; text-align: center;
    padding: 40px;
  }
  .expired-icon {
    width: 56px; height: 56px; border-radius: 14px;
    background: var(--bg-elevated); border: 1px solid var(--border);
    display: grid; place-items: center; margin-bottom: 20px;
  }
  .expired-icon svg { width: 26px; height: 26px; color: var(--text-tertiary); }
  .session-expired h2 { font-size: 1.15rem; font-weight: 600; margin-bottom: 6px; }
  .session-expired p { font-size: 0.85rem; color: var(--text-tertiary); max-width: 340px; margin-bottom: 20px; }
  .btn-solid {
    padding: 10px 22px; border-radius: var(--radius-sm);
    background: var(--accent); color: #fff; border: none;
    font-size: 0.84rem; font-weight: 520; cursor: pointer;
    text-decoration: none; transition: opacity 0.2s;
  }
  .btn-solid:hover { opacity: 0.9; }
</style>
