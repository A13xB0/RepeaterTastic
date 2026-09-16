// Shared labels for plugin states.
import type { Plugin, PluginState } from '@/api/types'

export const stateLabel: Record<PluginState, string> = {
  disabled: 'off',
  needs_review: 'needs review',
  needs_settings: 'needs settings',
  starting: 'starting',
  running: 'running',
  restarting: 'restarting',
  crashed: 'crashed',
  stopped: 'stopped',
  waiting: 'waiting to connect',
  unsupported: 'not for this system',
}

function runningTone(p: Plugin): 'ok' | 'warn' | 'bad' {
  if (p.status?.state === 'error') return 'bad'
  return p.status?.state === 'warning' ? 'warn' : 'ok'
}

export function stateTone(p: Plugin): 'ok' | 'warn' | 'bad' | 'muted' {
  switch (p.state) {
    case 'running':
      return runningTone(p)
    case 'crashed':
    case 'unsupported':
      return 'bad'
    case 'disabled':
    case 'stopped':
      return 'muted'
    default:
      return 'warn'
  }
}

export const toneChip = {
  ok: 'bg-ok/14 text-ok',
  warn: 'bg-warn/15 text-warn',
  bad: 'bg-bad/14 text-bad',
  muted: 'bg-ink-3/14 text-ink-3',
}
export const toneDot = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', muted: 'bg-ink-3' }

/** Permissions that put packets on air. */
export const transmits = (key: string) => key === 'messages.send' || key === 'traceroute.send'
