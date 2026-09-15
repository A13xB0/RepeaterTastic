# RepeaterTastic

Host many virtual Meshtastic nodes in software, all sharing one dumb LoRa modem — the Meshtastic
equivalent of openHop Repeater's virtual companions.

- One Go binary for a Raspberry Pi or any Linux box, no CGO.
- The radio is a MeshCore KISS modem (e.g. Heltec V3) patched to speak Meshtastic's PHY (sync word
  0x2B, 16-symbol preamble); see [`firmware/`](firmware/).
- Every virtual node has its own keys, node number, channels and a Meshtastic client-API TCP port, so
  the official apps and the Python CLI can connect to each one.
- One relay persona per radio, shared packet history and airtime budget, local routing between
  identities.
- Web GUI modelled on openHop's dashboard.

> Status: under active construction. Not yet tested on air.

The design plan is in [`docs/plan.html`](docs/plan.html) (written under the working name "Hopstatic").

## Layout

```
cmd/repeatertastic     main binary
internal/wire          16-byte header, AES-CTR channels, X25519 + AES-CCM DMs
internal/phy           regions, presets, frequency slots, airtime, contention window
internal/pb            generated Meshtastic protobufs (scripts/gen-proto.sh)
proto/                 vendored meshtastic/protobufs (see proto/PATCHES.md)
firmware/              KISS modem patch + build script
tests/interop          meshtasticd (Docker, UDP multicast) reference harness
ui/                    web GUI (Vue)
```

## Licence

GPL-3.0-or-later (Meshtastic protobufs are GPL-3.0).
