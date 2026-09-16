![ScotMesh Meshtastic](https://raw.githubusercontent.com/ScotMesh/branding/main/networks/meshtastic/readme-header.png)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/repeatertastic-lockup-dark.png">
    <img src="docs/images/repeatertastic-lockup-light.png" width="430" alt="RepeaterTastic: virtual Meshtastic nodes">
  </picture>
</p>

# RepeaterTastic

**Many Meshtastic nodes, one LoRa modem.** RepeaterTastic runs as many Meshtastic nodes as you
like on a Raspberry Pi (or any Linux box) with a cheap LoRa board on USB. Every node is a real
Meshtastic node, a meshtasticd RepeaterTastic starts and shares the radio between. One of them repeats; the rest are yours
to chat, bridge and experiment with. Built and run by [ScotMesh](https://github.com/ScotMesh) for
Scottish mesh sites, and useful anywhere.

![The RepeaterTastic dashboard: radio status and relay switch, traffic counters, airtime against the duty cycle, noise floor, packets by port and live packets](docs/images/dashboard.png)

- **Nodes ("identities")** with their own key, node number, channels and app port, each running on
  its own meshtasticd. The official Meshtastic apps, the Python CLI and the web client connect to
  each one as if it were a radio.
- **A proper repeater:** one relay persona, itself a meshtasticd, does the repeating with a shared
  duty-cycle budget, so ten identities never mean ten repeats. Its role is a Meshtastic device
  role (client, client base, client mute, router, router late), or put the radio in Monitor (listen only) or Off from the top bar.
- **A web GUI** for everything: identities, chat (as any identity, the relay persona included),
  channels, a live node map, packets, statistics, logs, backups and configuration.
- **Several radios on one host** (LongFast, MediumFast, …) that take turns on shared frequencies.
- **MQTT** to one or more brokers (gateway, uplink-only, map reports, monitor, bridge), a fixed
  site position, device telemetry and an optional UDP multicast link to `meshtasticd` on the LAN.
- **Plugins** add uploaders, bots and dashboards: upload a .zip in the GUI or drop it in a folder,
  choose what each one may see, and cap how much it may send.
- **One container, or one binary plus meshtasticd.** The image includes meshtasticd; standalone,
  the installer sets it up. The top bar shows whether every node is running.

> **Status:** running on a ScotMesh site on a Heltec V3, alongside Reticulum. Still young: expect
> changes. Needs meshtasticd 2.8.0 or newer. Verified on air with KISS modems, a CH341 stick and a
> board on Meshtastic firmware: channel messages, PKI DMs with ACKs, local DMs between identities,
> MQTT, and the official apps and CLI on the identity ports.

## Quick start

### 1. Get a modem

RepeaterTastic drives a LoRa board running **Mesh KISS**: MeshCore's KISS modem firmware patched to
speak Meshtastic's PHY. Heltec V3/V4, XIAO nRF52840 + Wio-SX1262, RAK4631, T-Beam and more are
supported. Download a prebuilt image (`kiss-firmware-<board>.zip`) from the
[latest release](https://github.com/ScotMesh/RepeaterTastic/releases/latest) or build it, then flash:

```bash
esptool.py --chip esp32s3 --port /dev/ttyUSB0 write_flash 0x0 Heltec_v3_kiss_modem-factory.bin   # ESP32 boards
# nRF52 boards: double-tap reset and copy the .uf2 onto the USB drive that appears
```

Plug it into the host and find its stable path: `ls -l /dev/serial/by-id/`. More in
[Hardware and modems](docs/hardware.md).

**Or skip the modem board.** RepeaterTastic can drive a LoRa HAT or USB stick itself, the hardware
meshtasticd runs on: MeshAdv, Waveshare, RAK6421, Nebra/Zebra, PiMesh, PiTastic, Femtofox and
Luckfox HATs (SX126x and LR1121), and CH341 USB sticks such as MeshStick, Meshtoad, uMesh and
RAK19714. All of meshtasticd's board files are built in, so pick your board in the setup wizard
(or set `radio: {driver: spi, device: MeshAdv-900M30S}`; `auto` detects USB sticks and Pi HAT+
boards). meshtasticd's own service must not be running on the same radio (the installer turns it
off). Tested on an SX1262 over CH341 so far; HATs need testers: see [LoRa HATs and USB sticks](docs/spi-radio-testing.md).

### 2a. Run it with Docker

```bash
docker run -d --name repeatertastic --restart unless-stopped \
  --network host \
  --device /dev/serial/by-id/usb-…-if00-port0:/dev/ttyUSB0 \
  --group-add "$(getent group dialout | cut -d: -f3)" \
  -e REPEATERTASTIC_RADIO_DEVICE=/dev/ttyUSB0 \
  -v repeatertastic-data:/data \
  ghcr.io/scotmesh/repeatertastic:latest
```

- `--device` passes the modem in; `--group-add` lets the unprivileged container user open it.
  For a HAT pass `--device /dev/spidev0.0 --device /dev/gpiochip0` and the `spi` and `gpio` groups;
  for a CH341 stick `--device /dev/bus/usb` and its udev group.
- `--network host` lets the apps find identities over mDNS and joins the LAN multicast mesh. Without
  it, publish `-p 8080:8080 -p 4403-4410:4403-4410` instead.
- The image includes meshtasticd 2.8, which runs every node inside the container: nothing else to
  install. Its API ports (4500 up) stay inside; don't publish them.
- The `/data` volume holds the config, identity keys, chats and the nodes' state. Back it up.
- Prefer Compose? Copy [`deploy/docker-compose.example.yml`](deploy/docker-compose.example.yml).
- The image is published to `ghcr.io` with each release. To run your own build instead, build it
  with `docker build -t repeatertastic .` and use `repeatertastic` as the image name.

### 2b. Or run it standalone (systemd)

```bash
git clone https://github.com/ScotMesh/RepeaterTastic && cd RepeaterTastic
# binaries from the latest release: arm64 = 64-bit Raspberry Pi OS; armv7, armv6 and amd64 also available
gh release download -R ScotMesh/RepeaterTastic -p 'repeatertastic-linux-arm64' -p 'kisstool-linux-arm64'
sudo ./deploy/install.sh ./repeatertastic-linux-arm64 ./kisstool-linux-arm64
journalctl -u repeatertastic -f
```

The installer creates a `repeatertastic` user in `dialout`, installs the systemd unit and writes
`/etc/repeatertastic/repeatertastic.yaml`, pointing it at the first USB serial device it finds
(check it if several boards are plugged in).

It also sets up meshtasticd (2.8.0 or newer), which runs the nodes. You choose how with
`--meshtasticd`:

| Option | What it does |
| --- | --- |
| `auto` (default) | Uses a meshtasticd 2.8+ already on the PATH; otherwise the same as `apt` |
| `apt` | Installs the `meshtasticd` package from Meshtastic's alpha repository (Debian 12/13, Raspberry Pi OS, Ubuntu), then turns its own service off |
| `docker` | Pulls the meshtasticd image and lets the service use Docker (choose Docker in the setup wizard) |
| `skip` | Leaves meshtasticd to you: build it or install it your way, then give its path in the setup wizard |

For example `sudo ./deploy/install.sh --meshtasticd docker ./repeatertastic-linux-arm64`.

### 3. Set it up in the browser

1. Open `http://<host>:8080` and choose the admin password.
2. The setup wizard finds the modem (a serial port, a HAT or USB stick from the board list, or a
   board on Meshtastic firmware), checks meshtasticd, sets region and preset, and tests it.
3. The **meshtasticd** chip in the top bar turns green once the relay persona runs. Amber means an
   identity's node is down, red means meshtasticd isn't running; click it for the details.
4. **Identities → New identity** creates a node and gives it an app port.
5. In the Meshtastic app, add a **network** device: `<host>:<port>` (for example `192.168.1.20:4404`).

### Build it yourself

```bash
make ui                                  # web GUI → internal/web/dist (Node 22)
make build                               # bin/repeatertastic, bin/kisstool (run with meshtasticd 2.8+)
make dist                                # static binaries for Pi (arm64/armv7/armv6) and amd64
docker build -t repeatertastic .         # container image, meshtasticd included
./firmware/build.sh Heltec_v3_kiss_modem # modem firmware (PlatformIO)
```

Go 1.25+ is needed. `MAP_API_KEY=… make build` (or `--secret id=map_api_key,env=CARTO_API_KEY` for
Docker) bakes in a default map tile key; see [Configuration](docs/configuration.md#web-and-map-tiles).

## Documentation

| Guide | What's in it |
| --- | --- |
| [Hardware and modems](docs/hardware.md) | Supported boards, flashing Mesh KISS, stable device paths, permissions, Docker devices, `kisstool`, troubleshooting |
| [meshtasticd nodes](docs/meshtasticd-nodes.md) | How the relay persona and identities run on meshtasticd, the status indicator, and measured firmware behaviour |
| [LoRa HATs and USB sticks](docs/spi-radio-testing.md) | Driving a Pi HAT or CH341 stick directly (`radio.driver: spi`): supported boards, setup, and how to test one |
| [Configuration](docs/configuration.md) | The config file section by section, environment variables, what applies live, backups |
| [Using the web GUI](docs/web-gui.md) | Identities, chat, channels, nodes and map, packets, statistics, configuration tabs |
| [Several radios](docs/radios.md) | Running LongFast and MediumFast side by side, moving identities between them, and the site airtime cap |
| [MQTT](docs/mqtt.md) | Broker connections, modes, channels, relaying and map reports |
| [Plugins](docs/plugins.md) | Installing plugins (GUI, folder, CLI, Docker), permissions, attached plugins, and writing your own |
| [Architecture and development](docs/architecture.md) | How it fits together, code layout, tests and interop |
| [Plugin API](docs/plugin-api.md) | Reference for plugin authors: the gRPC session, calls, events, errors, manifest and settings |
| [HTTP API](docs/api.md) | REST and event-stream API for scripts and integrations |
| [Bench test](docs/bench-test.md) | Step-by-step first test on a real radio |
| [AGENTS.md](AGENTS.md) | Rules and commands for coding agents and contributors |

## Licence

GPL-3.0-or-later. The Meshtastic protobufs are GPL-3.0. Parts of the web GUI are adapted from
openHop Repeater UI (MIT, © Lloyd Newton).
