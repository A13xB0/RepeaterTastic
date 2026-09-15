![ScotMesh Meshtastic](https://raw.githubusercontent.com/ScotMesh/branding/main/networks/meshtastic/readme-header.png)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/repeatertastic-lockup-dark.png">
    <img src="docs/images/repeatertastic-lockup-light.png" width="430" alt="RepeaterTastic: virtual Meshtastic nodes">
  </picture>
</p>

# RepeaterTastic

Host many virtual Meshtastic nodes in software, all sharing one LoRa modem: the Meshtastic
equivalent of openHop Repeater's virtual companions. Built and run by
[ScotMesh](https://github.com/ScotMesh) for Scottish mesh sites, and useful anywhere.

- **One Go binary** (about 10 MB, static, no CGO) for a Raspberry Pi or any Linux box, or one container. The web GUI is embedded.
- **Radio:** a Mesh KISS modem (MeshCore KISS firmware patched to speak Meshtastic's PHY: sync word 0x2B and a 16-symbol preamble) on a Heltec V3, XIAO nRF52840 + Wio-SX1262, RAK4631, T-Beam, … See [`firmware/`](firmware/).
- **Each virtual node is a full Meshtastic node:** its own X25519 key, a node number of `crc32(public_key)`, channels, and a client-API port. The official apps, the Python CLI and the web client connect to each node as if it were a radio.
- **Shared per radio:** one relay persona (the only identity that repeats), packet history, a next-hop table and the duty-cycle budget. DMs between identities on the host are delivered locally.
- **Several radios on one host** (LongFast, MediumFast, …), **MQTT** connections to one or more brokers, fixed **position** broadcasts, and an optional **UDP multicast** link to `meshtasticd` on the LAN.

> **Status:** running on a ScotMesh site on a Heltec V3, alongside Reticulum. Still young:
> expect changes. Start with [`docs/bench-test.md`](docs/bench-test.md) for a new setup.
> Verified: byte-exact against 25 golden vectors from meshtasticd 2.7.26 and 2.8.0; PKI DMs with
> ACKs against real meshtasticd; the official apps and Python CLI on the identity ports; on-air
> channel messages and DMs; multi-radio routing in simulation.

## Quick start

You need a Mesh KISS modem on USB ([`firmware/`](firmware/) builds and flashes it).

### Docker

```bash
docker run -d --name repeatertastic --restart unless-stopped \
  --network host \
  --device /dev/serial/by-id/usb-…-if00-port0:/dev/ttyUSB0 \
  --group-add "$(getent group dialout | cut -d: -f3)" \
  -e REPEATERTASTIC_RADIO_DEVICE=/dev/ttyUSB0 \
  -v repeatertastic-data:/data \
  ghcr.io/a13xb0/repeatertastic:latest
```

Open `http://<host>:8080` and set the admin password; the setup wizard finds the modem and writes
the config to the volume. The volume holds the config, identity keys and chats, so back it up.
For a compose file copy [`deploy/docker-compose.example.yml`](deploy/docker-compose.example.yml).

- **Host networking** is what lets the apps discover identities over mDNS and joins the UDP
  multicast LAN mesh. Without it, publish `8080` and the identity ports (`4403` upwards) instead.
- **Environment:** `REPEATERTASTIC_CONFIG` (default `/data/repeatertastic.yaml`),
  `REPEATERTASTIC_STATE_DIR` (`/data`), `REPEATERTASTIC_RADIO_DEVICE`, `REPEATERTASTIC_WEB_PORT`,
  `REPEATERTASTIC_MAP_API_KEY`.
- **Build it yourself:** `docker build -t repeatertastic .` (add
  `--secret id=map_api_key,env=CARTO_API_KEY` to bake in a map key).

### systemd

```bash
make build                                   # bin/repeatertastic, bin/kisstool
make dist                                    # static binaries for Pi (arm64/armv7/armv6) + amd64
sudo ./deploy/install.sh dist/repeatertastic-linux-arm64 dist/kisstool-linux-arm64
# open http://<pi>:8080 and set the admin password
```

Release binaries are on the [releases page](https://github.com/A13xB0/RepeaterTastic/releases).
The configuration reference is [`deploy/repeatertastic.example.yaml`](deploy/repeatertastic.example.yaml);
almost everything in it can also be set in the web GUI.

## Using the web GUI

- **Identities:** create, import, edit and move virtual nodes, each with its own app port. The
  relay persona is created for you.
- **Chat:** channel conversations and DMs for any identity, live.
- **Channels:** every identity's eight slots. Add, edit and remove channels (one dialog), or add a
  channel to several identities at once.
- **Nodes & map, Packets, Statistics, Logs:** what the radio hears and sends.
- **Configuration:** Radios (add, edit, remove; site airtime cap), Relay, Airtime & duty, Position &
  hardware, MQTT, Web & API tokens, Experimental, Backup & restore. A banner lists saved changes
  that need a restart.

## Map tiles

The Nodes & map page uses CARTO basemaps (OpenStreetMap data). Release builds carry a default
CARTO API key baked in at build time from the `CARTO_API_KEY` repository secret. To use your own,
set `REPEATERTASTIC_MAP_API_KEY` in the service's environment. A different tile server can be set
with `web.map_tile_url`, where `{api_key}` is replaced by the key. The key is visible to browsers
in tile requests, so restrict it on the provider's side.

## Several radios on one host

Add radios under `radios:` in the config: each gets its own modem, preset, relay persona,
identities, node DB and airtime budget, so one host can serve LongFast and MediumFast side by side.
The top-level radio stays the **main** radio and existing configs keep working unchanged.

- **Overlapping channels:** radios whose channels overlap in frequency take turns to transmit. In
  EU_868, LongFast, MediumFast, MediumSlow and ShortFast all sit on 869.525 MHz.
- **Site airtime cap:** `site.duty_cycle_percent` caps the summed airtime of all radios.
- **Web GUI:** Configuration → Radios adds, renames and removes radios; a radio switcher
  appears once there is more than one. Configuration shows a restart banner when a change
  (a new radio, MQTT, the web port) waits for a restart.
- **Identities:** each lives on one radio. Move one from its editor: key, node ID, app port and
  chats go with it, and its primary channel follows the new preset. A key can't be imported onto a
  second radio.
- **API:** `?radio=<id>` selects a radio (see [`docs/api.md`](docs/api.md)).
- **EU_868 notes:** LongTurbo's 500 kHz doesn't fit the 250 kHz sub-band, and LongSlow sits on 869.4625 MHz.

### Experimental: identities on several radios

Off by default (Configuration → Experimental, or `experimental.multi_radio_identities`). When on:

- **One radio per channel slot.** In the Channels page's slot dialog, each slot gets one radio.
  Scotland on LongFast and Scotland on MediumFast are two slots, two channels, two chats (the
  Meshtastic app shows both as "Scotland", in slot order).
- **Default radio per identity** (identity editor, home unless changed): slot 0 is its primary
  channel, new channels and channels changed from the app start on it, and DMs fall back to it.
- **DMs:** the radio where the destination was last heard best in the last day, always the default
  radio, or a fixed radio, with an optional one-time retry on another radio.
- An identity is on its home radio, its default radio and each radio one of its slots uses. Other
  radios treat it as a guest: they hear and send its slots there and deliver into its home chats.
- **Safety:** a packet heard on two radios is delivered once, relays never rebroadcast the site's own
  identities, MQTT publishes a packet once, and turning the switch off puts everything back on the
  home radio at once (the choices are kept).

## Position and hardware

- **`position:`** gives a radio a fixed site location. The relay persona (or every identity, with
  `identities: all`) broadcasts it on a timer and answers position requests, and apps connected
  to those identities see it.
- **Hardware:** identities advertise the modem's real board by default (`mesh.hw_model: auto`, e.g.
  Heltec V3 → `HELTEC_V3`). The firmware version reads `2.8.1.rptrtst`, so apps can still tell
  it's RepeaterTastic.

## MQTT

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

Connection changes apply after a daemon restart (Configuration → MQTT has a Restart button).

## How it fits together

```
LoRa modem ──USB/KISS── radio driver ── receive pipeline ── mesh host ──┬── identity "Base Camp" ── :4403 (apps, CLI, web client)
                                         (dedupe, decrypt,   (relay,     ├── identity "Ops Desk"  ── :4404
                                          deliver)            retries,   ├── relay persona
                                                              airtime)   └── UDP multicast link (meshtasticd LAN mesh)
                                                   web GUI + REST/SSE API :8080
```

- **Receive:**
  - Each frame is checked against the shared (from, id) history.
  - DMs addressed to one of our identities are PKI-decrypted with that identity's key.
  - Everything else is tried against every channel whose hash matches.
  - The result goes to every identity that holds that channel, at the same time.
- **Transmit:**
  - Every identity's packets go through one queue.
  - The queue uses Meshtastic's contention window, a channel-busy check before each transmission, and the region duty cycle.
- **Relay:** only the relay persona rebroadcasts, following the firmware's flooding and next-hop rules. N identities never relay the same packet N times.

The plan and research are in [`docs/plan.html`](docs/plan.html), written under the working name
"Hopstatic". The HTTP API is documented in [`docs/api.md`](docs/api.md). Coding agents and new
contributors: read [`AGENTS.md`](AGENTS.md) first.

## Layout

```
cmd/repeatertastic     daemon
cmd/kisstool           modem bench tool: info, listen, send-text
internal/wire          16-byte header, AES-CTR channels, X25519 + AES-CCM DMs, AEAD channels
internal/phy           regions, presets, frequency slots, airtime, contention window
internal/mesh          host: receive, relay, reliable delivery, identities, node DB, airtime
internal/phoneapi      Meshtastic client API: TCP stream + HTTP, config handshake, local admin
internal/radio         radio interface; kiss (serial), sim (tests), null
internal/links/udp     meshtasticd UDP multicast link
internal/links/mqtt    Meshtastic MQTT connections (gateway, monitor, bridge, map reports)
internal/site          several radios on one host: shared transmit turns and airtime cap
internal/config        YAML config, validation, environment overrides
internal/web           REST/SSE API, auth, embedded GUI
internal/pb            generated protobufs (scripts/gen-proto.sh; vendored in proto/)
ui/                    web GUI (Vue 3 + Vite), built into internal/web/dist
firmware/              KISS modem patch, board list, build script
tests/interop          meshtasticd Docker harness + golden vectors
deploy/                systemd unit, example config, install script, docker-compose example
Dockerfile             distroless container image
```

## Development

```bash
make test race          # unit tests, simulated multi-host mesh tests, golden vectors
make ui                 # rebuild the GUI into internal/web/dist (commit the result)
cd ui && npm run dev    # GUI against a mock API
RT_TEST_MQTT_BROKER=127.0.0.1:1883 go test ./internal/links/mqtt   # MQTT against a real broker
cd tests/interop && ./run_meshtasticd.sh up 2 && ./run_repeatertastic.sh up   # real firmware interop
```

## Licence

GPL-3.0-or-later. The Meshtastic protobufs are GPL-3.0. Parts of the web GUI are adapted from
openHop Repeater UI (MIT, © Lloyd Newton).
