// Types for the RepeaterTastic HTTP API — see docs/api.md (including "Proposed additions").
// Shared by the SPA and the dev mock, so keep this file free of runtime imports.

export type RelayRole = 'client' | 'router' | 'mute'
export type ChannelRole = 'PRIMARY' | 'SECONDARY' | 'DISABLED'
export type PacketKind = 'ours' | 'relayed' | 'dup' | 'undecryptable' | 'delivered' | 'local'
export type MessageStatus = 'queued' | 'sent' | 'acked' | 'failed' | 'received'
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'
export type StatsWindow = '1h' | '24h' | '7d'

export interface Phy {
  region: string
  preset: string
  preset_name: string
  frequency_mhz: number
  bw_khz: number
  sf: number
  cr: number
  slot: number
  num_slots: number
  sync_word: number
  preamble: number
  tx_power_dbm: number
  primary_channel: string
}

export interface RadioSummary {
  id: string
  name: string
  main: boolean
  device: string
  driver: string
  firmware: string
  connected: boolean
  configured: boolean
  noise_floor_dbm: number
  phy: Status['phy']
  relay: { role: string; node_id?: string; long_name?: string }
  identities: number
  tx_pct: number
  channel_util_pct: number
  overlaps: string[] | null
}

export interface RadiosResponse {
  radios: RadioSummary[]
  site: { radios: number; duty_limit_pct: number; tx_pct: number } | null
}

export interface Status {
  version: string
  radio_id?: string
  radio_name?: string
  map?: { tile_url: string }
  uptime_s: number
  radio: {
    driver: string
    device: string
    firmware: string
    name: string
    connected: boolean
    reconnects: number
    rx: number
    tx: number
    errors: number
    noise_floor_dbm: number
  }
  phy: Phy
  relay: { node_id: string; node_num: number; long_name: string; short_name: string; role: RelayRole }
  airtime: {
    window_s: number
    tx_ms: number
    rx_ms: number
    duty_limit_pct: number
    tx_pct: number
    channel_util_pct: number
  }
  counters: {
    rx: number
    rx_dupe: number
    rx_undecryptable: number
    tx: number
    relayed: number
    relay_cancelled: number
    ack_ok: number
    ack_fail: number
    dropped_duty: number
  }
}

export interface Channel {
  index: number
  role: ChannelRole
  name: string
  display_name: string
  psk: string
  hash: number
  uplink: boolean
  downlink: boolean
  locked: boolean
}

export interface Identity {
  node_id: string
  node_num: number
  long_name: string
  short_name: string
  role: string
  hw_model: string
  public_key: string
  is_relay: boolean
  enabled: boolean
  api: { bind: string; port: number; clients: number } | null
  outbox: number
  airtime_ms_1h: number
  share_pct: number
  /** Proposed: configured slice of the hourly duty budget (percent of the budget). */
  share_limit_pct?: number
  /** Cap on the hop limit of packets this identity sends; 0 = the radio's hop limit. */
  hop_limit?: number
  /** Proposed: unread browser-chat messages across all conversations. */
  unread?: number
  created_at: number
  channels: Channel[]
}

export interface KeyPreview {
  private_key: string
  public_key: string
  node_id: string
  node_num: number
  last_byte: number
  collision: string | null
}

export interface Conversation {
  key: string
  title: string
  last_text: string
  last_time: number
  unread: number
}

export interface Message {
  id: number
  from: string
  to: string
  channel: number
  text: string
  time: number
  direction: 'in' | 'out'
  status: MessageStatus
  error: string
  pki: boolean
  rssi: number | null
  snr: number | null
  hops: number | null
}

export interface MeshNode {
  node_id: string
  node_num: number
  long_name: string
  short_name: string
  hw_model: string
  role: string
  has_public_key: boolean
  last_heard: number
  snr: number | null
  rssi: number | null
  hops_away: number | null
  via_mqtt: boolean
  local: boolean
  next_hop: number | null
  position: { lat: number; lon: number; alt: number; time: number } | null
  telemetry: { battery: number; voltage: number; channel_util: number; air_util_tx: number } | null
  /** Proposed: local identities whose NodeDB holds this node. */
  known_by?: string[]
}

export interface Packet {
  seq: number
  time: number
  direction: 'rx' | 'tx'
  kind: PacketKind
  id: number
  from: string
  to: string
  channel_hash: number
  channel: string
  port: string
  hop_limit: number
  hop_start: number
  want_ack: boolean
  via_mqtt: boolean
  next_hop: number
  relay_node: number
  rssi: number | null
  snr: number | null
  size: number
  airtime_ms: number
  decoded_by: string
  pki: boolean
  summary: string
  payload: unknown
  raw: string
}

export interface TracerouteEvent {
  identity: string
  target: string
  route: string[]
  snr_towards: number[]
  route_back: string[]
  snr_back: number[]
  /** Proposed: set when no reply arrived. */
  error?: string
}

export interface LogLine {
  time: number
  level: LogLevel
  msg: string
}

export interface AirtimeBucket {
  time: number
  tx_ms: number
  rx_ms: number
  relay_ms: number
  by_identity: Record<string, number>
}

export interface AirtimeStats {
  bucket_s: number
  buckets: AirtimeBucket[]
}

export interface PortStat {
  port: string
  rx: number
  tx: number
}

/** Proposed: GET /stats/rf */
export interface RfStats {
  bucket_s: number
  points: { time: number; noise_floor_dbm: number; channel_util_pct: number; rx: number; tx: number }[]
}

/** Proposed: GET /stats/identities */
export interface IdentityStat {
  node_id: string
  tx: number
  rx: number
  ack_ok: number
  ack_fail: number
  airtime_ms: number
}

export interface SerialPort {
  path: string
  description: string
}

export interface Region {
  name: string
  presets: string[]
  duty_cycle_pct: number
  power_limit_dbm: number
}

/** Proposed: POST /setup/probe */
export interface ProbeResult {
  ok: boolean
  driver: string
  firmware: string
  name: string
  sync_word_ok: boolean
  error: string
}

export interface ApiToken {
  id: string
  name: string
  created_at: number
  last_used: number | null
  token?: string
}

export interface Link {
  name: string
  type: string
  enabled: boolean
  connected: boolean
  rx: number
  tx: number
  /** Proposed: human-readable endpoint, e.g. "239.0.0.69:4403". */
  detail?: string
  dropped?: number
  // MQTT only
  broker?: string
  root?: string
  tls?: boolean
  downlink?: string[] | null
  ok_to_mqtt?: boolean
  relay_mqtt?: boolean
  map_report?: boolean
}

/** Proposed: effective config shape for GET/PUT /config (mirrors the YAML file). */
export interface Config {
  radio: {
    type: string
    port: string
    region: string
    preset: string
    primary_channel: string
    tx_power_dbm: number
    frequency_offset_mhz: number
  }
  relay: { role: RelayRole; long_name: string; short_name: string; local_dm: 'software' | 'also_rf' }
  airtime: {
    duty_cycle_percent: number
    identity_share_percent: number
    nodeinfo_interval: string
    position: 'off' | 'fixed'
    telemetry: 'off' | 'device'
    cw_min: number
    cw_max: number
  }
  web: { bind: string; port: number; session_ttl: string }
}

export interface ConfigPutResult {
  config: Config
  restart_required: boolean
}
