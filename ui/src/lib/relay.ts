// Relay modes: how the relay persona repeats, and whether the radio transmits or listens at all.
import type { RelayRole } from '@/api/types'

export interface RelayMode {
  id: RelayRole
  label: string
  title: string
  /** Text colour when selected. */
  tone: string
}

export const relayModes: RelayMode[] = [
  { id: 'client', label: 'Client', title: 'Rebroadcast like a normal client: after routers, and cancel if someone else relays', tone: 'text-brand' },
  { id: 'router', label: 'Router', title: 'Rebroadcast with router priority', tone: 'text-warn' },
  { id: 'mute', label: 'Mute', title: 'Never rebroadcast; identities still transmit', tone: 'text-bad' },
  { id: 'monitor', label: 'Monitor', title: 'Listen only: nothing is transmitted, not even by identities', tone: 'text-info' },
  { id: 'off', label: 'Off', title: 'Radio off: nothing is received or transmitted', tone: 'text-bad' },
]

export const relayModeLabel = (role: string | undefined) => relayModes.find((m) => m.id === role)?.label ?? role ?? ''

/** The radio transmits in this mode (not monitor or off). */
export const transmits = (role: string | undefined) => role !== 'monitor' && role !== 'off'

/** Why a send failed, in words (routing error names from the daemon). */
export function sendError(error: string | undefined): string {
  if (error === 'NO_INTERFACE') return "not sent: the radio isn't transmitting (monitor or off)"
  return error || 'failed'
}
