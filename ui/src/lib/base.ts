// Detect if running under hosting proxy at /panel/{serverId}/
// If so, API calls need this prefix. Hash routing is unaffected.
function detectBasePath(): string {
  const match = window.location.pathname.match(/^(\/panel\/[^/]+)/);
  return match ? match[1] : '';
}

export const basePath = detectBasePath();
export const embedded = basePath !== '';
