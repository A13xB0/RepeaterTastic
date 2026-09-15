# RepeaterTastic

Host many virtual Meshtastic nodes in software, all sharing one dumb LoRa modem: the Meshtastic
equivalent of openHop Repeater's virtual companions.

- **One Go binary** (about 10 MB, static, no CGO) for a Raspberry Pi or any Linux box. The web GUI is embedded.
- **Radio:** a MeshCore KISS modem (Heltec V3, XIAO nRF52840 + Wio-SX1262, RAK4631, T-Beam, …), patched to speak Meshtastic's PHY: sync word 0x2B and a 16-symbol preamble. See [`firmware/`](firmware/).
- **Each virtual node is a full Meshtastic node:** its own X25519 key, a node number of `crc32(public_key)`, channels, and a client-API port. The official apps, the Python CLI and the web client connect to each node as if it were a radio.
- **Shared per radio:** one relay persona, packet history, a next-hop table, and the duty-cycle budget. DMs between identities on the host are delivered locally.
- **Optional UDP multicast link:** join the LAN mesh of native `meshtasticd` nodes, on both the 2.7 group (224.0.0.69) and the 2.8 group (239.0.0.69).

> **Status:** feature-complete for a first bench test, but **not yet tested on air**.
> Verified so far:
> - byte-exact against 25 golden vectors captured from meshtasticd 2.7.26 and 2.8.0;
> - PKI DMs with ACKs in both directions with real meshtasticd over UDP multicast;
> - the official Python CLI against the virtual node API ports;
> - multi-host relaying in simulation.
>
> Start with [`docs/bench-test.md`](docs/bench-test.md).

## Quick start

```bash
make build                                   # bin/repeatertastic, bin/kisstool
make dist                                    # static binaries for Pi (arm64/armv7/armv6) + amd64
sudo ./deploy/install.sh dist/repeatertastic-linux-arm64 dist/kisstool-linux-arm64
# open http://<pi>:8080 and set the admin password
```

The configuration reference is [`deploy/repeatertastic.example.yaml`](deploy/repeatertastic.example.yaml).

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
- **Web GUI:** a radio switcher appears once there is more than one radio.
- **API:** `?radio=<id>` selects a radio (see [`docs/api.md`](docs/api.md)).
- **EU_868 notes:** LongTurbo's 500 kHz doesn't fit the 250 kHz sub-band, and LongSlow sits on 869.4625 MHz.

## Position and hardware

- **`position:`** gives a radio a fixed site location. The relay persona (or every identity, with
  `identities: all`) broadcasts it on a timer and answers position requests, and apps connected
  to those identities see it.
- **Hardware:** identities advertise the modem's real board by default (`mesh.hw_model: auto`, e.g.
  Heltec V3 → `HELTEC_V3`). The firmware version reads `2.8.1.rptrtst`, so apps can still tell
  it's RepeaterTastic.

## MQTT

`links.mqtt` makes a radio a Meshtastic MQTT gateway (the relay persona is the gateway node):

- **Topics and payloads:** encrypted ServiceEnvelopes on `<root>/2/e/<channel>/<!gateway>`, as the firmware sends them.
- **Uplink:** set per identity channel. Packets go up only when their sender set OK_TO_MQTT, and DMs never go up.
- **Downlink:** set per channel and rate limited. Downlinked packets reach our identities, but the relay never rebroadcasts them unless `relay_mqtt` is set.
- **Map reports:** optional, with the position coarsened to `position_precision` bits.

Recommended for a node that's on the air: uplink the default channel only, keep downlink off
(or on one gateway per mesh), never bridge a private channel, and leave `relay_mqtt` off.

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
"Hopstatic". The HTTP API is documented in [`docs/api.md`](docs/api.md).

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
internal/web           REST/SSE API, auth, embedded GUI
internal/pb            generated protobufs (scripts/gen-proto.sh; vendored in proto/)
ui/                    web GUI (Vue 3 + Vite), built into internal/web/dist
firmware/              KISS modem patch, board list, build script
tests/interop          meshtasticd Docker harness + golden vectors
deploy/                systemd unit, example config, install script
```

## Development

```bash
make test race          # unit tests, simulated multi-host mesh tests, golden vectors
cd ui && npm run dev    # GUI against a mock API
cd tests/interop && ./run_meshtasticd.sh up 2 && ./run_repeatertastic.sh up   # real firmware interop
```

## Licence

GPL-3.0-or-later. The Meshtastic protobufs are GPL-3.0. Parts of the web GUI are adapted from
openHop Repeater UI (MIT, © Lloyd Newton).
