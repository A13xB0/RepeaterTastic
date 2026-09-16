// In-memory world for the dev mock: a relay on Calton Hill, four identities, ~34 heard nodes
// around Edinburgh and the Lothians, packet history, chat, airtime buckets and logs.
import { randomBytes } from 'node:crypto'
import type {
  AirtimeStats, ApiToken, Channel, Config, Conversation, Identity, IdentityStat, Link, LogLevel, LogLine,
  MeshNode, Message, Packet, PacketKind, PortStat, RfStats, Status, StatsWindow, TracerouteEvent,
} from '../src/api/types.ts'
import { airtimeMs, resolvePhy } from './phy.ts'

export type Emit = (event: string, data: unknown) => void

const now = () => Date.now()
const rnd = (a: number, b: number) => a + Math.random() * (b - a)
const rint = (a: number, b: number) => Math.floor(rnd(a, b + 1))
const pick = <T>(xs: readonly T[]): T => xs[Math.floor(Math.random() * xs.length)]!
const chance = (p: number) => Math.random() < p
const round = (v: number, d = 2) => Math.round(v * 10 ** d) / 10 ** d
const BROADCAST = '!ffffffff'
const idNum = (id: string) => parseInt(id.slice(1), 16) >>> 0
const hex8 = (n: number) => '!' + (n >>> 0).toString(16).padStart(8, '0')
const b64 = (n: number) => randomBytes(n).toString('base64')

// ---------------------------------------------------------------- channels

const DEFAULT_KEY = [0xd4, 0xf1, 0xbb, 0x3a, 0x20, 0x29, 0x07, 0x59, 0xf0, 0xbc, 0xff, 0xab, 0xcf, 0x4e, 0x69, 0x01]

export function channelHash(name: string, psk: string): number {
  let bytes = [...Buffer.from(psk || '', 'base64')]
  if (bytes.length === 1) {
    const k = [...DEFAULT_KEY]
    k[15] = (k[15]! + bytes[0]! - 1) & 0xff
    bytes = bytes[0] === 0 ? [] : k
  }
  let h = 0
  for (const c of Buffer.from(name)) h ^= c
  for (const b of bytes) h ^= b
  return h
}

function primaryChannel(): Channel {
  return { index: 0, role: 'PRIMARY', name: '', display_name: 'LongFast', psk: 'AQ==', hash: channelHash('LongFast', 'AQ=='),
    uplink: false, downlink: false, locked: true }
}

export function channelSlots(extra: Partial<Channel>[] = []): Channel[] {
  const out: Channel[] = [primaryChannel()]
  for (let i = 1; i < 8; i++) {
    const e = extra.find((c) => c.index === i)
    const name = e?.name ?? ''
    const psk = e?.psk ?? ''
    out.push({ index: i, role: e?.role ?? 'DISABLED', name, display_name: name, psk, hash: e ? channelHash(name, psk) : 0,
      uplink: e?.uplink ?? false, downlink: e?.downlink ?? false, locked: false })
  }
  return out
}

// ---------------------------------------------------------------- identities

const HOUR = 3_600_000
const created = now() - 23 * 24 * HOUR

function identity(p: Partial<Identity> & Pick<Identity, 'node_id' | 'long_name' | 'short_name'>): Identity {
  return {
    node_num: idNum(p.node_id), role: 'CLIENT', hw_model: 'PORTDUINO', public_key: b64(32), is_relay: false, enabled: true,
    api: null, outbox: 0, airtime_ms_1h: 0, share_pct: 0, share_limit_pct: 25, unread: 0, created_at: created,
    channels: channelSlots(), ...p,
  }
}

export const state = {
  setupNeeded: process.env.MOCK_SETUP === '1',
  password: 'meshtastic',
  sessions: new Set<string>(),
  startedAt: now() - 5234_000,
  relayRole: 'client' as import('../src/api/types').RelayRole,
  identities: [] as Identity[],
  nodes: new Map<string, MeshNode>(),
  packets: [] as Packet[],
  messages: new Map<string, Message[]>(),
  unread: new Map<string, Map<string, number>>(),
  logs: [] as LogLine[],
  tokens: [] as (ApiToken & { secret: string })[],
  seq: 88_000,
  counters: { rx: 1203, rx_dupe: 402, rx_undecryptable: 77, tx: 311, relayed: 120, relay_cancelled: 33, ack_ok: 41, ack_fail: 3, dropped_duty: 0 },
  noise: -118,
  config: null as unknown as Config,
  links: [] as Link[],
}

state.identities = [
  identity({ node_id: '!3f0a91c2', long_name: 'RepeaterTastic Relay', short_name: 'RPTR', is_relay: true, role: 'ROUTER',
    airtime_ms_1h: 9800, share_limit_pct: 100, created_at: created - HOUR }),
  identity({ node_id: '!a1c40e07', long_name: 'Base Camp', short_name: 'BASE', api: { bind: '0.0.0.0', port: 4403, clients: 1, listening: true },
    airtime_ms_1h: 3200 }),
  identity({ node_id: '!5b9e2213', long_name: 'Ops Desk', short_name: 'OPS', api: { bind: '0.0.0.0', port: 4404, clients: 0, listening: true },
    outbox: 12, airtime_ms_1h: 1100, created_at: created + 2 * 24 * HOUR,
    channels: channelSlots([{ index: 1, role: 'SECONDARY', name: 'LothianOps', psk: 'q2YH8n1Vx0bE3cE1s7w8pF0c6rJmKf9Qe2bT4yUuVhA=' }]) }),
  identity({ node_id: '!7d21e4a9', long_name: 'Weather Bot', short_name: 'WX', role: 'SENSOR', api: { bind: '0.0.0.0', port: 4405, clients: 1, listening: true },
    airtime_ms_1h: 133600, created_at: created + 6 * 24 * HOUR }),
  identity({ node_id: '!e41b6c58', long_name: 'Pentland Hut', short_name: 'PHUT', enabled: false,
    api: { bind: '0.0.0.0', port: 4406, clients: 0, listening: true }, created_at: now() - 3 * 24 * HOUR }),
]

export const identityById = (id: string) => state.identities.find((i) => i.node_id === id)

function recomputeShares() {
  const total = state.identities.reduce((s, i) => s + i.airtime_ms_1h, 0) || 1
  for (const i of state.identities) i.share_pct = round((i.airtime_ms_1h / total) * 100, 1)
}
recomputeShares()

// ---------------------------------------------------------------- heard nodes

const HOME = { lat: 55.9553, lon: -3.1826 } // Calton Hill

type Seed = [string, string, number | null, number | null, string, string]
const SEEDS: Seed[] = [
  ['Calton Hill Router', 'CALT', 55.9556, -3.1812, 'STATION_G2', 'ROUTER'],
  ["Arthur's Seat", 'ARTH', 55.9441, -3.1618, 'RAK4631', 'ROUTER_LATE'],
  ['Leith Shore', 'LETH', 55.976, -3.1703, 'HELTEC_V3', 'CLIENT'],
  ['Portobello Prom', 'PRTB', 55.9531, -3.107, 'T_ECHO', 'CLIENT'],
  ['Morningside Loft', 'MSDE', 55.928, -3.21, 'HELTEC_V3', 'CLIENT'],
  ['Bruntsfield Links', 'BRUN', 55.938, -3.203, 'TBEAM', 'CLIENT'],
  ['Pentland Hills Mast', 'PENT', 55.857, -3.278, 'RAK4631', 'ROUTER'],
  ['Blackford Observatory', 'BLKF', 55.923, -3.188, 'STATION_G2', 'CLIENT'],
  ['Stockbridge Van', 'STBR', 55.959, -3.209, 'T_DECK', 'CLIENT'],
  ['Cramond Walker', 'CRAM', 55.98, -3.3, 'HELTEC_WIRELESS_TRACKER', 'TRACKER'],
  ['South Queensferry', 'SQFY', 55.99, -3.396, 'RAK4631', 'CLIENT'],
  ['Corstorphine Hill', 'CORS', 55.945, -3.275, 'HELTEC_V3', 'CLIENT_MUTE'],
  ['Craigmillar Castle', 'CRMR', 55.926, -3.14, 'T_ECHO', 'CLIENT'],
  ['Musselburgh Harbour', 'MUSS', 55.944, -3.054, 'HELTEC_V3', 'CLIENT'],
  ['Dalkeith Solar', 'DLKT', 55.893, -3.068, 'RAK4631', 'SENSOR'],
  ['Penicuik Allotments', 'PNCK', 55.829, -3.223, 'SEEED_XIAO_S3', 'CLIENT'],
  ['Livingston North', 'LIVN', 55.902, -3.522, 'HELTEC_V3', 'CLIENT'],
  ['Linlithgow Palace', 'LNLG', 55.976, -3.601, 'TBEAM', 'ROUTER'],
  ['Kirkcaldy Esplanade', 'KCDY', 56.11, -3.158, 'T_ECHO', 'CLIENT'],
  ['Burntisland Links', 'BURN', 56.059, -3.233, 'HELTEC_V3', 'CLIENT'],
  ['North Berwick Law', 'NBLW', 56.052, -2.717, 'RAK4631', 'ROUTER'],
  ['Haddington Mill', 'HDGN', 55.956, -2.781, 'HELTEC_V3', 'CLIENT'],
  ['Tranent Tracker', 'TRNT', 55.944, -2.954, 'TRACKER_T1000_E', 'TRACKER'],
  ['Dunfermline Abbey', 'DUNF', 56.07, -3.463, 'STATION_G2', 'CLIENT'],
  ['Falkirk Wheel', 'FLKW', 56.0, -3.841, 'HELTEC_V3', 'CLIENT'],
  ['Peebles Tweed', 'PEEB', 55.652, -3.188, 'RAK4631', 'CLIENT'],
  ['Stirling Castle', 'STRL', 56.124, -3.946, 'T_ECHO', 'ROUTER'],
  ['Bathgate Hills', 'BTHG', 55.902, -3.644, 'HELTEC_V3', 'CLIENT'],
  ['Juniper Green', 'JGRN', 55.902, -3.283, 'TBEAM', 'CLIENT'],
  ['Gorgie Farm', 'GRGE', 55.937, -3.24, 'HELTEC_V3', 'CLIENT'],
  ['Glasgow West End', 'GLWE', 55.872, -4.289, 'HELTEC_V3', 'CLIENT'],
  ["Sam's T-Deck", 'SAMD', null, null, 'T_DECK', 'CLIENT'],
  ['Meshtastic 4f1a', '4f1a', null, null, 'HELTEC_V3', 'CLIENT'],
  ['Fife Coastal Relay', 'FCRL', null, null, 'RAK4631', 'ROUTER'],
]

function km(lat1: number, lon1: number, lat2: number, lon2: number) {
  const r = Math.PI / 180
  const x = (lon2 - lon1) * r * Math.cos(((lat1 + lat2) / 2) * r)
  const y = (lat2 - lat1) * r
  return Math.sqrt(x * x + y * y) * 6371
}

const usedLastBytes = new Set(state.identities.map((i) => i.node_num & 0xff))
for (const [long, short, lat, lon, hw, role] of SEEDS) {
  let num: number
  do num = (Math.random() * 0xffffffff) >>> 0
  while (usedLastBytes.has(num & 0xff))
  usedLastBytes.add(num & 0xff)
  const dist = lat != null && lon != null ? km(HOME.lat, HOME.lon, lat, lon) : rnd(5, 40)
  const mqtt = short === 'GLWE'
  const hops = mqtt ? null : dist < 5 ? 0 : dist < 18 ? 1 : dist < 45 ? 2 : 3
  const heardAgo = chance(0.8) ? rnd(0.5, 150) * 60_000 : rnd(3, 40) * HOUR
  const node: MeshNode = {
    node_id: hex8(num), node_num: num, long_name: long, short_name: short, hw_model: hw, role,
    has_public_key: chance(0.85), last_heard: Math.round(now() - heardAgo),
    snr: hops === null ? null : round(hops === 0 ? rnd(2, 11) : rnd(-12, 6), 2),
    rssi: hops === null ? null : Math.round(hops === 0 ? rnd(-95, -60) : rnd(-122, -95)),
    hops_away: hops, via_mqtt: mqtt, local: false, next_hop: null,
    position: lat != null && lon != null ? { lat, lon, alt: Math.round(rnd(10, role.startsWith('ROUTER') ? 420 : 160)), time: Math.round(now() - heardAgo) } : null,
    telemetry: chance(0.75) ? { battery: role === 'ROUTER' ? 101 : rint(34, 100), voltage: round(rnd(3.6, 4.2), 2),
      channel_util: round(rnd(4, 18), 1), air_util_tx: round(rnd(0.2, 3.5), 2) } : null,
    known_by: state.identities.filter((i) => i.enabled && chance(0.9)).map((i) => i.node_id),
  }
  state.nodes.set(node.node_id, node)
}
for (const i of state.identities) {
  state.nodes.set(i.node_id, {
    node_id: i.node_id, node_num: i.node_num, long_name: i.long_name, short_name: i.short_name, hw_model: i.hw_model, role: i.role,
    has_public_key: true, last_heard: now(), snr: null, rssi: null, hops_away: 0, via_mqtt: false, local: true, next_hop: null,
    position: { lat: HOME.lat + rnd(-0.002, 0.002), lon: HOME.lon + rnd(-0.003, 0.003), alt: 102, time: now() },
    telemetry: null, known_by: state.identities.map((x) => x.node_id),
  })
}
// Two heard neighbours route through Calton/Arthur's Seat as next hop.
{
  const calt = [...state.nodes.values()].find((n) => n.short_name === 'CALT')!
  for (const n of state.nodes.values()) if (!n.local && (n.hops_away ?? 0) >= 2 && chance(0.5)) n.next_hop = calt.node_num & 0xff
}

export const heard = () => [...state.nodes.values()].filter((n) => !n.local)
export const nodeName = (id: string) => state.nodes.get(id)?.short_name ?? id

// ---------------------------------------------------------------- packets

const TEXTS = [
  'Morning all, Pentland mast is back up after the gales',
  'Anyone hearing Kirkcaldy across the Forth today?',
  'Testing a new T-Echo from Portobello, 2 hops to Calton',
  'Signal check please from Leith Shore',
  "Copy loud and clear from Arthur's Seat",
  'Heading up Blackford Hill with the Station G2 at lunch',
  'Rain moving in from the west, waterproofs for the Pentlands walk',
  'North Berwick Law relay is on solar now, battery holding 94%',
  "Who's running the Stockbridge node? Great coverage down to the Water of Leith",
  'Traceroute to Stirling took 3 hops via Linlithgow',
  'Meetup Saturday 11:00 at Bruntsfield Links, bring radios',
  'Is LongFast still the default for the Lothians group?',
  'Just flashed 2.8.1 on the RAK, NodeInfo looks fine',
  'Heard a burst of undecryptable traffic from the Fife side, new channel?',
  'Tram works on Leith Walk killing my rooftop antenna line of sight',
  'Anyone got a spare 868 collinear? Swapping mine on Corstorphine Hill',
]
const DM_TEXTS = [
  'Are you still at base camp this afternoon?',
  'Can you check the hut battery when you are up there?',
  'Got your message, all good here',
  'Will swap the antenna tomorrow morning',
  'Thanks, see you at the meetup',
]
const WX = () => {
  const t = round(rnd(9, 16), 1)
  return `WX Edinburgh ${t}°C, ${rint(68, 92)}% RH, ${rint(996, 1021)} hPa, wind ${pick(['W', 'WSW', 'SW', 'NW'])} ${rint(8, 38)} km/h`
}

function header(to: number, from: number, id: number, flags: number, hash: number, nextHop: number, relay: number): string {
  const b = Buffer.alloc(16)
  b.writeUInt32LE(to >>> 0, 0)
  b.writeUInt32LE(from >>> 0, 4)
  b.writeUInt32LE(id >>> 0, 8)
  b[12] = flags; b[13] = hash; b[14] = nextHop; b[15] = relay
  return b.toString('hex')
}

interface PacketSpec {
  from: string; to?: string; port: string; payload: unknown; summary: string; kind: PacketKind; direction?: 'rx' | 'tx'
  channel?: number; want_ack?: boolean; pki?: boolean; hopStart?: number; hops?: number; id?: number; time?: number; decodedBy?: string
}

export function makePacket(s: PacketSpec): Packet {
  const node = state.nodes.get(s.from)
  const direction = s.direction ?? (s.kind === 'ours' && identityById(s.from) ? 'tx' : 'rx')
  const hopStart = s.hopStart ?? 3
  const hops = s.hops ?? (direction === 'tx' ? 0 : Math.min(node?.hops_away ?? rint(0, 2), hopStart))
  const hopLimit = hopStart - hops
  const undecryptable = s.kind === 'undecryptable'
  const hash = undecryptable ? rint(0, 255) : s.channel === 1 ? channelHash('LothianOps', 'q2YH8n1Vx0bE3cE1s7w8pF0c6rJmKf9Qe2bT4yUuVhA=') : 8
  const to = s.to ?? BROADCAST
  const id = s.id ?? (Math.random() * 0xffffffff) >>> 0
  const want = s.want_ack ?? false
  const via = node?.via_mqtt ?? false
  const flags = (hopLimit & 7) | (want ? 8 : 0) | (via ? 16 : 0) | ((hopStart & 7) << 5)
  const bodyLen = undecryptable ? rint(18, 60) : Math.max(8, Math.min(220, JSON.stringify(s.payload ?? '').length / 2 + rint(4, 20)))
  const size = 16 + Math.round(bodyLen)
  const relayNode = hops > 0 ? pick(heard().filter((n) => n.role.startsWith('ROUTER'))).node_num & 0xff : idNum(s.from) & 0xff
  const raw = header(idNum(to), idNum(s.from), id, flags, hash, 0, relayNode) + randomBytes(size - 16).toString('hex')
  const rx = direction === 'rx'
  return {
    seq: ++state.seq, time: s.time ?? now(), direction, kind: s.kind, id, from: s.from, to, channel_hash: hash,
    channel: undecryptable ? '' : s.channel === 1 ? 'LothianOps' : 'LongFast', port: undecryptable ? '' : s.port,
    hop_limit: hopLimit, hop_start: hopStart, want_ack: want, via_mqtt: via, next_hop: 0, relay_node: relayNode,
    rssi: rx ? Math.round(node?.rssi ?? rnd(-120, -70)) + rint(-3, 3) : null,
    snr: rx ? round((node?.snr ?? rnd(-10, 8)) + rnd(-1.5, 1.5), 2) : null,
    size, airtime_ms: airtimeMs(size), decoded_by: undecryptable ? '' : s.decodedBy ?? (s.pki ? s.to ?? '' : '!a1c40e07'),
    pki: s.pki ?? false, summary: s.summary, payload: undecryptable ? null : s.payload, raw,
  }
}

function randomRx(time?: number): Packet {
  const n = pick(heard())
  const r = Math.random()
  const kind: PacketKind = r < 0.42 ? 'delivered' : r < 0.66 ? 'dup' : r < 0.8 ? 'relayed' : r < 0.9 ? 'undecryptable' : 'ours'
  const pr = Math.random()
  if (kind === 'undecryptable')
    return makePacket({ from: n.node_id, port: '', payload: null, summary: 'encrypted, unknown channel', kind, time })
  if (kind !== 'ours' && pr < 0.14) {
    const text = pick(TEXTS)
    return makePacket({ from: n.node_id, port: 'TEXT_MESSAGE_APP', payload: { text }, summary: text, kind, time })
  }
  if (kind !== 'ours' && pr < 0.42 && n.position) {
    const lat = n.position.lat + rnd(-0.0005, 0.0005), lon = n.position.lon + rnd(-0.0005, 0.0005)
    return makePacket({ from: n.node_id, port: 'POSITION_APP', kind, time,
      payload: { latitude_i: Math.round(lat * 1e7), longitude_i: Math.round(lon * 1e7), altitude: n.position.alt, time: Math.floor(now() / 1000), precision_bits: 32, sats_in_view: rint(5, 14) },
      summary: `${lat.toFixed(4)}, ${lon.toFixed(4)} · alt ${n.position.alt} m` })
  }
  if (kind !== 'ours' && pr < 0.62)
    return makePacket({ from: n.node_id, port: 'TELEMETRY_APP', kind, time,
      payload: { time: Math.floor(now() / 1000), device_metrics: { battery_level: n.telemetry?.battery ?? 88, voltage: n.telemetry?.voltage ?? 4.02, channel_utilization: round(rnd(5, 16), 1), air_util_tx: round(rnd(0.3, 3), 2), uptime_seconds: rint(3600, 900000) } },
      summary: `battery ${n.telemetry?.battery ?? 88}% · ${(n.telemetry?.voltage ?? 4.02).toFixed(2)} V · ch util ${n.telemetry?.channel_util ?? 9.4}%` })
  if (kind !== 'ours' && pr < 0.78)
    return makePacket({ from: n.node_id, port: 'NODEINFO_APP', kind, time,
      payload: { id: n.node_id, long_name: n.long_name, short_name: n.short_name, hw_model: n.hw_model, role: n.role, public_key: n.has_public_key ? b64(32) : '' },
      summary: `${n.short_name} ${n.long_name} · ${n.hw_model}` })
  if (kind !== 'ours' && pr < 0.88) {
    const ns = heard().slice(0, rint(2, 5)).map((x) => ({ node_id: x.node_num, snr: round(rnd(-8, 10), 2) }))
    return makePacket({ from: n.node_id, port: 'NEIGHBORINFO_APP', kind, time, payload: { node_id: n.node_num, node_broadcast_interval_secs: 10800, neighbors: ns }, summary: `${ns.length} neighbours` })
  }
  const req = (Math.random() * 0xffffffff) >>> 0
  if (kind !== 'ours') // neighbour-info / misc broadcasts from nodes without a position
    return makePacket({ from: n.node_id, port: 'NODEINFO_APP', kind, time, payload: { id: n.node_id, long_name: n.long_name, short_name: n.short_name, hw_model: n.hw_model, role: n.role }, summary: `${n.short_name} ${n.long_name} · ${n.hw_model}` })
  return makePacket({ from: n.node_id, to: pick(state.identities.filter((i) => i.enabled)).node_id, port: 'ROUTING_APP', kind: 'ours', time,
    payload: { error_reason: 'NONE', request_id: req }, summary: `ACK for 0x${req.toString(16).padStart(8, '0')}` })
}

export function pushPacket(p: Packet, emit: Emit) {
  state.packets.unshift(p)
  if (state.packets.length > 3000) state.packets.length = 3000
  const c = state.counters
  if (p.direction === 'tx') c.tx++
  else {
    c.rx++
    if (p.kind === 'dup') c.rx_dupe++
    if (p.kind === 'undecryptable') c.rx_undecryptable++
    if (p.kind === 'relayed') { c.relayed++; c.tx++ }
  }
  emit('packet', p)
  const n = state.nodes.get(p.from)
  if (n && !n.local && p.direction === 'rx' && p.kind !== 'dup') {
    n.last_heard = p.time
    if (p.snr != null) n.snr = p.snr
    if (p.rssi != null) n.rssi = p.rssi
    emit('node', n)
  }
}

// seed ~2 h of history
{
  const hist: Packet[] = []
  for (let t = now() - 2 * HOUR; t < now(); t += rint(4_000, 30_000)) hist.push(randomRx(t))
  for (const p of hist.reverse()) state.packets.push(p)
  state.packets.sort((a, b) => b.time - a.time)
  state.packets.forEach((p, i) => (p.seq = state.seq - i))
}

// ---------------------------------------------------------------- messages

const keyFor = (identity: string, m: Message) =>
  m.to === BROADCAST ? `ch:${m.channel}` : `dm:${m.direction === 'out' || m.from === identity ? m.to : m.from}`

function addMessage(identity: string, m: Message, emit?: Emit, countUnread = true) {
  const list = state.messages.get(identity) ?? []
  const idx = list.findIndex((x) => x.id === m.id)
  if (idx >= 0) list[idx] = m
  else list.push(m)
  state.messages.set(identity, list)
  if (m.direction === 'in' && idx < 0 && countUnread) {
    const u = state.unread.get(identity) ?? new Map<string, number>()
    const k = keyFor(identity, m)
    u.set(k, (u.get(k) ?? 0) + 1)
    state.unread.set(identity, u)
    const ident = identityById(identity)
    if (ident) ident.unread = [...u.values()].reduce((a, b) => a + b, 0)
  }
  emit?.('message', { identity, message: m })
}

function msg(p: Partial<Message> & Pick<Message, 'from' | 'to' | 'text' | 'direction'>): Message {
  const node = state.nodes.get(p.from)
  const inbound = p.direction === 'in' && !node?.local
  return {
    id: (Math.random() * 0xffffffff) >>> 0, channel: 0, time: now(), status: p.direction === 'in' ? 'received' : 'acked', error: '',
    pki: false, rssi: inbound ? node?.rssi ?? -100 : null, snr: inbound ? node?.snr ?? 2 : null, hops: inbound ? node?.hops_away ?? 1 : null, ...p,
  }
}

{
  const base = '!a1c40e07', ops = '!5b9e2213', wx = '!7d21e4a9', relay = '!3f0a91c2'
  const locals = [relay, base, ops, wx]
  let t = now() - 26 * HOUR
  for (let i = 0; i < 18; i++) {
    t += rint(20, 110) * 60_000
    const n = pick(heard().filter((x) => !x.via_mqtt))
    const text = TEXTS[i % TEXTS.length]!
    for (const id of locals) addMessage(id, msg({ from: n.node_id, to: BROADCAST, text, direction: 'in', time: t }), undefined, t > now() - HOUR)
    if (i % 5 === 2) {
      const out = msg({ from: base, to: BROADCAST, text: pick(['Base Camp hearing you fine, SNR 7', 'Copy that, relay is up on Calton Hill', 'Base Camp is QRV all afternoon']), direction: 'out', time: t + 90_000 })
      addMessage(base, out)
      for (const id of [relay, ops, wx]) addMessage(id, { ...out, direction: 'in', status: 'received' }, undefined, false)
    }
    if (i % 4 === 1) {
      const w = msg({ from: wx, to: BROADCAST, text: WX(), direction: 'out', time: t + 30_000 })
      addMessage(wx, w)
      for (const id of [relay, base, ops]) addMessage(id, { ...w, direction: 'in', status: 'received' }, undefined, false)
    }
  }
  const leith = heard().find((n) => n.short_name === 'LETH')!
  const pent = heard().find((n) => n.short_name === 'PENT')!
  let d = now() - 5 * HOUR
  const dm: [string, string, string][] = [
    [leith.node_id, base, 'Are you still at base camp this afternoon?'],
    [base, leith.node_id, 'Yes until 6, relay is running off the car battery'],
    [leith.node_id, base, 'Great, I will bring the spare Heltec over'],
    [base, leith.node_id, 'Cheers, park by the observatory gate'],
  ]
  for (const [from, to, text] of dm) {
    d += rint(3, 15) * 60_000
    addMessage(base, msg({ from, to, text, direction: from === base ? 'out' : 'in', pki: true, time: d }), undefined, false)
  }
  addMessage(base, msg({ from: leith.node_id, to: base, text: 'On my way, 20 minutes', direction: 'in', pki: true, time: now() - 40 * 60_000 }))
  addMessage(base, msg({ from: base, to: pent.node_id, text: 'Pentland mast, can you confirm your solar charge?', direction: 'out', pki: true, time: now() - 25 * 60_000, status: 'failed', error: 'no ACK after 3 retries' }))
  const o1 = msg({ from: ops, to: base, text: 'Ops here, routing the Saturday meetup notices through you', direction: 'out', pki: true, time: now() - 3 * HOUR })
  addMessage(ops, o1)
  addMessage(base, { ...o1, direction: 'in', status: 'received' }, undefined, false)
  const o2 = msg({ from: base, to: ops, text: 'Received, delivered locally without RF', direction: 'out', pki: true, time: now() - 3 * HOUR + 60_000 })
  addMessage(base, o2)
  addMessage(ops, { ...o2, direction: 'in', status: 'received' }, undefined, false)
  let c1 = now() - 9 * HOUR
  for (const text of ['Net control check-in at 19:00 on LothianOps', 'CALT and ARTH both reporting', 'Standing down, 73']) {
    c1 += rint(10, 50) * 60_000
    addMessage(ops, msg({ from: pick(heard()).node_id, to: BROADCAST, channel: 1, text, direction: 'in', time: c1 }))
  }
}

export function conversations(identity: string): Conversation[] {
  const ident = identityById(identity)
  const list = state.messages.get(identity) ?? []
  const unread = state.unread.get(identity) ?? new Map<string, number>()
  const out = new Map<string, Conversation>()
  for (const ch of ident?.channels ?? []) {
    if (ch.role === 'DISABLED') continue
    out.set(`ch:${ch.index}`, { key: `ch:${ch.index}`, title: ch.display_name || ch.name, last_text: '', last_time: 0, unread: 0 })
  }
  for (const m of list) {
    const k = keyFor(identity, m)
    const c = out.get(k) ?? { key: k, title: k.startsWith('dm:') ? state.nodes.get(k.slice(3))?.long_name ?? k.slice(3) : k, last_text: '', last_time: 0, unread: 0 }
    if (m.time >= c.last_time) { c.last_time = m.time; c.last_text = m.text }
    c.unread = unread.get(k) ?? 0
    out.set(k, c)
  }
  return [...out.values()].sort((a, b) => b.last_time - a.last_time)
}

export function messages(identity: string, conversation: string, before: number, limit: number): Message[] {
  const list = (state.messages.get(identity) ?? []).filter((m) => keyFor(identity, m) === conversation && m.time < before)
  return list.sort((a, b) => a.time - b.time).slice(-limit)
}

export function markRead(identity: string, key: string, emit: Emit) {
  const u = state.unread.get(identity)
  const ident = identityById(identity)
  if (!u || !ident) return
  u.delete(key)
  ident.unread = [...u.values()].reduce((a, b) => a + b, 0)
  emit('identity', ident)
}

export function sendMessage(identity: string, body: { to: string; channel: number; text: string; want_ack: boolean }, emit: Emit): Message {
  const ident = identityById(identity)!
  const dm = body.to !== BROADCAST
  const target = state.nodes.get(body.to)
  const m: Message = { id: (Math.random() * 0xffffffff) >>> 0, from: identity, to: body.to, channel: dm ? 0 : body.channel, text: body.text,
    time: now(), direction: 'out', status: 'queued', error: '', pki: dm, rssi: null, snr: null, hops: null }
  addMessage(identity, m)
  const localTarget = target?.local
  setTimeout(() => {
    m.status = 'sent'
    emit('message', { identity, message: { ...m } })
    if (!localTarget) {
      const p = makePacket({ from: identity, to: body.to, port: 'TEXT_MESSAGE_APP', payload: { text: body.text }, summary: body.text, kind: 'ours', direction: 'tx', channel: m.channel, want_ack: body.want_ack, pki: dm, id: m.id, decodedBy: identity })
      pushPacket(p, emit)
    } else {
      pushPacket(makePacket({ from: identity, to: body.to, port: 'TEXT_MESSAGE_APP', payload: { text: body.text }, summary: body.text, kind: 'local', direction: 'tx', pki: dm, id: m.id, hopStart: 0, decodedBy: body.to }), emit)
    }
  }, rint(250, 900))
  setTimeout(() => {
    if (dm && target && !target.local && (target.hops_away ?? 3) >= 2 && chance(0.35)) {
      m.status = 'failed'
      m.error = 'no ACK after 3 retries'
      state.counters.ack_fail++
    } else {
      m.status = 'acked'
      state.counters.ack_ok++
    }
    emit('message', { identity, message: { ...m } })
  }, dm ? rint(2500, 6000) : rint(1800, 3500))
  // deliver locally
  if (!dm) {
    for (const other of state.identities) if (other.node_id !== identity && other.enabled && other.channels[m.channel]?.role !== 'DISABLED')
      addMessage(other.node_id, { ...m, direction: 'in', status: 'received' }, emit)
  } else if (localTarget) {
    addMessage(body.to, { ...m, direction: 'in', status: 'received', pki: true }, emit)
  } else if (target && chance(0.6)) {
    setTimeout(() => {
      addMessage(identity, msg({ from: target.node_id, to: identity, text: pick(DM_TEXTS), direction: 'in', pki: true }), emit)
      emit('identity', ident)
    }, rint(8000, 16000))
  }
  return m
}

// ---------------------------------------------------------------- traceroute

export function traceroute(from: string, target: string, emit: Emit) {
  const t = state.nodes.get(target)
  const routers = heard().filter((n) => n.role.startsWith('ROUTER') && n.node_id !== target)
  setTimeout(() => {
    let ev: TracerouteEvent
    if (!t || (t.hops_away ?? 0) >= 3 || chance(0.12)) {
      ev = { identity: from, target, route: [], snr_towards: [], route_back: [], snr_back: [], error: 'no response within 60 s' }
    } else {
      const hops = t.hops_away ?? 1
      const route = [...routers].sort(() => Math.random() - 0.5).slice(0, hops).map((n) => n.node_id)
      const back = chance(0.7) ? [...route].reverse() : [...routers].sort(() => Math.random() - 0.5).slice(0, hops).map((n) => n.node_id)
      ev = { identity: from, target, route, snr_towards: [...route, 0].map(() => round(rnd(-9, 10), 2)), route_back: back, snr_back: [...back, 0].map(() => round(rnd(-9, 10), 2)) }
    }
    emit('traceroute', ev)
    log(ev.error ? 'warn' : 'info', `traceroute ${nodeName(from)} → ${nodeName(target)}: ${ev.error ?? `${ev.route.length} hop(s) via ${ev.route.map(nodeName).join(', ') || 'direct'}`}`, emit)
  }, rint(2500, 6500))
  pushPacket(makePacket({ from, to: target, port: 'TRACEROUTE_APP', payload: { route: [] }, summary: 'traceroute request', kind: 'ours', direction: 'tx', want_ack: true, decodedBy: from }), emit)
}

// ---------------------------------------------------------------- logs

export function log(level: LogLevel, text: string, emit?: Emit, time = now()) {
  const l = { time, level, msg: text }
  state.logs.push(l)
  if (state.logs.length > 2000) state.logs.splice(0, state.logs.length - 2000)
  emit?.('log', l)
}

function packetLog(p: Packet, emit?: Emit) {
  const who = nodeName(p.from)
  const id = '0x' + p.id.toString(16).padStart(8, '0')
  switch (p.kind) {
    case 'relayed': return log('info', `relay: rebroadcast ${id} from ${who} (hop_limit ${p.hop_limit}→${Math.max(0, p.hop_limit - 1)})`, emit, p.time)
    case 'dup': return log('debug', `history: duplicate ${id} from ${who} via relay 0x${p.relay_node.toString(16)}, dropped`, emit, p.time)
    case 'undecryptable': return log('debug', `rx: ${p.size} B from ${who} on unknown channel hash 0x${p.channel_hash.toString(16).padStart(2, '0')}`, emit, p.time)
    case 'ours': return log('info', `${p.direction} ${p.port} ${id} ${who} → ${nodeName(p.to)} (${p.airtime_ms} ms)`, emit, p.time)
    default: return log('debug', `rx ${p.port} ${id} from ${who} snr=${p.snr} rssi=${p.rssi} hops=${p.hop_start - p.hop_limit}`, emit, p.time)
  }
}

{
  const boot = state.startedAt
  const lines: [LogLevel, string][] = [
    ['info', 'RepeaterTastic 0.1.0 starting'],
    ['info', 'config: loaded /etc/repeatertastic/config.yaml'],
    ['info', 'radio: opening kiss modem on /dev/serial/by-id/usb-Silicon_Labs_CP2102_USB_to_UART_Bridge_Controller_0001-if00-port0'],
    ['info', 'radio: Heltec V3, firmware Mesh KISS v2 (sync word 0x2B, preamble 16)'],
    ['info', 'phy: EU_868 LongFast 869.525 MHz bw 250 kHz sf 11 cr 4/5, slot 1/1, 27 dBm'],
    ['info', 'identity: RepeaterTastic Relay !3f0a91c2 (relay persona, role client)'],
    ['info', 'identity: Base Camp !a1c40e07 api 0.0.0.0:4403'],
    ['info', 'identity: Ops Desk !5b9e2213 api 0.0.0.0:4404'],
    ['info', 'identity: Weather Bot !7d21e4a9 api 0.0.0.0:4405'],
    ['warn', 'identity: Pentland Hut !e41b6c58 is disabled, api not started'],
    ['info', 'nodedb: loaded 34 nodes'],
    ['info', 'link udp: joined 239.0.0.69:4403 on eth0'],
    ['warn', 'link glasgow-site: dial tcp 81.2.69.160:4410: connection refused, retrying in 30s'],
    ['info', 'web: listening on 0.0.0.0:8080'],
    ['info', 'api :4403 client connected from 192.168.1.42:51522 (Meshtastic Android 2.8.1)'],
    ['info', 'api :4405 client connected from 127.0.0.1:40114 (python meshtastic 2.7.2)'],
  ]
  lines.forEach(([lvl, text], i) => log(lvl, text, undefined, boot + i * 180))
  for (const p of [...state.packets].reverse().slice(-260)) packetLog(p)
}

// ---------------------------------------------------------------- status & stats

export function status(): Status {
  const relay = state.identities[0]!
  const txMs = state.identities.reduce((s, i) => s + i.airtime_ms_1h, 0)
  const phy = resolvePhy(state.config.radio.region, state.config.radio.preset, state.config.radio.primary_channel, state.config.radio.tx_power_dbm)
  return {
    version: '0.1.0', uptime_s: Math.floor((now() - state.startedAt) / 1000),
    nodes: { state: 'ok', nodes: state.identities.length, up: state.identities.length, problems: [], version: '2.8.0.47db0e3', launcher: 'meshtasticd' },
    radio: { driver: 'kiss', device: '/dev/ttyUSB0', firmware: 'Mesh KISS v2', name: 'Heltec V3', connected: true, reconnects: 0,
      rx: state.counters.rx, tx: state.counters.tx, errors: 2, noise_floor_dbm: state.noise },
    phy,
    relay: { node_id: relay.node_id, node_num: relay.node_num, long_name: relay.long_name, short_name: relay.short_name, role: state.relayRole },
    airtime: { window_s: 3600, tx_ms: txMs, rx_ms: 402_000, duty_limit_pct: state.config.airtime.duty_cycle_percent,
      tx_pct: round((txMs / 3_600_000) * 100, 1), channel_util_pct: round(rnd(9.5, 13), 1) },
    counters: { ...state.counters },
  }
}

const WINDOW: Record<StatsWindow, [number, number]> = { '1h': [60, 60], '24h': [600, 144], '7d': [3600, 168] }
const diurnal = (t: number) => { const h = new Date(t).getHours() + new Date(t).getMinutes() / 60; return 0.55 + 0.45 * Math.sin(((h - 8) / 24) * 2 * Math.PI) }

export function airtimeStats(w: StatsWindow): AirtimeStats {
  const [bucket, n] = WINDOW[w] ?? WINDOW['24h']
  const end = Math.floor(now() / (bucket * 1000)) * bucket * 1000
  const shares = [['!a1c40e07', 0.022], ['!5b9e2213', 0.008], ['!7d21e4a9', 0.9]] as const
  const buckets = Array.from({ length: n }, (_, i) => {
    const time = end - (n - 1 - i) * bucket * 1000
    const load = (w === '1h' ? 1 : diurnal(time) * 1.6) * rnd(0.55, 1.45)
    const ms = bucket * 1000
    const tx = ms * 0.041 * load * (w === '1h' && i > 40 && i < 48 ? 1.7 : 1)
    const relay = tx * 0.066 * rnd(0.5, 1.6)
    const by: Record<string, number> = {}
    for (const [id, s] of shares) by[id] = Math.round(tx * s * rnd(0.6, 1.4))
    return { time, tx_ms: Math.round(relay + Object.values(by).reduce((a, b) => a + b, 0)), rx_ms: Math.round(ms * 0.112 * load * rnd(0.8, 1.2)), relay_ms: Math.round(relay), by_identity: by }
  })
  return { bucket_s: bucket, buckets }
}

export function rfStats(w: StatsWindow): RfStats {
  const [bucket, n] = WINDOW[w] ?? WINDOW['1h']
  const end = Math.floor(now() / (bucket * 1000)) * bucket * 1000
  let nf = -118
  return {
    bucket_s: bucket,
    points: Array.from({ length: n }, (_, i) => {
      const time = end - (n - 1 - i) * bucket * 1000
      nf = Math.max(-124, Math.min(-108, nf + rnd(-1.2, 1.2) + (chance(0.03) ? 6 : 0)))
      const load = w === '1h' ? rnd(0.5, 0.8) : diurnal(time)
      return { time, noise_floor_dbm: Math.round(nf), channel_util_pct: round(8 + 7 * load * rnd(0.7, 1.3), 1),
        rx: Math.round(bucket / 3.2 * load * rnd(0.6, 1.4)), tx: Math.round(bucket / 11 * load * rnd(0.6, 1.4)) }
    }),
  }
}

export function portStats(w: StatsWindow): PortStat[] {
  const scale = w === '1h' ? 1 : w === '24h' ? 20 : 130
  const base: [string, number, number][] = [
    ['POSITION_APP', 118, 6], ['TELEMETRY_APP', 96, 31], ['NODEINFO_APP', 64, 14], ['TEXT_MESSAGE_APP', 41, 12],
    ['ROUTING_APP', 37, 18], ['NEIGHBORINFO_APP', 22, 0], ['TRACEROUTE_APP', 7, 3], ['STORE_FORWARD_APP', 3, 0],
  ]
  return base.map(([port, rx, tx]) => ({ port, rx: Math.round(rx * scale * rnd(0.85, 1.15)), tx: Math.round(tx * scale * rnd(0.85, 1.15)) }))
}

export function identityStats(w: StatsWindow): IdentityStat[] {
  const scale = w === '1h' ? 1 : w === '24h' ? 22 : 150
  return state.identities.map((i) => {
    const tx = Math.round((i.is_relay ? 14 : i.short_name === 'WX' ? 60 : i.enabled ? 6 : 0) * scale)
    const fail = Math.round(tx * (i.short_name === 'OPS' ? 0.12 : 0.04) * 0.3)
    return { node_id: i.node_id, tx, rx: Math.round(160 * scale * (i.enabled ? 1 : 0)), ack_ok: Math.round(tx * 0.3) - fail, ack_fail: fail, airtime_ms: Math.round(i.airtime_ms_1h * scale * rnd(0.8, 1.1)) }
  })
}

// ---------------------------------------------------------------- config, tokens, links

state.config = {
  radio: { type: 'kiss', port: '/dev/serial/by-id/usb-Silicon_Labs_CP2102_USB_to_UART_Bridge_Controller_0001-if00-port0', region: 'EU_868', preset: 'LONG_FAST', primary_channel: '', tx_power_dbm: 27, frequency_offset_mhz: 0, baud: 115200, hop_limit: 3, channel_num: 0, override_frequency_mhz: 0 },
  relay: { role: 'client', rebroadcast: 'all', favorites: [], long_name: 'RepeaterTastic Relay', short_name: 'RPTR', local_dm: 'software' },
  airtime: { duty_cycle_percent: 10, identity_share_percent: 25, nodeinfo_interval: '3h', telemetry_interval: 'off', override_duty_cycle: false, cw_min: 3, cw_max: 8 },
  web: { bind: '0.0.0.0', port: 8080, session_ttl: '24h', map_tile_url: '', map_key_source: 'built in', mdns: true, log_level: 'info' },
  position: { latitude: 55.9533, longitude: -3.1883, altitude: 47, precision_bits: 32, interval: '3h', identities: 'relay' },
  hardware: { hw_model: 'AUTO', effective: 'HELTEC_V3', modem: 'Heltec V3' },
  mqtt: [
    {
      key: 'mqtt', name: 'mqtt', enabled: false, address: 'mqtt.meshtastic.org:1883', username: 'meshdev', password: '', password_set: true,
      clear_password: false, tls: false, root: 'msh/EU_868/Scotland', mode: 'gateway', gateway: 'relay', format: 'encrypted',
      uplink_channels: [], downlink_channels: [], channel_selection: 'identity', ignore_consent: false, ok_to_mqtt: false,
      relay_mqtt: false, relay_hops: 0, cross_link: false, bridge_acknowledged: false, downlink_per_minute: 30, uplink_per_minute: 120,
      map_report: { enabled: false, interval: '1h', position_precision: 14, latitude: 0, longitude: 0 },
    },
  ],
  radio_id: 'main',
  main: true,
}

state.tokens = [
  { id: 'tok_h4ss', name: 'Home Assistant', created_at: now() - 12 * 24 * HOUR, last_used: now() - 4 * 60_000, secret: 'rt_' + randomBytes(18).toString('hex') },
  { id: 'tok_graf', name: 'Prometheus scrape', created_at: now() - 5 * 24 * HOUR, last_used: now() - 30_000, secret: 'rt_' + randomBytes(18).toString('hex') },
]

state.links = [
  { name: 'udp', type: 'udp_multicast', enabled: true, connected: true, rx: 5821, tx: 1377, detail: '239.0.0.69:4403 on eth0' },
  { name: 'glasgow-site', type: 'host_link', enabled: true, connected: false, rx: 0, tx: 0, detail: '81.2.69.160:4410 (WireGuard)' },
  { name: 'mqtt:mqtt', connection: 'mqtt', type: 'mqtt', enabled: true, connected: true, rx: 214, tx: 1630, dropped: 3, detail: 'mqtt.meshtastic.org:1883 · msh/EU_868/Scotland',
    root: 'msh/EU_868/Scotland', mode: 'gateway', format: 'encrypted', gateway: 'relay', gateway_id: '!be77562b', uplink: ['LongFast'], downlink: [],
    ok_to_mqtt: true, relay_mqtt: false, cross_link: false, map_report: true },
]

export function newToken() {
  return 'rt_' + randomBytes(18).toString('hex')
}

// ---------------------------------------------------------------- live generator

export function startGenerator(emit: Emit) {
  const tick = () => {
    const p = randomRx()
    pushPacket(p, emit)
    packetLog(p, emit)
    if (p.port === 'TEXT_MESSAGE_APP' && p.kind === 'delivered') {
      for (const i of state.identities) if (i.enabled || i.is_relay) {
        addMessage(i.node_id, msg({ from: p.from, to: BROADCAST, text: (p.payload as { text: string }).text, direction: 'in', id: p.id }), emit)
        emit('identity', i)
      }
    }
    if (chance(0.06)) {
      const wx = identityById('!7d21e4a9')!
      const text = WX()
      const out = msg({ from: wx.node_id, to: BROADCAST, text, direction: 'out' })
      addMessage(wx.node_id, out, emit)
      pushPacket(makePacket({ from: wx.node_id, port: 'TEXT_MESSAGE_APP', payload: { text }, summary: text, kind: 'ours', direction: 'tx', decodedBy: wx.node_id }), emit)
      wx.airtime_ms_1h += 610
      recomputeShares()
      emit('identity', wx)
    }
    if (chance(0.04)) log(pick(['debug', 'debug', 'info', 'warn'] as LogLevel[]), pick([
      `scheduler: tx queue depth ${rint(0, 3)}, cw ${rint(3, 8)}, slot ${rint(160, 230)} ms`,
      `airtime: rolling 1h tx ${status().airtime.tx_pct}% of ${state.config.airtime.duty_cycle_percent}% budget`,
      'relay: cancelled rebroadcast, heard 2 copies during contention window',
      `api :4403 heartbeat from 192.168.1.42 (Meshtastic Android)`,
      `link udp: rx ${rint(1, 9)} packets from meshtasticd 192.168.1.${rint(20, 90)}`,
    ]), emit)
    if (chance(0.01)) log('warn', 'airtime: Weather Bot is using more than its 25% share of the duty-cycle budget', emit)
    setTimeout(tick, rint(1000, 3000))
  }
  setTimeout(tick, 1200)
  setInterval(() => {
    state.noise = Math.round(Math.max(-124, Math.min(-109, state.noise + rnd(-1.5, 1.5))))
    emit('status', status())
  }, 5000)
}
