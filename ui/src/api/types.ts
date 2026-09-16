// Types for the RepeaterTastic HTTP API — see docs/api.md (including "Proposed additions").
// Shared by the SPA and the dev mock, so keep this file free of runtime imports.

/** Meshtastic device roles the relay can take, plus monitor (listen only) and off. */
export type RelayRole = 'client' | 'client_base' | 'client_mute' | 'router' | 'router_late' | 'monitor' | 'off'
/** Meshtastic rebroadcast modes ('' = all). */
export type RebroadcastMode = '' | 'all' | 'all_skip_decoding' | 'local_only' | 'known_only' | 'none' | 'core_portnums_only'
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
  /** Radios added or removed in the config that start or stop at the next restart. */
  pending: { id: string; name: string; device: string; driver?: string; region?: string; preset?: string; tx_power_dbm?: number; relay_role?: string; action: 'start' | 'remove' }[]
  restart_required: boolean
}

export interface Status {
  version: string
  radio_id?: string
  radio_name?: string
  map?: { tile_url: string }
  /** Saved changes the running daemon hasn't picked up yet. */
  restart_reasons?: string[] | null
  /** How the meshtasticd nodes are doing across the site. */
  nodes?: NodesHealth
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
  /** Runs on meshtasticd rather than in RepeaterTastic. */
  real_node?: boolean
  /** Runs on a meshtasticd RepeaterTastic starts, with the key RepeaterTastic keeps. */
  hosted?: boolean
  enabled: boolean
  /** The app port; listening is false when the port couldn't be opened. */
  api: { bind: string; port: number; clients: number; listening: boolean } | null
  outbox: number
  airtime_ms_1h: number
  share_pct: number
  /** Proposed: configured slice of the hourly duty budget (percent of the budget). */
  share_limit_pct?: number
  /** Cap on the hop limit of packets this identity sends; 0 = the radio's hop limit. */
  hop_limit?: number
  /** Own fixed position (null = uses the radio's site position). */
  position?: { latitude: number; longitude: number; altitude: number } | null
  position_secs?: number
  /** Proposed: unread browser-chat messages across all conversations. */
  unread?: number
  created_at: number
  channels: Channel[]
  api_bind?: string
  /** The radio this identity is on. */
  radio_id?: string
  radio_name?: string
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

/** GET /boards: a LoRa board the experimental spi driver can run; `id` goes in radio.device. */
export interface Board {
  host: string
  file: string
  id: string
  name: string
  module: string
  bus: string
  source: 'auto' | 'config.d' | 'available.d' | 'built-in'
  supported: boolean
  error: string
}

/** POST /setup/probe */
export interface ProbeResult {
  ok: boolean
  driver: string
  firmware: string
  name: string
  sync_word_ok: boolean
  error: string
  /** spi: the chip's diagnostic lines */
  details?: string[]
  /** meshtastic: the board's current region, preset and role */
  region?: string
  preset?: string
  role?: string
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
  uplink?: string[] | null
  connection?: string
  mode?: MqttMode
  format?: string
  gateway?: string
  gateway_id?: string
  cross_link?: boolean
  ok_to_mqtt?: boolean
  relay_mqtt?: boolean
  // UDP only
  group?: string
  map_report?: boolean
}

export type MqttMode = 'gateway' | 'uplink_only' | 'map_only' | 'monitor' | 'bridge'

/** One MQTT broker connection of a radio. */
export interface MqttConnection {
  /** The saved name (read-only); empty for a new connection. */
  key: string
  name: string
  enabled: boolean
  address: string
  username: string
  /** Write-only: empty keeps the saved password. */
  password: string
  password_set: boolean
  clear_password: boolean
  tls: boolean
  root: string
  mode: MqttMode
  /** "relay" or an identity's node id. */
  gateway: string
  format: 'encrypted' | 'json' | 'both'
  uplink_channels: string[]
  downlink_channels: string[]
  channel_selection: 'identity' | 'override' | 'combine'
  ignore_consent: boolean
  ok_to_mqtt: boolean
  relay_mqtt: boolean
  relay_hops: number
  cross_link: boolean
  bridge_acknowledged: boolean
  downlink_per_minute: number
  uplink_per_minute: number
  map_report: { enabled: boolean; interval: string; position_precision: number; latitude: number; longitude: number }
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
    baud: number
    hop_limit: number
    channel_num: number
    override_frequency_mhz: number
  }
  relay: { role: RelayRole; rebroadcast: RebroadcastMode; favorites: string[]; long_name: string; short_name: string; local_dm: 'software' | 'also_rf' }
  airtime: {
    duty_cycle_percent: number
    identity_share_percent: number
    nodeinfo_interval: string
    /** "off" or a duration such as "3h". */
    telemetry_interval: string
    override_duty_cycle: boolean
    /** Read-only: the firmware's contention window. */
    cw_min: number
    cw_max: number
  }
  web: { bind: string; port: number; session_ttl: string; map_tile_url: string; map_key_source: string; mdns: boolean; log_level: string }
  position: { latitude: number; longitude: number; altitude: number; precision_bits: number; interval: string; identities: 'relay' | 'all' }
  hardware: { hw_model: string; effective: string; modem: string }
  mqtt: MqttConnection[]
  radio_id: string
  main: boolean
}

export interface ConfigPutResult {
  config: Config
  restart_required: boolean
}

// ------------------------------------------------------------------------------------ plugins

export type PluginState =
  | 'disabled' | 'needs_review' | 'needs_settings' | 'starting' | 'running' | 'restarting' | 'crashed' | 'stopped' | 'waiting' | 'unsupported'

export interface PluginSetting {
  key: string
  label: string
  type: 'string' | 'secret' | 'url' | 'bool' | 'int' | 'number' | 'select' | 'multiselect' | 'radios' | 'identities'
  help?: string
  required?: boolean
  default?: unknown
  options?: string[]
  placeholder?: string
}

export interface Plugin {
  id: string
  name: string
  version?: string
  description?: string
  author?: string
  homepage?: string
  license?: string
  kind: 'managed' | 'attached'
  enabled: boolean
  pinned: boolean
  state: PluginState
  detail?: string
  permissions: { key: string; text: string; granted: boolean }[]
  network?: string[]
  settings: PluginSetting[]
  values: Record<string, unknown>
  secrets_set: string[]
  status?: { summary: string; state: 'ok' | 'warning' | 'error'; fields?: Record<string, string> }
  has_logo: boolean
  has_panel: boolean
  logo_url?: string
  panel_url?: string
  connected: boolean
  connected_at?: number
  started_at?: number
  restarts: number
  dropped_events: number
  installed_at: number
  source?: string
  deleted?: boolean
}

export interface PluginsResponse {
  enabled: boolean
  /** Why plugins couldn't start ("" when they did). */
  error?: string
  plugins: Plugin[]
  permissions?: Record<string, string>
  attach_address?: string
  allow_url_install?: boolean
  folder?: string
  messages_per_hour?: number
  traceroutes_per_hour?: number
  identities?: PluginIdentityChoice[]
}

export interface PluginIdentityChoice {
  node_id: string
  long_name: string
  short_name: string
  radio_id: string
  radio_name: string
  is_relay: boolean
}

export interface PluginLogLine {
  time: number
  level: 'debug' | 'info' | 'warn' | 'error'
  source: 'plugin' | 'stdout' | 'stderr' | 'host'
  message: string
}

/** A meshtasticd the daemon runs (GET /hosted). */
export interface HostedInstance {
  radio: string
  role: 'persona' | 'identity'
  name: string
  launcher: string
  port: number
  running: boolean
  connected: boolean
  restarts: number
  last_error?: string
  firmware?: string
  node_id?: string
}

/** What this machine can run hosted nodes with (GET /setup/runtimes). */
export interface Runtimes {
  meshtasticd: { found: boolean; path?: string; version?: string; ok: boolean; error?: string }
  docker: { found: boolean; ok: boolean; version?: string; error?: string; image: string; image_present: boolean }
  min_version: string
}

/** GET /status nodes: ok, starting, warning (some identities down) or error (meshtasticd isn't running). */
export interface NodesHealth {
  state: 'ok' | 'starting' | 'warning' | 'error'
  nodes: number
  up: number
  problems: string[]
  version?: string
  launcher: string
}

export interface HostedSettings {
  meshtasticd: string
  docker_image: string
  port_base: number
  min_version: string
  instances: HostedInstance[]
  restart_required: boolean
}
