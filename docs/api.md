# RepeaterTastic HTTP API (v1)

JSON over HTTP, served by the daemon (default `:8080`). The web GUI is embedded at `/`. All endpoints
are under `/api/v1`. Times are Unix **milliseconds** unless noted. Node ids are strings `"!a1c40e07"`;
`node_num` is the same value as a number.

## Auth

- `GET  /api/v1/setup` → `{"needed": true}` when no admin account exists yet.
- `POST /api/v1/setup` `{"password": "...", "region": "EU_868", "preset": "LONG_FAST", "device": "/dev/ttyUSB0", "relay_role": "client"}` → `{"token": "..."}`
- `POST /api/v1/auth/login` `{"password": "..."}` → `{"token": "...", "expires": 1757980000000}`
- Every other endpoint needs `Authorization: Bearer <token>` (JWT from login, or an API token).
  401 → `{"error": "unauthorized"}`. Errors always look like `{"error": "human readable message"}`.
- SSE (`/api/v1/events`) accepts the token as `?token=` because EventSource can't set headers.

## Status

`GET /api/v1/status`

```json
{
  "version": "0.1.0", "uptime_s": 5234,
  "radio": {"driver": "kiss", "device": "/dev/ttyUSB0", "firmware": "MeshCore KISS v2", "name": "Heltec V3",
            "connected": true, "reconnects": 0, "rx": 1203, "tx": 311, "errors": 2, "noise_floor_dbm": -118},
  "phy": {"region": "EU_868", "preset": "LONG_FAST", "preset_name": "LongFast", "frequency_mhz": 869.525,
          "bw_khz": 250, "sf": 11, "cr": 5, "slot": 0, "num_slots": 1, "sync_word": 43, "preamble": 16,
          "tx_power_dbm": 27, "primary_channel": "LongFast"},
  "relay": {"node_id": "!3f0a91c2", "node_num": 1057657282, "long_name": "RepeaterTastic Relay", "short_name": "RPTR",
            "role": "client"},
  "map": {"tile_url": "https://tile.openstreetmap.org/{z}/{x}/{y}.png"},
  "airtime": {"window_s": 3600, "tx_ms": 147700, "rx_ms": 402000, "duty_limit_pct": 10, "tx_pct": 4.1,
              "channel_util_pct": 11.2},
  "counters": {"rx": 1203, "rx_dupe": 402, "rx_undecryptable": 77, "tx": 311, "relayed": 120,
               "relay_cancelled": 33, "ack_ok": 41, "ack_fail": 3, "dropped_duty": 0}
}
```

`PUT /api/v1/relay` `{"role": "client" | "router" | "mute"}` → status.relay

## Radios

A host can run several radios (see `radios:` in the example config). Every endpoint works on the
**main** radio by default; add `?radio=<id>` to use another. Endpoints under
`/identities/{node_id}/…` find the identity's radio by themselves. `POST /identities` also takes
`"radio_id"`. Status carries `radio_id`, `radio_name` and `site`.

`GET /api/v1/radios` →

```json
{"radios": [{"id": "main", "name": "Main", "main": true, "device": "/dev/ttyUSB0", "driver": "kiss",
             "firmware": "MeshCore KISS v2", "connected": true, "configured": true, "noise_floor_dbm": -111,
             "phy": {"…": "as status.phy"}, "relay": {"role": "mute", "node_id": "!be77562b", "long_name": "Relay"},
             "identities": 3, "tx_pct": 0.1, "channel_util_pct": 6.2, "overlaps": ["mf"]}],
 "site": {"radios": 2, "duty_limit_pct": 0, "tx_pct": 0.3}}
```

`overlaps` lists radios whose channel overlaps this one's; overlapping radios take turns to
transmit. `site` is `null` for a single radio without a site airtime cap. `PUT /relay?radio=<id>`
changes an extra radio's relay role. The configuration endpoints still describe the main radio;
extra radios are configured in the YAML file.

New identities default to `role: CLIENT_MUTE`: only each radio's relay persona repeats.

## Identities

`GET /api/v1/identities` → `[Identity]`

```json
{
  "node_id": "!a1c40e07", "node_num": 2713980423, "long_name": "Base Camp", "short_name": "BASE",
  "role": "CLIENT", "hw_model": "PORTDUINO", "public_key": "base64…", "is_relay": false, "enabled": true,
  "api": {"bind": "0.0.0.0", "port": 4403, "clients": 1},
  "outbox": 0, "airtime_ms_1h": 3200, "share_pct": 2.2, "created_at": 1757900000000,
  "channels": [
    {"index": 0, "role": "PRIMARY", "name": "", "display_name": "LongFast", "psk": "AQ==", "hash": 8,
     "uplink": false, "downlink": false, "locked": true}
  ]
}
```

The relay persona is included with `"is_relay": true` and `"api": null`.

- `POST /api/v1/identities` `{"long_name": "...", "short_name": "...", "private_key": "base64 (optional)", "api_port": 4404}` → Identity (201)
- `POST /api/v1/identities/preview-key` `{"private_key": "base64 (optional)"}` → `{"private_key", "public_key", "node_id", "node_num", "last_byte": 7, "collision": null | "!xxxxxx07"}` — generate a key and check its last byte against local and heard nodes, before creating
- `PATCH /api/v1/identities/{node_id}` `{"long_name"?, "short_name"?, "enabled"?, "api_port"?}` → Identity
- `DELETE /api/v1/identities/{node_id}` → 204
- `GET /api/v1/identities/{node_id}/key` → `{"private_key": "base64", "public_key": "base64"}`
- `PUT /api/v1/identities/{node_id}/channels/{index}` `{"name", "psk" (base64), "role": "PRIMARY|SECONDARY|DISABLED", "uplink", "downlink"}` → Identity. Index 0 name/role is locked (409 with an explanation).
- `GET /api/v1/identities/{node_id}/channels/url` → `{"url": "https://meshtastic.org/e/#…"}`
- `POST /api/v1/identities/{node_id}/channels/url` `{"url": "..."}` → Identity (imports secondary channels only)

## Messages (browser chat)

- `GET /api/v1/identities/{node_id}/conversations` → `[{"key": "ch:0" | "dm:!5b9e2213", "title": "LongFast" | "Ops Desk", "last_text": "…", "last_time": 0, "unread": 2}]`
  Every enabled channel of the identity is listed, with `last_time: 0` and an empty `last_text` until something is said on it, so a new identity can post in its channels straight away.
- `GET /api/v1/identities/{node_id}/messages?conversation=ch:0&before=<ms>&limit=50` → `[Message]`
- `POST /api/v1/identities/{node_id}/messages` `{"to": "!ffffffff", "channel": 0, "text": "hello", "want_ack": true}` → Message (202)

```json
{"id": 195939341, "from": "!a1c40e07", "to": "!ffffffff", "channel": 0, "text": "hello", "time": 1757900000000,
 "direction": "out", "status": "queued|sent|acked|failed|received", "error": "", "pki": false,
 "rssi": -92, "snr": 6.5, "hops": 1}
```

## Nodes (shared node DB)

`GET /api/v1/nodes` → `[Node]`

A node heard without a NodeInfo gets the firmware's placeholders (`"long_name": "Meshtastic 77f6"`, `"short_name": "77f6"`, `"hw_model": "UNSET"`, `"role": "CLIENT"`) and `"has_user": false`, so every node always carries every field.

```json
{"node_id": "!5b9e2213", "node_num": 1537090067, "long_name": "Hilltop", "short_name": "HILL", "hw_model": "HELTEC_V3",
 "role": "ROUTER", "has_public_key": true, "last_heard": 1757900000000, "snr": 7.25, "rssi": -88, "hops_away": 0,
 "via_mqtt": false, "local": false, "next_hop": null,
 "position": {"lat": 55.95, "lon": -3.19, "alt": 120, "time": 1757900000000} ,
 "telemetry": {"battery": 87, "voltage": 4.05, "channel_util": 12.3, "air_util_tx": 1.2}}
```

- `POST /api/v1/nodes/{node_id}/traceroute` `{"from": "!a1c40e07"}` → 202; result arrives as SSE `traceroute`
- `POST /api/v1/nodes/{node_id}/request-nodeinfo` `{"from": "!a1c40e07"}` → 202
- `DELETE /api/v1/nodes/{node_id}` → 204

## Packets

`GET /api/v1/packets?limit=100&before=<ms>&node=!xxxx&port=TEXT_MESSAGE_APP&kind=relayed` → `[Packet]` newest first

```json
{"seq": 88123, "time": 1757900000000, "direction": "rx|tx", "kind": "ours|relayed|dup|undecryptable|delivered|local",
 "id": 195939341, "from": "!5b9e2213", "to": "!ffffffff", "channel_hash": 8, "channel": "LongFast",
 "port": "TEXT_MESSAGE_APP", "hop_limit": 2, "hop_start": 3, "want_ack": false, "via_mqtt": false,
 "next_hop": 0, "relay_node": 19, "rssi": -91, "snr": 5.75, "size": 42, "airtime_ms": 612,
 "decoded_by": "!a1c40e07", "pki": false, "summary": "hello world", "payload": {"text": "hello world"},
 "raw": "hex of the full frame"}
```

## Live events (SSE)

`GET /api/v1/events?token=…` — `text/event-stream`, events:

- `packet` → Packet
- `status` → Status (every 5 s)
- `identity` → Identity (on change)
- `message` → `{"identity": "!a1c40e07", "message": Message}` (new or status change)
- `node` → Node (on change)
- `traceroute` → `{"identity", "target", "route": ["!…"], "snr_towards": [6.5], "route_back": [...], "snr_back": [...]}`
- `log` → `{"time", "level": "info|warn|error|debug", "msg"}`

## Statistics

`GET /api/v1/stats/airtime?window=24h` (`1h`, `24h`, `7d`) → `{"bucket_s": 600, "buckets": [{"time", "tx_ms", "rx_ms", "relay_ms", "by_identity": {"!a1c40e07": 120}}]}`

`GET /api/v1/stats/ports?window=24h` → `[{"port": "TEXT_MESSAGE_APP", "rx": 120, "tx": 30}]`

## Configuration

- `GET /api/v1/config` → effective config (secrets redacted)
- `PUT /api/v1/config` (partial) → `{"config": …, "restart_required": false}`
- `GET /api/v1/serial-ports` → `[{"path": "/dev/serial/by-id/usb-Silicon_Labs_CP2102…", "description": "CP2102"}]`
- `GET /api/v1/regions` → `[{"name": "EU_868", "presets": ["LONG_FAST", …], "duty_cycle_pct": 10, "power_limit_dbm": 27}]`
- `POST /api/v1/phy/preview` `{"region", "preset", "primary_channel"}` → status.phy shape
- `GET /api/v1/tokens`, `POST /api/v1/tokens {"name"}` → `{"id","name","token"}` (token shown once), `DELETE /api/v1/tokens/{id}`
- `GET /api/v1/backup` → JSON file download (config + keys); `POST /api/v1/restore`
- `GET /api/v1/logs?limit=500` → `[{"time","level","msg"}]`
- `GET /api/v1/links[?radio=<id>]` → the UDP link and the MQTT link (`{"name": "mqtt", "enabled", "connected", "broker", "root", "tls", "rx", "tx", "dropped", "downlink": ["LongFast"], "ok_to_mqtt", "relay_mqtt", "map_report"}`), then the legacy shape:
- `GET /api/v1/links` (legacy shape) → `[{"name": "udp", "type": "udp_multicast", "enabled": false, "connected": false, "rx": 0, "tx": 0}]`

## Proposed additions (from the web GUI)

> Added while building `ui/`. The GUI already calls these and the dev mock (`ui/mock/`) implements them.
> Everything above stays as is; these are additive. Types live in `ui/src/api/types.ts`.

### Setup and auth

- While `GET /setup` reports `needed: true`, these work **without a token** so the wizard can run:
  `GET /serial-ports`, `GET /regions`, `POST /phy/preview`, `POST /setup/probe`.
- `POST /api/v1/setup/probe` `{"device": "/dev/ttyUSB0"}` → `{"ok": true, "driver": "kiss", "firmware": "MeshCore KISS v2", "name": "Heltec V3", "sync_word_ok": true, "error": ""}` — ping the modem, read its version and check it accepts sync word 0x2B. Always 200; `ok: false` + `error` when nothing answers.
- `POST /phy/preview` also accepts `"tx_power_dbm"` (clamped to the region limit in the reply).
- `PUT /api/v1/auth/password` `{"current": "...", "new": "..."}` → 204 (400 with an error when `current` is wrong or `new` < 8 chars).

### Identities

- Identity gains `"share_limit_pct": 25` (the slice of the hourly duty budget this identity may use; the GUI shows
  "Over share" when `airtime_ms_1h / (duty_limit_pct% × window)` exceeds it) and `"unread": 3` (browser-chat unread
  messages across its conversations).
- `POST /identities` also accepts `"role"` (`CLIENT`, `CLIENT_MUTE`, `CLIENT_HIDDEN`, `TRACKER`, `SENSOR`).
  The GUI sends the `private_key` returned by `preview-key` so the node id matches the preview.
- `PATCH /identities/{node_id}` also accepts `"role"` and `"share_limit_pct"`. 409 when `api_port` is taken.
- `POST /api/v1/identities/{node_id}/api/restart` → 204 — restart that identity's client-API server (drops apps).
- `POST /api/v1/identities/{node_id}/conversations/{key}/read` → 204 — clear unread for one conversation
  (`key` URL-encoded, e.g. `dm%3A!5b9e2213`). Emits SSE `identity` with the new `unread`.
- `DELETE /identities/{node_id}` on the relay persona → 409.

### Nodes

- Node gains `"known_by": ["!a1c40e07", …]` — local identities whose NodeDB holds this node.
- `DELETE /nodes/{node_id}` on a local identity → 409.
- SSE `traceroute` may carry `"error": "no response within 60 s"` with empty routes when the request timed out.

### Packets

- `GET /packets` also filters by `direction=rx|tx`, `channel=<name>`, `since=<ms>` and `q=<text>` (substring of `summary`).
  `before` paginates on `time`.

### Statistics

- `GET /api/v1/stats/rf?window=1h|24h|7d` → `{"bucket_s": 60, "points": [{"time", "noise_floor_dbm": -118, "channel_util_pct": 11.2, "rx": 19, "tx": 5}]}` — noise-floor and channel-utilisation history (dashboard sparkline, statistics charts). Same bucket sizes as `stats/airtime` (1h → 60 s, 24h → 600 s, 7d → 3600 s).
- `GET /api/v1/stats/identities?window=24h` → `[{"node_id", "tx": 132, "rx": 3520, "ack_ok": 38, "ack_fail": 2, "airtime_ms": 60000}]` — per-identity packets, ACK success and airtime for the window (relay persona included).
- `stats/airtime` buckets: `tx_ms` is the total of `relay_ms` + all `by_identity` values.

### Configuration

- Shape of `GET /config` / partial `PUT /config` (mirrors the YAML file; secrets never included):

  ```json
  {
    "radio": {"type": "kiss", "port": "/dev/serial/by-id/usb-…", "region": "EU_868", "preset": "LONG_FAST",
              "primary_channel": "", "tx_power_dbm": 27, "frequency_offset_mhz": 0},
    "relay": {"role": "client", "long_name": "RepeaterTastic Relay", "short_name": "RPTR", "local_dm": "software|also_rf"},
    "airtime": {"duty_cycle_percent": 10, "identity_share_percent": 25, "nodeinfo_interval": "3h",
                "position": "off|fixed", "telemetry": "off|device", "cw_min": 3, "cw_max": 8},
    "web": {"bind": "0.0.0.0", "port": 8080, "session_ttl": "24h"}
  }
  ```

  `PUT` takes any subset of sections (the GUI sends one section at a time) and replies `{"config", "restart_required"}`.
  Changing `relay.role` is equivalent to `PUT /relay`.
- `GET /tokens` → `[{"id", "name", "created_at", "last_used": null | ms}]`; `POST /tokens` replies 201 with the same fields plus `token`.
- `GET /backup` sets `Content-Disposition: attachment; filename="repeatertastic-backup-YYYY-MM-DD.json"`.
- `POST /restore` body is the backup JSON as downloaded → `{"restart_required": true}`; 400 when it isn't a backup.
- `PATCH /api/v1/links/{name}` `{"enabled": true}` → Link. Link gains `"detail": "239.0.0.69:4403 on eth0"` (human-readable endpoint).
