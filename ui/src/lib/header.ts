// Decode the 16-byte Meshtastic over-the-air header from a packet's raw hex (see internal/wire/header.go).

export interface HeaderField {
  name: string
  offset: number
  length: number
  value: string
  hint?: string
}

export interface DecodedHeader {
  bytes: number[]
  fields: HeaderField[]
  flags: { hopLimit: number; wantAck: boolean; viaMqtt: boolean; hopStart: number; raw: number }
  payloadHex: string
}

function u32le(b: number[], o: number): number {
  return (b[o]! | (b[o + 1]! << 8) | (b[o + 2]! << 16) | (b[o + 3]! << 24)) >>> 0
}

export function hexToBytes(hex: string): number[] {
  const out: number[] = []
  for (let i = 0; i + 1 < hex.length; i += 2) out.push(Number.parseInt(hex.slice(i, i + 2), 16))
  return out
}

export function decodeHeader(rawHex: string): DecodedHeader | null {
  const bytes = hexToBytes(rawHex)
  if (bytes.length < 16) return null
  const id = (n: number) => '!' + n.toString(16).padStart(8, '0')
  const f = bytes[12]!
  const flags = { hopLimit: f & 7, wantAck: !!(f & 8), viaMqtt: !!(f & 16), hopStart: (f >> 5) & 7, raw: f }
  return {
    bytes,
    flags,
    fields: [
      { name: 'Destination', offset: 0, length: 4, value: id(u32le(bytes, 0)), hint: u32le(bytes, 0) === 0xffffffff ? 'broadcast' : undefined },
      { name: 'Sender', offset: 4, length: 4, value: id(u32le(bytes, 4)) },
      { name: 'Packet ID', offset: 8, length: 4, value: '0x' + u32le(bytes, 8).toString(16).padStart(8, '0') },
      { name: 'Flags', offset: 12, length: 1, value: '0b' + f.toString(2).padStart(8, '0') },
      { name: 'Channel hash', offset: 13, length: 1, value: '0x' + bytes[13]!.toString(16).padStart(2, '0') },
      { name: 'Next hop', offset: 14, length: 1, value: '0x' + bytes[14]!.toString(16).padStart(2, '0'), hint: bytes[14] ? undefined : 'none' },
      { name: 'Relay node', offset: 15, length: 1, value: '0x' + bytes[15]!.toString(16).padStart(2, '0') },
    ],
    payloadHex: rawHex.slice(32),
  }
}

/** Classic hex dump rows: offset, hex bytes, ASCII. */
export function hexDump(rawHex: string, width = 16): { offset: string; hex: string; ascii: string }[] {
  const b = hexToBytes(rawHex)
  const rows = []
  for (let i = 0; i < b.length; i += width) {
    const chunk = b.slice(i, i + width)
    rows.push({
      offset: i.toString(16).padStart(4, '0'),
      hex: chunk.map((x) => x.toString(16).padStart(2, '0')).join(' '),
      ascii: chunk.map((x) => (x >= 32 && x < 127 ? String.fromCodePoint(x) : '·')).join(''),
    })
  }
  return rows
}
