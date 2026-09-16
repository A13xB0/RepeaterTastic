// Relay modes: how the relay persona repeats, and whether the radio transmits or listens at all.
import type { RebroadcastMode, RelayRole } from '@/api/types'

export interface RelayMode {
  id: RelayRole
  /** relay: how the relay persona repeats; radio: whether the radio transmits at all. */
  group: 'relay' | 'radio'
  label: string
  title: string
  /** Text colour when selected. */
  tone: string
}

// The relay's Meshtastic device role (as meshtasticd runs it), then the two radio switches.
export const relayModes: RelayMode[] = [
  { id: 'client', group: 'relay', label: 'Client', title: 'CLIENT: rebroadcasts after routers and cancels if another node relays first', tone: 'text-brand' },
  { id: 'client_base', group: 'relay', label: 'Client base', title: 'CLIENT_BASE: a client that relays its favourited nodes with router priority', tone: 'text-brand' },
  { id: 'client_mute', group: 'relay', label: 'Client mute', title: 'CLIENT_MUTE: never rebroadcasts; identities still transmit', tone: 'text-bad' },
  { id: 'router', group: 'relay', label: 'Router', title: 'ROUTER: always rebroadcasts, with priority. For a well-placed site', tone: 'text-warn' },
  { id: 'router_late', group: 'relay', label: 'Router late', title: 'ROUTER_LATE: always rebroadcasts, but only after everyone else had the chance', tone: 'text-warn' },
  { id: 'monitor', group: 'radio', label: 'Monitor', title: 'Listen only: nothing is transmitted, not even by identities', tone: 'text-info' },
  { id: 'off', group: 'radio', label: 'Off', title: 'Radio off: nothing is received or transmitted', tone: 'text-bad' },
]

// Meshtastic's rebroadcast modes, as meshtasticd applies them to the relay.
export const rebroadcastModes: { id: RebroadcastMode; label: string; title: string }[] = [
  { id: 'all', label: 'All', title: 'Rebroadcast everything heard, even from other meshes with a known key' },
  { id: 'all_skip_decoding', label: 'All, skip decoding', title: 'Rebroadcast everything without decoding it (repeater only)' },
  { id: 'local_only', label: 'Local only', title: 'Only rebroadcast packets on our own channels; ignore foreign meshes' },
  { id: 'known_only', label: 'Known only', title: 'Only rebroadcast packets from nodes already in the node list' },
  { id: 'core_portnums_only', label: 'Core only', title: 'Only rebroadcast core traffic: text, position, node info, routing, telemetry' },
  { id: 'none', label: 'None', title: 'Never rebroadcast (like client mute)' },
]

/** The relay never repeats in this mode. */
export const muted = (role: string | undefined) => role === 'client_mute' || role === 'mute'

export const relayModeLabel = (role: string | undefined) => relayModes.find((m) => m.id === role)?.label ?? role ?? ''

/** The radio transmits in this mode (not monitor or off). */
export const transmits = (role: string | undefined) => role !== 'monitor' && role !== 'off'

/** Why a send failed, in words (routing error names from the daemon). */
export function sendError(error: string | undefined): string {
  if (error === 'NO_INTERFACE') return "not sent: the radio isn't transmitting (monitor or off)"
  return error || 'failed'
}
