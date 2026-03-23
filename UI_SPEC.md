# Web UI Specification

## Overview

Single-page application embedded in the gamejanitor binary. Consumes the API like any other client — no shared code with the service layer. Built with Svelte (SvelteKit), compiled to static assets, served from `ui/dist/` via `embed.go`.

The UI adapts to the user's permissions. An admin sees everything. A scoped token sees only the gameservers and actions they have access to. This naturally supports businesses exposing the UI to customers via reverse proxy — no separate "customer panel" needed, just token scoping.

## Architecture

### File Structure

```
ui/
├── src/
│   ├── app.html                    # shell HTML
│   ├── routes/
│   │   ├── +layout.svelte          # root layout: nav, SSE connection, toast container
│   │   ├── +page.svelte            # dashboard
│   │   ├── gameservers/
│   │   │   ├── new/
│   │   │   │   └── +page.svelte    # create gameserver (game select + configure)
│   │   │   └── [id]/
│   │   │       ├── +layout.svelte  # server header + tab bar (shared across tabs)
│   │   │       ├── +page.svelte    # overview tab
│   │   │       ├── console/+page.svelte
│   │   │       ├── files/+page.svelte
│   │   │       ├── backups/+page.svelte
│   │   │       ├── schedules/+page.svelte
│   │   │       └── settings/+page.svelte
│   │   └── settings/
│   │       └── +page.svelte        # admin settings
│   ├── lib/
│   │   ├── components/             # shared UI components
│   │   ├── stores/                 # Svelte stores (toasts, auth, sse, gameservers)
│   │   └── api.ts                  # typed API client
│   └── styles/
│       └── tokens.css              # design tokens (global CSS variables)
├── dist/                           # built output, embedded in Go binary
├── embed.go                        # Go embed for serving
├── package.json
└── vite.config.js
```

### Key Architectural Decisions

**`[id]/+layout.svelte` loads the gameserver once** and shares it with all child tabs via Svelte context. The header and tab bar render there. Each tab page receives the gameserver data without refetching.

**One SSE connection per session** lives in the root `+layout.svelte`. Events dispatch to Svelte stores. Components subscribe reactively. Status changes update the dashboard. Lifecycle events feed the activity feed. Backup completion triggers toasts.

**API client is one typed file** — `api.ts`. Every API call goes through it: `api.gameservers.list()`, `api.gameservers.start(id)`, `api.backups.create(gsId)`, etc. No raw `fetch` scattered across components.

**Components are presentation-only** — they receive data via props and emit events. Business logic (API calls, state mutations) stays in page components or stores.

### Embedding

```go
package ui

import "embed"

//go:embed dist/*
var Assets embed.FS
```

The Go router serves `ui/dist/` at `/` with SPA fallback (all non-API routes serve `index.html`). The API lives at `/api/*` and is unaffected.

When `web_ui: false` in config, the embed route is not registered. API-only mode.

### Build Process

- **Development**: `cd ui && npm run dev` (Vite dev server with proxy to Go backend on `:8080`)
- **Production**: `cd ui && npm run build` → `ui/dist/` → Go embeds it

## Tech Stack

- **SvelteKit** — file-based routing, layouts, SSR disabled (SPA adapter)
- **Vite** — build tool, dev server with HMR
- **TypeScript** — typed API client, typed stores, typed component props
- **No CSS framework** — design tokens as CSS variables, component-scoped styles via Svelte `<style>`

## Shared Components

Abstract early — the API is complete, the mockups define the patterns. Every component that appears on 2+ pages is extracted from day one.

### Layout

| Component | Used By | Description |
|---|---|---|
| `Nav` | Root layout | Brand, nav links, auth pill |
| `ServerHeader` | All control panel tabs | Game icon, name, status, action buttons, uptime |
| `TabBar` | All control panel tabs | Overview/Console/Files/Backups/Schedules/Settings |
| `Breadcrumb` | Sub-pages | "← Gameservers" back link |
| `PageHeader` | Most pages | Title + optional action button |

### Data Display

| Component | Used By | Description |
|---|---|---|
| `StatusPill` | Dashboard, header, backups | Running/stopped/starting/error with pulse dot |
| `TelemetryCell` | Dashboard hero, overview | Label + large mono value + progress bar |
| `CopyBlock` | Dashboard hero, overview | Label + monospace address + copy button |
| `Panel` | Overview, backups, schedules, files | Surface container with title |
| `PlayerTag` | Overview | Player name pill |
| `ActivityFeed` | Overview | Timestamped event list with colored dots |
| `TypeBadge` | Schedules | restart/backup/command/update colored badges |

### Form Elements

| Component | Used By | Description |
|---|---|---|
| `Input` | Create, settings, schedules | Text/number input with label, optional suffix |
| `Select` | Create, settings, schedules | Styled dropdown |
| `Toggle` | Create, settings, schedules | On/off switch with optional label |
| `Slider` | Create, settings | Range input with orange thumb, filled track, live value display |
| `EnvGroup` | Create, settings | Grouped env vars with left orange border |
| `EulaCallout` | Create | Special required-field callout with notice |

### Actions

| Component | Used By | Description |
|---|---|---|
| `Button` | Everywhere | Variants: `solid` (filled orange), `accent` (outline orange), `action` (stop/restart/start with status colors) |
| `MoreMenu` | Server header | "..." overflow with Update Game, Reinstall |
| `ConfirmDialog` | Settings, backups | Modal for destructive actions (delete, reinstall, restore) |
| `Toast` | Global | Success/error/info notifications for async events |

## Stores

### `sse.ts` — Event Stream

Single SSE connection to `/api/events`. Reconnects with backoff on failure. Dispatches events to subscribers:

```ts
import { writable } from 'svelte/store';

// Components subscribe to filtered events
export function onEvent(type: string, callback: (data: any) => void): () => void;

// Or subscribe to events for a specific gameserver
export function onGameserverEvent(gsId: string, callback: (event: any) => void): () => void;
```

### `toasts.ts` — Notifications

```ts
export const toasts = writable<Toast[]>([]);
export function toast(message: string, type: 'success' | 'error' | 'info' = 'info'): void;
```

SSE events automatically generate toasts for:
- Status changes: "survival-smp is now running"
- Backup completion: "Backup completed (245 MB)"
- Backup failure: "Backup failed: disk full"
- Errors: "survival-smp encountered an error: Port conflict"

### `auth.ts` — Authentication State

```ts
export const token = writable<string | null>(null);     // raw token
export const permissions = writable<string[]>([]);       // decoded permissions
export const isAdmin = derived(permissions, p => ...);
export function hasPermission(perm: string): boolean;
export function hasGameserverAccess(gsId: string): boolean;
```

Loaded on app init from `_token` cookie. Permissions fetched from a `/api/auth/me` endpoint (to be added) or inferred from 403 responses.

### `gameservers.ts` — Gameserver List

```ts
export const gameservers = writable<Gameserver[]>([]);
```

Loaded on dashboard mount. Updated reactively by SSE `status_changed` events — no polling needed for status updates on the dashboard.

## API Client (`api.ts`)

Typed wrapper around `fetch`. Handles:
- Bearer token injection from auth store
- JSON encoding/decoding
- Error response parsing (API returns `{"status": "error", "error": "message"}`)
- 401 → redirect to token entry page
- 403 → toast "Permission denied"

```ts
export const api = {
  gameservers: {
    list: () => get<Gameserver[]>('/api/gameservers'),
    get: (id: string) => get<Gameserver>(`/api/gameservers/${id}`),
    create: (data: CreateGameserver) => post<Gameserver>('/api/gameservers', data),
    update: (id: string, data: Partial<Gameserver>) => patch<Gameserver>(`/api/gameservers/${id}`, data),
    delete: (id: string) => del(`/api/gameservers/${id}`),
    start: (id: string) => post(`/api/gameservers/${id}/start`),
    stop: (id: string) => post(`/api/gameservers/${id}/stop`),
    restart: (id: string) => post(`/api/gameservers/${id}/restart`),
    status: (id: string) => get(`/api/gameservers/${id}/status`),
    stats: (id: string) => get(`/api/gameservers/${id}/stats`),
    query: (id: string) => get(`/api/gameservers/${id}/query`),
    logs: (id: string, tail?: number) => get(`/api/gameservers/${id}/logs`, { tail }),
    command: (id: string, cmd: string) => post(`/api/gameservers/${id}/command`, { command: cmd }),
  },
  backups: {
    list: (gsId: string) => get(`/api/gameservers/${gsId}/backups`),
    create: (gsId: string) => post(`/api/gameservers/${gsId}/backups`),
    restore: (gsId: string, bkId: string) => post(`/api/gameservers/${gsId}/backups/${bkId}/restore`),
    download: (gsId: string, bkId: string) => `/api/gameservers/${gsId}/backups/${bkId}/download`,
    delete: (gsId: string, bkId: string) => del(`/api/gameservers/${gsId}/backups/${bkId}`),
  },
  schedules: {
    list: (gsId: string) => get(`/api/gameservers/${gsId}/schedules`),
    create: (gsId: string, data: any) => post(`/api/gameservers/${gsId}/schedules`, data),
    update: (gsId: string, sId: string, data: any) => patch(`/api/gameservers/${gsId}/schedules/${sId}`, data),
    delete: (gsId: string, sId: string) => del(`/api/gameservers/${gsId}/schedules/${sId}`),
  },
  files: {
    list: (gsId: string, path: string) => get(`/api/gameservers/${gsId}/files`, { path }),
    read: (gsId: string, path: string) => get(`/api/gameservers/${gsId}/files/content`, { path }),
    write: (gsId: string, path: string, content: string) => put(`/api/gameservers/${gsId}/files/content`, { path, content }),
    delete: (gsId: string, path: string) => del(`/api/gameservers/${gsId}/files`, { path }),
    mkdir: (gsId: string, path: string) => post(`/api/gameservers/${gsId}/files/mkdir`, { path }),
    rename: (gsId: string, from: string, to: string) => post(`/api/gameservers/${gsId}/files/rename`, { from, to }),
    upload: (gsId: string, path: string, file: File) => uploadFile(`/api/gameservers/${gsId}/files/upload`, path, file),
    download: (gsId: string, path: string) => `/api/gameservers/${gsId}/files/download?path=${encodeURIComponent(path)}`,
  },
  games: {
    list: () => get(`/api/games`),
    get: (id: string) => get(`/api/games/${id}`),
    options: (gameId: string, key: string) => get(`/api/games/${gameId}/options/${key}`),
  },
  settings: {
    get: () => get(`/api/settings`),
    update: (data: Record<string, any>) => patch(`/api/settings`, data),
  },
  tokens: {
    list: () => get(`/api/tokens`),
    create: (data: any) => post(`/api/tokens`, data),
    delete: (id: string) => del(`/api/tokens/${id}`),
  },
  webhooks: {
    list: () => get(`/api/webhooks`),
    create: (data: any) => post(`/api/webhooks`, data),
    update: (id: string, data: any) => patch(`/api/webhooks/${id}`, data),
    delete: (id: string) => del(`/api/webhooks/${id}`),
    test: (id: string) => post(`/api/webhooks/${id}/test`),
    deliveries: (id: string) => get(`/api/webhooks/${id}/deliveries`),
  },
  workers: {
    list: () => get(`/api/workers`),
    get: (id: string) => get(`/api/workers/${id}`),
  },
};
```

## Error Handling

Two systems, distinct purposes:

### Inline Errors

For synchronous request/response failures that are contextual to what the user is doing:
- Form validation: "Server name is required", "Server name already exists"
- Action failures: "Cannot start: server is already running"
- Displayed next to the relevant field or action

### Toasts

For async events and global notifications:
- SSE-driven: "survival-smp is now running", "Backup completed (245 MB)", "Backup failed: disk full"
- API errors that aren't field-specific: "Failed to connect to API"
- Success confirmations for destructive actions: "Gameserver deleted"

Toasts auto-dismiss after 5 seconds. Error toasts persist until dismissed. Stack in the bottom-right corner.

## Real-Time

One SSE connection to `/api/events` per session.

| Data | Source | Method |
|---|---|---|
| Gameserver status changes | `status_changed` events via SSE | Real-time |
| Lifecycle progress | Lifecycle events via SSE | Real-time |
| Backup/restore completion | `backup.completed`/`backup.failed` via SSE | Real-time |
| Console/log output | `/api/gameservers/{id}/logs?follow=true` | Streaming |
| Resource stats (CPU, memory, storage) | `/api/gameservers/{id}/stats` | Polling (5s) |
| Query data (players, map, version) | `/api/gameservers/{id}/query` | Polling (10s) |
| Worker status | `worker.connected`/`worker.disconnected` via SSE | Real-time |

## Pages

### Dashboard (`/`)

Hero panels for each gameserver — same component works at any count:
- 1-3 servers: full-width stacked panels with telemetry, connection info, quick actions
- 4+: panels stack, scroll naturally
- Status summary bar: "5 running · 2 stopped · 1 error" + search filter
- Empty state: "Create your first gameserver" CTA
- Status badges update in real-time via SSE
- "New Gameserver" button (admin only)
- Clicking a panel → gameserver detail

### Create Gameserver (`/gameservers/new`)

Two-step flow, single page with state toggle:

**Step 1 — Pick a game:**
- Featured games row (4 visual cards for popular games)
- All games searchable list (compact rows with icon, name, description)
- Click any game → step 2

**Step 2 — Configure:**
- Selected game header with "Change" link back to step 1
- Server name (required)
- Memory slider (prefilled from game's recommended, warning if below)
- Game-specific env vars in groups (Server, Gameplay) rendered from game definition
- EULA callout for games that require it (e.g. Minecraft)
- "Create [Game] Server" button
- On success → redirect to gameserver detail, watch install in real-time

### Gameserver Detail (`/gameservers/[id]`)

**Header** (persistent across all tabs, rendered in `[id]/+layout.svelte`):
- Game icon + server name + game name
- Status pill (real-time)
- Action buttons: Stop + Restart (when running), Start (when stopped)
- "..." overflow menu: Update Game, Reinstall
- Uptime display

**Tab bar**: Overview, Console, Files, Backups, Schedules, Settings

### Overview Tab (`/gameservers/[id]`)

- Connection info: game address (prominent, copyable) + SFTP address (secondary, copyable)
- Resource telemetry: Memory, CPU, Storage with progress bars (polled every 5s)
- Server info: Players online/max, player list, map, version (polled every 10s)
- Activity feed: recent lifecycle events via SSE

### Console Tab (`/gameservers/[id]/console`)

- Terminal-style dark background, monospace font
- Log lines stream in real-time with colored categories (joins green, chat white, warnings amber)
- Command input at bottom with `>` prompt
- Clear button

### Files Tab (`/gameservers/[id]/files`)

- Breadcrumb path navigation
- File list: icon, name, size, modified date
- Row actions on hover: edit (text files), download, rename, delete
- Top bar: Upload + New Folder buttons
- In-browser text editor for config files

### Backups Tab (`/gameservers/[id]/backups`)

- Backup list with status badges (completed/in_progress/failed)
- In-progress backups pulse with SSE-driven status updates
- Row actions: Restore (with confirmation), Download, Delete
- "Create Backup" button
- Failed backups show error reason

### Schedules Tab (`/gameservers/[id]/schedules`)

- Schedule list with type badges (restart/backup/command/update)
- Cron expression + human-readable preview
- Enable/disable toggle inline
- Row actions: Edit, Delete
- "Create Schedule" button

### Settings Tab (`/gameservers/[id]/settings`)

- General: server name (editable), game (read-only)
- Environment variables: grouped layout (Server, Gameplay), same as create form
- Resources: memory slider
- SFTP: username (read-only), regenerate password button
- Danger zone (red border): Reinstall, Delete (with confirmation modals)

### Admin Settings (`/settings`)

All settings editable in-place via `PATCH /api/settings`:
- Connection address, port range, port mode
- Auth enable/disable, localhost bypass
- Rate limiting thresholds
- Backup retention, resource requirements
- Event retention, proxy headers

### Admin sub-pages

Workers, Tokens, Webhooks — admin-only pages for cluster management. Accessible from the Settings page or admin nav. Not in the main nav for newbies.

## Permission-Aware Rendering

The auth store provides permission checks. Components conditionally render:

- **No token** (auth disabled): show everything, assume admin
- **Admin token**: show everything
- **Custom token**: hide what the token can't access

Examples:
- No `gameserver.start` → Start button hidden
- No `settings.view` → Settings nav item hidden
- `gameserver_ids` scoped → dashboard only shows those gameservers

Purely cosmetic — the API enforces permissions regardless.

## Auth UX

**Auth disabled (newbie default):** UI just works. No login needed.

**Auth enabled:**
- No token in cookie → token entry page
- Token stored in `_token` cookie (auth middleware reads it)
- On first auth enable via UI: auto-generate admin token, display prominently, store in cookie automatically
- Emergency recovery: `gamejanitor token create --type admin` via CLI

## Design System

### Tokens

Global CSS variables in `src/styles/tokens.css`:
- Brand: `--accent` (#e8722a copper-orange), hover/dim/subtle/border variants
- Surfaces: warm-shifted darks (`--bg-root`, `--bg-surface`, `--bg-elevated`, `--bg-inset`)
- Text: cream-shifted (`--text-primary`, `--text-secondary`, `--text-tertiary`)
- Status: `--live` (green), `--caution` (amber), `--danger` (red), `--idle` (gray)
- Typography: `--font-body` (DM Sans), `--font-mono` (JetBrains Mono)

### Color Roles

- **Orange** = things the user controls (buttons, links, interactive elements, resource usage)
- **Green** = things the server reports (status, health, player count)
- Never mixed — two distinct lanes

### Component Styles

Each Svelte component uses `<style>` for scoped CSS. Global tokens are accessed via CSS variables. No utility class system, no CSS-in-JS.

## What the UI is NOT

- **Not a customer portal** — it's an admin/management interface. Businesses build their own on top of the API.
- **Not a replacement for the CLI** — power users prefer CLI/API. The UI is for visual management.
- **Not a monitoring dashboard** — current state only, no historical graphs.

However, because the UI respects token permissions, businesses *can* expose it to customers. A scoped token only sees their gameservers and allowed actions. Not a designed feature — a natural consequence of permission-aware rendering.
