// Display derivation helpers for gameservers.
//
// The controller does not emit a compressed `status` enum — it exposes primary
// facts (process_state, ready, worker_online, operation, desired_state,
// error_reason) and lets consumers compute whatever display they need.
//
// This file is the UI's single source for those derivations. If a new display
// state is needed, add it here; don't inline the branching at call sites.

import type { Gameserver } from './api';

// The one-word display phase for a gameserver. Used in pills, dashboard cards,
// and anywhere a glanceable single-word summary is wanted. NEVER used for
// decision logic — use the typed predicates below (isRunning, hasError, etc.).
export type Phase =
  | 'deleting'
  | 'archived'
  | 'unreachable'
  | 'installing'
  | 'stopping'
  | 'starting'
  | 'error'
  | 'running'
  | 'stopped';

export function phaseOf(gs: Gameserver | null | undefined): Phase | '' {
  if (!gs) return '';
  if (gs.operation && gs.operation.phase === 'deleting') return 'deleting';
  if (gs.desired_state === 'archived') return 'archived';
  if (!gs.worker_online) return 'unreachable';
  if (gs.operation) {
    switch (gs.operation.phase) {
      case 'pulling_image':
      case 'downloading_game':
      case 'installing':
        return 'installing';
      case 'stopping':
        return 'stopping';
      case 'starting':
        return 'starting';
      case 'migrating':
        return 'installing';
    }
  }
  if (gs.error_reason) return 'error';
  if (gs.process_state === 'running' && gs.ready) return 'running';
  // Process alive but not ready yet — still "starting" to the user.
  if (gs.process_state === 'running' && !gs.ready) return 'starting';
  return 'stopped';
}

export function phaseLabel(phase: Phase | ''): string {
  if (!phase) return '';
  return phase.charAt(0).toUpperCase() + phase.slice(1);
}

// Typed predicates. Prefer these over string comparison on phase() in decision
// logic — they read the primary facts directly.
export function isRunning(gs: Gameserver | null | undefined): boolean {
  return !!gs && gs.process_state === 'running' && gs.ready;
}

export function isStopped(gs: Gameserver | null | undefined): boolean {
  return !!gs && gs.process_state === 'none' && !gs.operation;
}

export function isArchived(gs: Gameserver | null | undefined): boolean {
  return !!gs && gs.desired_state === 'archived';
}

export function isUnreachable(gs: Gameserver | null | undefined): boolean {
  return !!gs && !gs.worker_online && gs.desired_state !== 'archived';
}

export function hasError(gs: Gameserver | null | undefined): boolean {
  return !!gs?.error_reason;
}

// True when the user would see the gameserver as "busy" — an operation is in
// flight, or the process is transitioning.
export function isBusy(gs: Gameserver | null | undefined): boolean {
  if (!gs) return false;
  if (gs.operation) return true;
  if (gs.process_state === 'creating' || gs.process_state === 'starting') return true;
  // Process running but not yet ready counts as busy (starting phase).
  if (gs.process_state === 'running' && !gs.ready) return true;
  return false;
}
