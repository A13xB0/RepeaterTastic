import type { Channel, Identity } from '@/api/types'

export const CHANNEL_SLOTS = 8

/** All eight slots of an identity: the API lists only enabled channels, so fill the gaps. */
export function channelSlots(i: Identity): Channel[] {
  const out: Channel[] = []
  for (let index = 0; index < CHANNEL_SLOTS; index++) {
    out.push(
      i.channels.find((c) => c.index === index) ?? {
        index, role: 'DISABLED', name: '', display_name: '', psk: '', hash: 0, uplink: false, downlink: false, locked: false,
      },
    )
  }
  return out
}

/** The first free secondary slot (1–7), if any. */
export function freeSlot(i: Identity): number | undefined {
  return channelSlots(i).find((c) => c.index > 0 && c.role === 'DISABLED')?.index
}
