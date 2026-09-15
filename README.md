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
