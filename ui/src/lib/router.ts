// Hash-based router for plain Svelte SPA.
// Reads window.location.hash, matches against known route patterns,
// and exposes reactive state via Svelte 5 $state.

interface Route {
  name: string;
  path: string;
  params: Record<string, string>;
}

const patterns: { name: string; pattern: RegExp; paramNames: string[] }[] = [
  { name: 'dashboard', pattern: /^\/$/, paramNames: [] },
  { name: 'settings', pattern: /^\/settings$/, paramNames: [] },
  { name: 'newGameserver', pattern: /^\/gameservers\/new$/, paramNames: [] },
  { name: 'gameserverConsole', pattern: /^\/gameservers\/([^/]+)\/console$/, paramNames: ['id'] },
  { name: 'gameserverFiles', pattern: /^\/gameservers\/([^/]+)\/files$/, paramNames: ['id'] },
  { name: 'gameserverBackups', pattern: /^\/gameservers\/([^/]+)\/backups$/, paramNames: ['id'] },
  { name: 'gameserverSchedules', pattern: /^\/gameservers\/([^/]+)\/schedules$/, paramNames: ['id'] },
  { name: 'gameserverSettings', pattern: /^\/gameservers\/([^/]+)\/settings$/, paramNames: ['id'] },
  { name: 'gameserverMods', pattern: /^\/gameservers\/([^/]+)\/mods$/, paramNames: ['id'] },
  { name: 'gameserverOverview', pattern: /^\/gameservers\/([^/]+)$/, paramNames: ['id'] },
];

function parseHash(): Route {
  const hash = window.location.hash.replace(/^#/, '') || '/';
  for (const { name, pattern, paramNames } of patterns) {
    const match = hash.match(pattern);
    if (match) {
      const params: Record<string, string> = {};
      for (let i = 0; i < paramNames.length; i++) {
        params[paramNames[i]] = match[i + 1];
      }
      return { name, path: hash, params };
    }
  }
  return { name: 'dashboard', path: '/', params: {} };
}

let route = $state<Route>(parseHash());

function handleHashChange() {
  route = parseHash();
}

if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', handleHashChange);
}

export function navigate(path: string) {
  window.location.hash = path;
}

export function getRoute(): Route {
  return route;
}

// Check if a given hash path is active (exact or prefix match)
export function isActive(hashPath: string): boolean {
  const current = route.path;
  if (hashPath === current) return true;
  if (hashPath !== '/' && current.startsWith(hashPath + '/')) return true;
  return false;
}
