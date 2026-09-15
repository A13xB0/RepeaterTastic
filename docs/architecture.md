# Architecture and development

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Web GUI](web-gui.md) · [Several radios](radios.md) · [MQTT](mqtt.md)

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
- **Several radios:** each radio is its own mesh host (identities, node DB, queue). `internal/site`
  makes radios on overlapping frequencies take turns and applies the site airtime cap;
  `mesh.Federation` joins the hosts for the experimental identities on several radios.
- **Links:** UDP multicast and MQTT connections see packets as they're received and sent, and inject
  broker packets into the receive pipeline marked `via_mqtt`.
- **App API:** each identity listens on its own TCP port with the Meshtastic client protocol
  (`internal/phoneapi`); the web GUI and scripts use the REST/SSE API on `:8080`.

The original plan and research are in [`plan.html`](plan.html). The HTTP API is documented in
[`api.md`](api.md). Coding agents and new contributors: read [`AGENTS.md`](../AGENTS.md) first.

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
pb/                    generated Meshtastic protobufs (scripts/gen-proto.sh; vendored in proto/), public for plugins
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

### Checks before a pull request

- `go vet ./... && go test ./...` (add `-race` for `internal/mesh`, `internal/site`, `internal/links`)
- `cd ui && npm run build` when anything under `ui/` changed, committing `internal/web/dist`
- README, the guides in `docs/` and `deploy/repeatertastic.example.yaml` updated with the change
