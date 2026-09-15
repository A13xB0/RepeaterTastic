// Small reactive store fed by REST snapshots plus the SSE stream (/api/v1/events).
import { markRaw, reactive, shallowRef, triggerRef } from 'vue'
import { API_BASE, api, token } from '@/api/client'
import type { Identity, LogLine, MeshNode, Message, Packet, RfStats, Status, TracerouteEvent } from '@/api/types'

const PACKET_BUFFER = 400
const LOG_BUFFER = 1500
const HISTORY = 90 // status samples (5 s apart) ≈ 7.5 min

export interface StatusSample {
  time: number
  noise: number
  txPct: number
  chUtil: number
  rx: number
  tx: number
  dupe: number
  relayed: number
  undecryptable: number
  ackOk: number
  ackFail: number
}

export const live = reactive({
  connected: false,
  status: null as Status | null,
  identities: [] as Identity[],
  nodes: {} as Record<string, MeshNode>,
  history: [] as StatusSample[],
  /** Noise-floor points from /stats/rf so sparklines aren't empty right after login. */
  noiseSeed: [] as number[],
  lastEvent: 0,
})

/** Newest first. Shallow so hundreds of packets don't become deep proxies. */
export const packets = shallowRef<Packet[]>([])
export const logs = shallowRef<LogLine[]>([])

type Handler<T> = (data: T) => void
const listeners = {
  packet: new Set<Handler<Packet>>(),
  message: new Set<Handler<{ identity: string; message: Message }>>(),
  traceroute: new Set<Handler<TracerouteEvent>>(),
  log: new Set<Handler<LogLine>>(),
}
type Events = typeof listeners

export function on<K extends keyof Events>(event: K, fn: Events[K] extends Set<infer H> ? H : never): () => void {
  const set = listeners[event] as Set<typeof fn>
  set.add(fn)
  return () => set.delete(fn)
}

function sample(s: Status) {
  live.history.push({
    time: Date.now(), noise: s.radio.noise_floor_dbm, txPct: s.airtime.tx_pct, chUtil: s.airtime.channel_util_pct,
    rx: s.counters.rx, tx: s.counters.tx, dupe: s.counters.rx_dupe, relayed: s.counters.relayed,
    undecryptable: s.counters.rx_undecryptable, ackOk: s.counters.ack_ok, ackFail: s.counters.ack_fail,
  })
  if (live.history.length > HISTORY) live.history.splice(0, live.history.length - HISTORY)
}

export function setStatus(s: Status) {
  live.status = s
  sample(s)
}

export function upsertIdentity(i: Identity) {
  const idx = live.identities.findIndex((x) => x.node_id === i.node_id)
  if (idx >= 0) live.identities[idx] = i
  else live.identities.push(i)
}

export function removeIdentity(id: string) {
  live.identities = live.identities.filter((i) => i.node_id !== id)
}

export async function refreshStatus() {
  setStatus(await api.get<Status>('/status'))
}
export async function refreshIdentities() {
  live.identities = await api.get<Identity[]>('/identities')
}
// Servers before the NodeInfo-defaults fix left names out for nodes heard without a
// NodeInfo; views sort and filter on them, so fill the firmware's placeholders here too.
export function normalizeNode(n: MeshNode): MeshNode {
  const short = n.node_id.slice(-4)
  n.long_name ??= `Meshtastic ${short}`
  n.short_name ??= short
  n.hw_model ??= 'UNSET'
  n.role ??= 'CLIENT'
  return n
}
export async function refreshNodes() {
  const list = await api.get<MeshNode[]>('/nodes')
  const map: Record<string, MeshNode> = {}
  for (const n of list) map[n.node_id] = normalizeNode(n)
  live.nodes = map
}
export async function refreshPackets() {
  const list = await api.get<Packet[]>('/packets?limit=100')
  packets.value = list.map((p) => markRaw(p))
}
export async function refreshLogs() {
  logs.value = await api.get<LogLine[]>('/logs?limit=500')
}

let source: EventSource | null = null
let retryTimer: number | undefined

function parse<T>(e: MessageEvent): T | null {
  try {
    return JSON.parse(e.data) as T
  } catch {
    return null
  }
}

export function connect() {
  disconnect()
  if (!token.value) return
  const es = new EventSource(`${API_BASE}/events?token=${encodeURIComponent(token.value)}`)
  source = es
  es.onopen = () => (live.connected = true)
  es.onerror = () => {
    live.connected = false
    if (es.readyState === EventSource.CLOSED) {
      // Browser gave up (e.g. 401 or daemon restart): probe status (triggers login on 401) and retry.
      clearTimeout(retryTimer)
      retryTimer = window.setTimeout(() => {
        refreshStatus().then(connect, () => token.value && connect())
      }, 4000)
    }
  }
  es.addEventListener('status', (e) => {
    const s = parse<Status>(e as MessageEvent)
    if (s) setStatus(s)
  })
  es.addEventListener('packet', (e) => {
    const p = parse<Packet>(e as MessageEvent)
    if (!p) return
    live.lastEvent = Date.now()
    const next = [markRaw(p), ...packets.value]
    if (next.length > PACKET_BUFFER) next.length = PACKET_BUFFER
    packets.value = next
    listeners.packet.forEach((fn) => fn(p))
  })
  es.addEventListener('identity', (e) => {
    const i = parse<Identity>(e as MessageEvent)
    if (i) upsertIdentity(i)
  })
  es.addEventListener('node', (e) => {
    const n = parse<MeshNode>(e as MessageEvent)
    if (n) live.nodes[n.node_id] = normalizeNode(n)
  })
  es.addEventListener('message', (e) => {
    const m = parse<{ identity: string; message: Message }>(e as MessageEvent)
    if (m) listeners.message.forEach((fn) => fn(m))
  })
  es.addEventListener('traceroute', (e) => {
    const t = parse<TracerouteEvent>(e as MessageEvent)
    if (t) listeners.traceroute.forEach((fn) => fn(t))
  })
  es.addEventListener('log', (e) => {
    const l = parse<LogLine>(e as MessageEvent)
    if (!l) return
    const arr = logs.value
    arr.push(l)
    if (arr.length > LOG_BUFFER) arr.splice(0, arr.length - LOG_BUFFER)
    triggerRef(logs)
    listeners.log.forEach((fn) => fn(l))
  })
}

export function disconnect() {
  clearTimeout(retryTimer)
  source?.close()
  source = null
  live.connected = false
}

let started = false
export async function startLive() {
  if (started) return
  started = true
  connect()
  await Promise.allSettled([
    refreshStatus(), refreshIdentities(), refreshNodes(), refreshPackets(), refreshLogs(),
    api.get<RfStats>('/stats/rf?window=1h').then((r) => (live.noiseSeed = r.points.slice(-40).map((p) => p.noise_floor_dbm))),
  ])
}

export function stopLive() {
  started = false
  disconnect()
  live.status = null
  live.identities = []
  live.nodes = {}
  live.history = []
  live.noiseSeed = []
  packets.value = []
  logs.value = []
}

// ---------------------------------------------------------------- lookups

export function nodeLabel(id: string): { short: string; long: string; local: boolean } {
  if (id === '!ffffffff') return { short: 'ALL', long: 'Broadcast', local: false }
  const n = live.nodes[id]
  if (n) return { short: n.short_name, long: n.long_name, local: n.local }
  const i = live.identities.find((x) => x.node_id === id)
  if (i) return { short: i.short_name, long: i.long_name, local: true }
  return { short: id.slice(-4), long: id, local: false }
}

/** Resolve a last-byte (next_hop / relay_node) to a known node, if unambiguous. */
export function nodeByLastByte(b: number): MeshNode | undefined {
  const hits = Object.values(live.nodes).filter((n) => (n.node_num & 0xff) === b)
  return hits.length === 1 ? hits[0] : undefined
}
