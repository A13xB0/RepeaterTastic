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
  "airtime": {"window_s": 3600, "tx_ms": 147700, "rx_ms": 402000, "duty_limit_pct": 10, "tx_pct": 4.1,
              "channel_util_pct": 11.2},
  "counters": {"rx": 1203, "rx_dupe": 402, "rx_undecryptable": 77, "tx": 311, "relayed": 120,
               "relay_cancelled": 33, "ack_ok": 41, "ack_fail": 3, "dropped_duty": 0}
}
```

`PUT /api/v1/relay` `{"role": "client" | "router" | "mute"}` → status.relay

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
- `GET /api/v1/identities/{node_id}/messages?conversation=ch:0&before=<ms>&limit=50` → `[Message]`
- `POST /api/v1/identities/{node_id}/messages` `{"to": "!ffffffff", "channel": 0, "text": "hello", "want_ack": true}` → Message (202)

```json
{"id": 195939341, "from": "!a1c40e07", "to": "!ffffffff", "channel": 0, "text": "hello", "time": 1757900000000,
 "direction": "out", "status": "queued|sent|acked|failed|received", "error": "", "pki": false,
 "rssi": -92, "snr": 6.5, "hops": 1}
```

## Nodes (shared node DB)

`GET /api/v1/nodes` → `[Node]`

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
- `GET /api/v1/links` → `[{"name": "udp", "type": "udp_multicast", "enabled": false, "connected": false, "rx": 0, "tx": 0}]`
