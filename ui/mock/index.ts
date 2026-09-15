// Vite dev-server middleware implementing docs/api.md against an in-memory world (mock/data.ts).
// Login password is "meshtastic". Start with the setup wizard: `npm run dev:setup`.
import type { IncomingMessage, ServerResponse } from 'node:http'
import { randomBytes } from 'node:crypto'
import type { Plugin } from 'vite'
import type { Channel, Config, Identity, KeyPreview, StatsWindow } from '../src/api/types.ts'
import {
  airtimeStats, channelHash, channelSlots, conversations, heard, identityById, identityStats, log, markRead, messages, newToken,
  portStats, rfStats, sendMessage, startGenerator, state, status, traceroute,
} from './data.ts'
import { regionList, resolvePhy } from './phy.ts'

type Res = ServerResponse
const clients = new Set<Res>()

function emit(event: string, data: unknown) {
  const frame = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
  for (const c of clients) c.write(frame)
}

function json(res: Res, code: number, body?: unknown) {
  res.statusCode = code
  if (body === undefined) return res.end()
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}
const fail = (res: Res, code: number, error: string) => json(res, code, { error })

async function readBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = []
  for await (const c of req) chunks.push(c as Buffer)
  const s = Buffer.concat(chunks).toString()
  if (!s) return {}
  try { return JSON.parse(s) } catch { return {} }
}

function authed(req: IncomingMessage, url: URL): boolean {
  const h = req.headers.authorization
  const tok = h?.startsWith('Bearer ') ? h.slice(7) : url.searchParams.get('token')
  return !!tok && (state.sessions.has(tok) || state.tokens.some((t) => t.secret === tok))
}

function session() {
  const t = `mock.${randomBytes(12).toString('base64url')}.${Date.now().toString(36)}`
  state.sessions.add(t)
  return t
}

function deepMerge<T>(target: T, patch: unknown): T {
  if (patch && typeof patch === 'object' && !Array.isArray(patch)) {
    for (const [k, v] of Object.entries(patch)) {
      const cur = (target as Record<string, unknown>)[k]
      if (cur && typeof cur === 'object' && !Array.isArray(cur)) deepMerge(cur, v)
      else (target as Record<string, unknown>)[k] = v
    }
  }
  return target
}

function syncNode(i: Identity) {
  const n = state.nodes.get(i.node_id)
  if (n) Object.assign(n, { long_name: i.long_name, short_name: i.short_name, role: i.role })
}

function preview(privateKey?: string): KeyPreview {
  const priv = privateKey || randomBytes(32).toString('base64')
  const seed = Buffer.from(priv, 'base64')
  // Deterministic stand-in for crc32(x25519 public key): hash the key bytes.
  let h = 0x811c9dc5
  for (const b of seed) h = Math.imul(h ^ b, 0x01000193) >>> 0
  const pub = Buffer.alloc(32)
  for (let i = 0; i < 32; i++) pub[i] = (Math.imul(h, i + 7) >>> (i % 24)) & 0xff
  const num = h >>> 0
  const last = num & 0xff
  const clash = [...state.nodes.values()].find((n) => (n.node_num & 0xff) === last)
  return { private_key: priv, public_key: pub.toString('base64'), node_id: '!' + num.toString(16).padStart(8, '0'), node_num: num, last_byte: last, collision: clash?.node_id ?? null }
}

const PUBLIC = new Set(['GET /setup', 'POST /setup', 'POST /auth/login'])
const SETUP_OPEN = new Set(['GET /serial-ports', 'GET /regions', 'POST /phy/preview', 'POST /setup/probe'])

async function handle(req: IncomingMessage, res: Res) {
  const url = new URL(req.url ?? '/', 'http://mock')
  const path = url.pathname.replace(/\/+$/, '') || '/'
  const method = req.method ?? 'GET'
  const route = `${method} ${path}`
  await new Promise((r) => setTimeout(r, 40 + Math.random() * 120))

  if (!PUBLIC.has(route) && !(state.setupNeeded && SETUP_OPEN.has(route)) && !authed(req, url)) return fail(res, 401, 'unauthorized')

  const body = method === 'GET' || method === 'DELETE' ? {} : await readBody(req)
  const win = (url.searchParams.get('window') ?? '24h') as StatsWindow
  let m: RegExpMatchArray | null

  switch (route) {
    case 'GET /setup': return json(res, 200, { needed: state.setupNeeded })
    case 'POST /setup': {
      if (!state.setupNeeded) return fail(res, 409, 'setup already completed')
      if (String(body.password ?? '').length < 8) return fail(res, 400, 'password must be at least 8 characters')
      state.password = String(body.password)
      state.config.radio.region = String(body.region ?? 'EU_868')
      state.config.radio.preset = String(body.preset ?? 'LONG_FAST')
      if (body.device) state.config.radio.port = String(body.device)
      state.relayRole = (body.relay_role as typeof state.relayRole) ?? 'client'
      state.setupNeeded = false
      log('info', 'setup: admin account created', emit)
      return json(res, 200, { token: session() })
    }
    case 'POST /setup/probe': {
      const dev = String(body.device ?? '')
      await new Promise((r) => setTimeout(r, 900))
      if (dev.includes('ttyAMA')) return json(res, 200, { ok: false, driver: '', firmware: '', name: '', sync_word_ok: false, error: 'no KISS response within 2 s (is this the Pi UART console?)' })
      return json(res, 200, { ok: true, driver: 'kiss', firmware: 'MeshCore KISS v2 (RepeaterTastic patch)', name: dev.includes('ACM') ? 'RAK4631' : 'Heltec V3', sync_word_ok: true, error: '' })
    }
    case 'POST /auth/login':
      if (body.password !== state.password) return fail(res, 401, 'wrong password')
      return json(res, 200, { token: session(), expires: Date.now() + 24 * 3600_000 })
    case 'PUT /auth/password':
      if (body.current !== state.password) return fail(res, 400, 'current password is wrong')
      if (String(body.new ?? '').length < 8) return fail(res, 400, 'new password must be at least 8 characters')
      state.password = String(body.new)
      return json(res, 204)
    case 'GET /events': {
      res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' })
      res.write(`retry: 3000\n\nevent: status\ndata: ${JSON.stringify(status())}\n\n`)
      clients.add(res)
      const ping = setInterval(() => res.write(': ping\n\n'), 20_000)
      req.on('close', () => { clearInterval(ping); clients.delete(res) })
      return
    }
    case 'GET /status': return json(res, 200, status())
    case 'PUT /relay': {
      const role = body.role as typeof state.relayRole
      if (!['client', 'router', 'mute'].includes(role)) return fail(res, 400, 'role must be client, router or mute')
      state.relayRole = role
      state.config.relay.role = role
      log('info', `relay: role set to ${role}`, emit)
      const s = status()
      emit('status', s)
      return json(res, 200, s.relay)
    }
    case 'GET /identities': return json(res, 200, state.identities)
    case 'POST /identities/preview-key': return json(res, 200, preview(body.private_key as string | undefined))
    case 'POST /identities': {
      const long = String(body.long_name ?? '').trim()
      if (!long) return fail(res, 400, 'long_name is required')
      const pv = preview(body.private_key as string | undefined)
      if (identityById(pv.node_id)) return fail(res, 409, `identity ${pv.node_id} already exists`)
      const port = Number(body.api_port) || 4403 + state.identities.length
      if (state.identities.some((i) => i.api?.port === port)) return fail(res, 409, `port ${port} is already used`)
      const ident: Identity = {
        node_id: pv.node_id, node_num: pv.node_num, long_name: long, short_name: String(body.short_name ?? long.slice(0, 4)).slice(0, 4),
        role: String(body.role ?? 'CLIENT'), hw_model: 'PORTDUINO', public_key: pv.public_key, is_relay: false, enabled: true,
        api: { bind: '0.0.0.0', port, clients: 0 }, outbox: 0, airtime_ms_1h: 0, share_pct: 0, share_limit_pct: state.config.airtime.identity_share_percent,
        unread: 0, created_at: Date.now(), channels: channelSlots(),
      }
      state.identities.push(ident)
      state.nodes.set(ident.node_id, { node_id: ident.node_id, node_num: ident.node_num, long_name: long, short_name: ident.short_name, hw_model: 'PORTDUINO',
        role: ident.role, has_public_key: true, last_heard: Date.now(), snr: null, rssi: null, hops_away: 0, via_mqtt: false, local: true, next_hop: null,
        position: null, telemetry: null, known_by: state.identities.map((i) => i.node_id) })
      log('info', `identity: created ${long} ${ident.node_id} api 0.0.0.0:${port}`, emit)
      emit('identity', ident)
      return json(res, 201, ident)
    }
    case 'GET /nodes': return json(res, 200, [...state.nodes.values()])
    case 'GET /packets': {
      const q = url.searchParams
      const limit = Math.min(Number(q.get('limit') ?? 100), 1000)
      const before = Number(q.get('before') ?? Infinity)
      const node = q.get('node'), port = q.get('port'), kind = q.get('kind'), dir = q.get('direction'), ch = q.get('channel'), text = q.get('q')?.toLowerCase()
      const since = Number(q.get('since') ?? 0)
      const out = state.packets.filter((p) => p.time < before && p.time >= since && (!node || p.from === node || p.to === node) && (!port || p.port === port)
        && (!kind || p.kind === kind) && (!dir || p.direction === dir) && (!ch || p.channel === ch) && (!text || p.summary.toLowerCase().includes(text)))
      return json(res, 200, out.slice(0, limit))
    }
    case 'GET /stats/airtime': return json(res, 200, airtimeStats(win))
    case 'GET /stats/ports': return json(res, 200, portStats(win))
    case 'GET /stats/rf': return json(res, 200, rfStats((url.searchParams.get('window') ?? '1h') as StatsWindow))
    case 'GET /stats/identities': return json(res, 200, identityStats(win))
    case 'GET /config': return json(res, 200, state.config)
    case 'PUT /config': {
      const before = JSON.stringify([state.config.radio.port, state.config.web.port, state.config.web.bind])
      deepMerge<Config>(state.config, body)
      if (body.relay && typeof body.relay === 'object') {
        const r = state.identities[0]!
        r.long_name = state.config.relay.long_name
        r.short_name = state.config.relay.short_name
        state.relayRole = state.config.relay.role
        syncNode(r)
      }
      const restart = before !== JSON.stringify([state.config.radio.port, state.config.web.port, state.config.web.bind])
      log('info', `config: updated ${Object.keys(body).join(', ')}${restart ? ' (restart required)' : ', applied live'}`, emit)
      emit('status', status())
      return json(res, 200, { config: state.config, restart_required: restart })
    }
    case 'GET /serial-ports': return json(res, 200, [
      { path: '/dev/serial/by-id/usb-Silicon_Labs_CP2102_USB_to_UART_Bridge_Controller_0001-if00-port0', description: 'CP2102 USB to UART (Heltec V3)' },
      { path: '/dev/serial/by-id/usb-RAKwireless_WisCore_RAK4631_Board_E8A1B2C3D4E5F607-if00', description: 'RAK4631 WisCore (native USB)' },
      { path: '/dev/ttyAMA0', description: 'Raspberry Pi UART' },
    ])
    case 'GET /regions': return json(res, 200, regionList())
    case 'POST /phy/preview':
      try {
        return json(res, 200, resolvePhy(String(body.region), String(body.preset), String(body.primary_channel ?? ''), Number(body.tx_power_dbm ?? 0)))
      } catch (e) { return fail(res, 400, (e as Error).message) }
    case 'GET /tokens': return json(res, 200, state.tokens.map(({ secret: _s, ...t }) => t))
    case 'POST /tokens': {
      const name = String(body.name ?? '').trim()
      if (!name) return fail(res, 400, 'name is required')
      const t = { id: 'tok_' + randomBytes(3).toString('hex'), name, created_at: Date.now(), last_used: null, secret: newToken() }
      state.tokens.push(t)
      return json(res, 201, { id: t.id, name, token: t.secret, created_at: t.created_at, last_used: null })
    }
    case 'GET /backup':
      res.setHeader('Content-Disposition', `attachment; filename="repeatertastic-backup-${new Date().toISOString().slice(0, 10)}.json"`)
      return json(res, 200, { version: 1, created: Date.now(), config: state.config, identities: state.identities.map((i) => ({ ...i, private_key: randomBytes(32).toString('base64') })) })
    case 'POST /restore':
      if (!body.config) return fail(res, 400, 'not a RepeaterTastic backup (missing config)')
      log('warn', 'restore: backup applied, restart required', emit)
      return json(res, 200, { restart_required: true })
    case 'GET /logs': {
      const limit = Number(url.searchParams.get('limit') ?? 500)
      return json(res, 200, state.logs.slice(-limit))
    }
    case 'GET /links': return json(res, 200, state.links)
  }

  if ((m = path.match(/^\/tokens\/([\w-]+)$/)) && method === 'DELETE') {
    state.tokens = state.tokens.filter((t) => t.id !== m![1])
    return json(res, 204)
  }
  if ((m = path.match(/^\/links\/([\w-]+)$/)) && method === 'PATCH') {
    const l = state.links.find((x) => x.name === m![1])
    if (!l) return fail(res, 404, 'no such link')
    if (typeof body.enabled === 'boolean') {
      l.enabled = body.enabled
      l.connected = body.enabled && l.type === 'udp_multicast'
    }
    return json(res, 200, l)
  }
  if ((m = path.match(/^\/nodes\/(![0-9a-f]{8})(?:\/(traceroute|request-nodeinfo))?$/))) {
    const [, id, action] = m
    const node = state.nodes.get(id!)
    if (!node) return fail(res, 404, `unknown node ${id}`)
    if (method === 'DELETE' && !action) {
      if (node.local) return fail(res, 409, 'local identities cannot be removed from the node DB')
      state.nodes.delete(id!)
      return json(res, 204)
    }
    const from = String(body.from ?? '')
    if (!identityById(from)) return fail(res, 400, 'from must be a local identity')
    if (action === 'traceroute') { traceroute(from, id!, emit); return json(res, 202, {}) }
    if (action === 'request-nodeinfo') {
      log('info', `nodeinfo: ${from} requested NodeInfo from ${node.short_name}`, emit)
      setTimeout(() => { node.last_heard = Date.now(); emit('node', node) }, 3000)
      return json(res, 202, {})
    }
  }
  if ((m = path.match(/^\/identities\/(![0-9a-f]{8})(\/.*)?$/))) {
    const id = m[1]!
    const sub = m[2] ?? ''
    const ident = identityById(id)
    if (!ident) return fail(res, 404, `unknown identity ${id}`)
    let mm: RegExpMatchArray | null
    if (!sub && method === 'PATCH') {
      if (body.api_port !== undefined && ident.api) {
        const port = Number(body.api_port)
        if (state.identities.some((i) => i !== ident && i.api?.port === port)) return fail(res, 409, `port ${port} is already used by another identity`)
        ident.api.port = port
      }
      for (const k of ['long_name', 'short_name', 'enabled', 'role', 'share_limit_pct'] as const)
        if (body[k] !== undefined) (ident as unknown as Record<string, unknown>)[k] = body[k]
      if (ident.api && body.enabled === false) ident.api.clients = 0
      syncNode(ident)
      emit('identity', ident)
      return json(res, 200, ident)
    }
    if (!sub && method === 'DELETE') {
      if (ident.is_relay) return fail(res, 409, 'the relay persona cannot be deleted')
      state.identities = state.identities.filter((i) => i !== ident)
      state.nodes.delete(id)
      log('warn', `identity: deleted ${ident.long_name} ${id}`, emit)
      return json(res, 204)
    }
    if (sub === '/key' && method === 'GET') return json(res, 200, { private_key: randomBytes(32).toString('base64'), public_key: ident.public_key })
    if (sub === '/api/restart' && method === 'POST') {
      if (ident.api) ident.api.clients = 0
      log('info', `api :${ident.api?.port} restarted for ${ident.long_name}`, emit)
      emit('identity', ident)
      return json(res, 204)
    }
    if (sub === '/conversations' && method === 'GET') return json(res, 200, conversations(id))
    if ((mm = sub.match(/^\/conversations\/(.+)\/read$/)) && method === 'POST') {
      markRead(id, decodeURIComponent(mm[1]!), emit)
      return json(res, 204)
    }
    if (sub === '/messages' && method === 'GET') {
      const q = url.searchParams
      return json(res, 200, messages(id, q.get('conversation') ?? 'ch:0', Number(q.get('before') ?? Infinity), Number(q.get('limit') ?? 50)))
    }
    if (sub === '/messages' && method === 'POST') {
      const text = String(body.text ?? '')
      if (!text.trim()) return fail(res, 400, 'text is required')
      if (Buffer.byteLength(text) > 200) return fail(res, 400, 'text is longer than 200 bytes')
      if (!ident.enabled) return fail(res, 409, `${ident.long_name} is disabled`)
      return json(res, 202, sendMessage(id, { to: String(body.to ?? '!ffffffff'), channel: Number(body.channel ?? 0), text, want_ack: body.want_ack !== false }, emit))
    }
    if (sub === '/channels/url' && method === 'GET') {
      const names = ident.channels.filter((c) => c.role !== 'DISABLED').map((c) => c.display_name).join(',')
      return json(res, 200, { url: `https://meshtastic.org/e/#${Buffer.from(`${names}|${ident.node_id}`).toString('base64url')}CgcSAQE6AggN` })
    }
    if (sub === '/channels/url' && method === 'POST') {
      const u = String(body.url ?? '')
      if (!/^https:\/\/meshtastic\.org\/e\/#?.+/.test(u)) return fail(res, 400, 'not a meshtastic.org/e/# channel URL')
      const free = ident.channels.find((c) => c.index > 0 && c.role === 'DISABLED')
      if (!free) return fail(res, 409, 'no free channel slots')
      const name = 'Imported' + free.index
      Object.assign(free, { role: 'SECONDARY', name, display_name: name, psk: randomBytes(32).toString('base64') })
      free.hash = channelHash(name, free.psk)
      emit('identity', ident)
      return json(res, 200, ident)
    }
    if ((mm = sub.match(/^\/channels\/(\d)$/)) && method === 'PUT') {
      const idx = Number(mm[1])
      const ch = ident.channels[idx]
      if (!ch) return fail(res, 404, 'channel index must be 0-7')
      if (ch.locked && (body.name !== undefined && body.name !== ch.name || body.role !== undefined && body.role !== ch.role))
        return fail(res, 409, 'channel 0 is the shared primary channel; its name and role come from the radio config')
      const next: Channel = { ...ch, ...(body as Partial<Channel>), index: idx, locked: ch.locked }
      next.display_name = next.name || (idx === 0 ? 'LongFast' : '')
      next.hash = next.role === 'DISABLED' ? 0 : channelHash(next.display_name, next.psk)
      ident.channels[idx] = next
      emit('identity', ident)
      return json(res, 200, ident)
    }
  }
  return fail(res, 404, `no mock for ${route}`)
}

export function mockApi(): Plugin {
  return {
    name: 'repeatertastic-mock-api',
    apply: 'serve',
    configureServer(server) {
      startGenerator(emit)
      server.middlewares.use('/api/v1', (req, res) => {
        handle(req, res).catch((e) => fail(res, 500, String(e)))
      })
      server.config.logger.info(`  mock API: /api/v1 (${heard().length} heard nodes, password "meshtastic"${state.setupNeeded ? ', setup wizard' : ''})`)
    },
  }
}
