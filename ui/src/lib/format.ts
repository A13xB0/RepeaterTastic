export const BROADCAST = '!ffffffff'

export function relTime(t: number, now = Date.now()): string {
  if (!t) return 'never'
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 5) return 'just now'
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m ago`
  const d = Math.floor(h / 24)
  return `${d}d ${h % 24}h ago`
}

export function clock(t: number, seconds = true): string {
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: seconds ? '2-digit' : undefined, hour12: false })
}

export function dateTime(t: number): string {
  return new Date(t).toLocaleString([], { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}

export function dayLabel(t: number): string {
  const d = new Date(t)
  const today = new Date()
  const y = new Date(Date.now() - 86_400_000)
  if (d.toDateString() === today.toDateString()) return 'Today'
  if (d.toDateString() === y.toDateString()) return 'Yesterday'
  return d.toLocaleDateString([], { weekday: 'long', day: 'numeric', month: 'long' })
}

export function uptime(s: number): string {
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d) return `${d}d ${h}h`
  if (h) return `${h}h ${m}m`
  return `${m}m ${s % 60}s`
}

export function compact(n: number): string {
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (Math.abs(n) >= 1e4) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'K'
  return n.toLocaleString()
}

/** A measured number with at most `digits` decimals and no float noise (968.7040000000001 → "968.7"). */
export function num(v: number | null | undefined, digits = 1): string {
  if (v == null || !Number.isFinite(v)) return '—'
  return v.toLocaleString(undefined, { maximumFractionDigits: digits })
}

/** Airtime of one packet: whole milliseconds below 10 s ("928 ms"), seconds above. */
export function airtime(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return '—'
  return ms < 10_000 ? `${num(ms, 0)} ms` : `${num(ms / 1000, 1)} s`
}

export function seconds(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} min`
  return `${(ms / 3_600_000).toFixed(1)} h`
}

export function hex(n: number, width = 8): string {
  return '0x' + (n >>> 0).toString(16).padStart(width, '0')
}

/** Human names for Meshtastic port numbers. */
export function portLabel(port: string): string {
  if (!port) return 'Encrypted'
  return port
    .replace(/_APP$/, '')
    .split('_')
    .map((w) => w.charAt(0) + w.slice(1).toLowerCase())
    .join(' ')
    .replace('Nodeinfo', 'NodeInfo')
    .replace('Neighborinfo', 'NeighborInfo')
}

export function roleLabel(role: string): string {
  return role
    .split('_')
    .map((w) => w.charAt(0) + w.slice(1).toLowerCase())
    .join(' ')
}

export function hwLabel(hw: string): string {
  const map: Record<string, string> = {
    HELTEC_V3: 'Heltec V3', T_ECHO: 'T-Echo', RAK4631: 'RAK4631', TBEAM: 'T-Beam', STATION_G2: 'Station G2', T_DECK: 'T-Deck',
    HELTEC_WIRELESS_TRACKER: 'Heltec Tracker', TRACKER_T1000_E: 'T1000-E', SEEED_XIAO_S3: 'XIAO S3', PORTDUINO: 'Virtual',
  }
  return map[hw] ?? hw.replace(/_/g, ' ')
}

export function utf8Len(s: string): number {
  return new TextEncoder().encode(s).length
}

/** Stable hue per node for avatar badges. */
export function nodeHue(id: string): number {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0
  return h % 360
}

export function pct(v: number, d = 1): string {
  return `${v.toFixed(d)}%`
}

export const snrClass = (snr: number | null) =>
  snr == null ? 'text-ink-3' : snr >= 5 ? 'text-ok' : snr >= -5 ? 'text-ink-2' : snr >= -12 ? 'text-warn' : 'text-bad'
