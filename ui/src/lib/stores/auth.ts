import { writable, derived } from 'svelte/store';

export const token = writable<string | null>(null);
export const role = writable<string>('');

export const isAdmin = derived(role, ($role) => $role === 'admin');

export const isAuthenticated = derived(token, ($token) => $token !== null);

// Load token from cookie on init
export function initAuth() {
  const match = document.cookie.match(/(?:^|; )_token=([^;]*)/);
  if (match) {
    token.set(decodeURIComponent(match[1]));
  }
}

// Store token in cookie
export function setToken(rawToken: string) {
  document.cookie = `_token=${encodeURIComponent(rawToken)}; path=/; max-age=${60 * 60 * 24 * 365}; SameSite=Lax`;
  token.set(rawToken);
}

// Clear token
export function clearToken() {
  document.cookie = '_token=; path=/; max-age=0';
  token.set(null);
  role.set('');
}
