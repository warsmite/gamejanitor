// History-based router for plain Svelte SPA.
// Reads window.location.pathname, strips basePath if present,
// matches against known route patterns, and exposes reactive state.
//
// In embedded mode (basePath set), routes are flat: /, /console, /backups, etc.
// The gameserver ID is resolved from the token scope, not the URL.

import { basePath, embedded } from './base';

interface Route {
  name: string;
  path: string;
  params: Record<string, string>;
}

const standardPatterns: { name: string; pattern: RegExp; paramNames: string[] }[] = [
  { name: 'dashboard', pattern: /^\/$/, paramNames: [] },
  { name: 'cluster', pattern: /^\/cluster$/, paramNames: [] },
  { name: 'settings', pattern: /^\/settings$/, paramNames: [] },
  { name: 'newGameserver', pattern: /^\/gameservers\/new$/, paramNames: [] },
  { name: 'newGameserver', pattern: /^\/gameservers\/new\/([^/]+)$/, paramNames: ['game'] },
  { name: 'gameserverConsole', pattern: /^\/gameservers\/([^/]+)\/console$/, paramNames: ['id'] },
  { name: 'gameserverFiles', pattern: /^\/gameservers\/([^/]+)\/files$/, paramNames: ['id'] },
  { name: 'gameserverBackups', pattern: /^\/gameservers\/([^/]+)\/backups$/, paramNames: ['id'] },
  { name: 'gameserverSchedules', pattern: /^\/gameservers\/([^/]+)\/schedules$/, paramNames: ['id'] },
  { name: 'gameserverMods', pattern: /^\/gameservers\/([^/]+)\/mods$/, paramNames: ['id'] },
  { name: 'gameserverSettings', pattern: /^\/gameservers\/([^/]+)\/settings$/, paramNames: ['id'] },
  { name: 'gameserverOverview', pattern: /^\/gameservers\/([^/]+)$/, paramNames: ['id'] },
];

// Embedded mode: no gameserver ID in URL — resolved from token scope
const embeddedPatterns: { name: string; pattern: RegExp; paramNames: string[] }[] = [
  { name: 'gameserverConsole', pattern: /^\/console$/, paramNames: [] },
  { name: 'gameserverFiles', pattern: /^\/files$/, paramNames: [] },
  { name: 'gameserverBackups', pattern: /^\/backups$/, paramNames: [] },
  { name: 'gameserverSchedules', pattern: /^\/schedules$/, paramNames: [] },
  { name: 'gameserverMods', pattern: /^\/mods$/, paramNames: [] },
  { name: 'gameserverSettings', pattern: /^\/settings$/, paramNames: [] },
  { name: 'gameserverOverview', pattern: /^\/$/, paramNames: [] },
];

const patterns = embedded ? embeddedPatterns : standardPatterns;

function parsePath(): Route {
  let path = window.location.pathname;
  if (basePath && path.startsWith(basePath)) {
    path = path.slice(basePath.length) || '/';
  }

  for (const { name, pattern, paramNames } of patterns) {
    const match = path.match(pattern);
    if (match) {
      const params: Record<string, string> = {};
      for (let i = 0; i < paramNames.length; i++) {
        params[paramNames[i]] = match[i + 1];
      }
      return { name, path, params };
    }
  }
  return { name: embedded ? 'gameserverOverview' : 'dashboard', path: '/', params: {} };
}

let route = $state<Route>(parsePath());

function handlePopState() {
  route = parsePath();
}

if (typeof window !== 'undefined') {
  window.addEventListener('popstate', handlePopState);
}

export function navigate(path: string) {
  window.history.pushState(null, '', basePath + path);
  route = parsePath();
}

export function getRoute(): Route {
  return route;
}

export function isActive(path: string): boolean {
  const current = route.path;
  if (path === current) return true;
  if (path !== '/' && current.startsWith(path + '/')) return true;
  return false;
}
