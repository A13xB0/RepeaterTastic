# MQTT

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Web GUI](web-gui.md) · [Several radios](radios.md) · [Architecture](architecture.md)

`links.mqtt` is a list of broker connections per radio (a single mapping, the old form, still
loads as a one-item list). Each connection has its own name, broker, root topic, gateway identity
(`gateway: relay` or a node id), channels and rate limits, and uses the firmware's topics and
payloads: encrypted ServiceEnvelopes on `<root>/2/e/<channel>/<!gateway>`, JSON on
`<root>/2/json/<channel>/<!gateway>` and map reports on `<root>/2/map/`.

| `mode` | Uplink | Downlink | Notes |
| --- | --- | --- | --- |
| `gateway` (default) | yes | yes | Respects OK_TO_MQTT; JSON only for channels anyone can read; broker traffic rebroadcast at most zero-hop |
| `uplink_only` | yes | no | |
| `map_only` | no | no | Map reports only |
| `monitor` | yes | no | JSON by default, for dashboards and loggers |
| `bridge` | yes | yes | Needs `bridge_acknowledged: true`. May set `ignore_consent`, publish JSON of private channels and `relay_hops` above 0. For joining your own sites over a broker you control |

- **Channels:** `channel_selection: identity` (default without lists) follows the identity channel
  uplink/downlink switches; `override` (default with lists) carries only `uplink_channels` /
  `downlink_channels`; `combine` carries both.
- **Format:** `encrypted` (default), `json` or `both`.
- **Relay:** a connection's broker packets reach our identities but only go on air when that
  connection has `relay_mqtt`. `ok_to_mqtt` on any connection sets OK_TO_MQTT on our packets.
- **Between connections:** packets from one connection go out on another only when both set
  `cross_link`. The duplicate filter stops a packet looping back in.
- **Limits:** `uplink_per_minute` (default 120) and `downlink_per_minute` (default 30).
- **Map reports:** optional per connection, with the position coarsened to `position_precision` bits.

Recommended for a node that's on the air: a `gateway` on the default channel only, downlink off
(or on one gateway per mesh), no private channels, and `relay_mqtt` off.

## In the GUI

**Configuration → MQTT** lists the radio's connections. Each has its mode (with a description of
each), broker, root topic, credentials, gateway identity, channel selection, payload format, rate
limits, relaying, passing traffic between connections, and its map report. Choosing **bridge** asks
for confirmation. The **Links** page shows each connection's state, gateway, uplink and downlink
channels and counters.

Connection changes apply after a restart; the restart banner offers it.

## Example: the public Scotland root

```yaml
links:
    mqtt:
        - name: public
          enabled: true
          address: mqtt.meshtastic.org:1883
          username: meshdev
          password: large4cats
          root: msh/EU_868/Scotland
          ok_to_mqtt: true
          relay_mqtt: false          # nothing from the broker goes on air
          map_report:
              enabled: true
              interval: 1h
```

With several radios, each radio has its own `links.mqtt`. A packet heard on two radios is published
once per broker and root.
