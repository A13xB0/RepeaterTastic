# Bench test: first time on a real radio

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Web GUI](web-gui.md) · [Several radios](radios.md) · [MQTT](mqtt.md) · [Architecture](architecture.md)

Use this for a first test of a new board, site or build. Work through the steps in order: each one
isolates a layer, so a failure points at one place.

You need:

- **The modem:** a supported board (e.g. Heltec V3) for the KISS modem.
- **A reference node:** a second board running **stock Meshtastic** firmware, on the same region/preset (EU_868 LongFast by default), with the phone app paired.
- **The host:** a Pi or laptop connected to the modem over USB.
- **Antennas on both boards before powering them.** Keep the two boards a few metres apart, or lower the TX power: two nodes at full power 30 cm apart can overload each other's receivers.

## 1. Flash the modem

Prebuilt images and Pi binaries are attached to the `v0.1.0-bench` GitHub release
(`gh release download v0.1.0-bench -R ScotMesh/RepeaterTastic`); `firmware-boards.md` there says
which file to flash for each board. Or build them yourself:

```bash
./firmware/build.sh Heltec_v3_kiss_modem          # or use a prebuilt image from firmware/out/
esptool.py --chip esp32s3 --port /dev/ttyUSB0 write_flash 0x0 firmware/out/Heltec_v3_kiss_modem/*factory.bin
```

nRF52 boards (XIAO nRF52840 + Wio-SX1262, RAK4631, T-Echo…): double-tap reset and copy the
`.uf2` onto the USB drive that appears. See `firmware/boards.md`.

## 2. Talk to the modem

```bash
make build        # or use dist/kisstool-linux-arm64 on a Pi
bin/kisstool info --dev /dev/ttyUSB0
```

Expect a firmware version of **2 or higher**. Version 1 means stock MeshCore KISS firmware, which
can't use Meshtastic's sync word, so re-flash. After tuning (step 3), `info` should also show the
radio set to 869.525 MHz / 250 kHz / SF11 / CR5, sync `0x2b`, preamble 16.

## 3. Hear Meshtastic

```bash
bin/kisstool listen --dev /dev/ttyUSB0 --region EU_868 --preset LONG_FAST
```

Send a message on the primary channel from the stock node's app. You should see a frame with a
decoded header, channel hash `0x08` and your text.

If nothing arrives:

- **Region and preset:** the stock node must be on the same region and preset, and on the default primary channel. The channel *name* picks the frequency.
- **Receive check:** `kisstool info` → the `rx` counter goes up if the modem receives anything at all.
- **Errors counter going up:** the counter rises but nothing decodes. That's usually a PHY mismatch (bandwidth, SF, sync word).

## 4. Be heard

```bash
bin/kisstool send-text --dev /dev/ttyUSB0 --region EU_868 --preset LONG_FAST "hello from kisstool"
```

The stock node's app should show the message on the primary channel, from a node with an unknown
name (a random `!xxxxxxxx`). If it doesn't:

- **Transmit check:** `kisstool info` shows the `tx` counter going up.
- **Duty cycle:** RepeaterTastic enforces the region's duty cycle (EU_868 is 10%/hour); kisstool does not.

## 5. Run RepeaterTastic

```bash
sudo ./deploy/install.sh dist/repeatertastic-linux-arm64 dist/kisstool-linux-arm64
# or, in the foreground:
bin/repeatertastic -config deploy/repeatertastic.example.yaml   # set radio.device and state_dir first
```

Open `http://<host>:8080`, set the admin password, and check that the top bar shows the modem
connected on EU_868 · LongFast · 869.525 MHz · sync 0x2B.

Within about a minute each identity broadcasts its NodeInfo. The stock node's app should list
**RepeaterTastic Relay** and **Base Camp** with a lock icon, meaning it has their public keys.

## 6. Connect an app to a virtual node

- **Python CLI:** `meshtastic --host <host>:4403 --info`, then `--nodes`, then
  `--dest '!<stock node id>' --sendtext hi --ack`.
- **Android app:** each identity should appear under network devices by itself (mDNS
  `_meshtastic._tcp`, listed by host:port and named from the TXT `shortname`/`id`). If it
  doesn't, use "add network device" with the host and the identity's port. The current app source
  has a port field and keeps host:port entries separate, so several identities on one IP work.
- **iOS app:** not checked in source. If it can't take a port, give each identity its own IP
  address (`api_bind` per identity).
- **Official web client:** `http://<host>:4403` serves `/api/v1/toradio` and `/api/v1/fromradio`.

## 7. The things worth checking

| Test | Expected |
|---|---|
| Channel message from the stock node | Appears in every identity's app and in the web GUI chat for each identity |
| DM stock node → Base Camp | Base Camp's app receives it; the stock node's app shows a delivered tick (real ACK) |
| DM Base Camp → stock node | The stock node receives it; Base Camp shows an ACK |
| DM Base Camp → Ops Desk (same host) | Delivered instantly, and the web packet log shows `local` with no TX |
| Traceroute from the web GUI to the stock node | A route appears |
| Stock node out of range of a third node, host in between | The relay persona relays (web packet log: `relayed`) |
| Unplug the modem USB, plug back in | The log shows the reconnect, the radio is re-tuned, and traffic resumes |

## What to send back if something fails

- `journalctl -u repeatertastic -n 200`, or the web GUI Logs page.
- The web GUI Packets page, showing raw hex for the frames in question.
- `kisstool info` output.
