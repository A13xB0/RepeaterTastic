# meshtasticd as the modem

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Several radios](radios.md)

If the radio is a Linux LoRa HAT (SPI) or a CH341 USB stick, you don't need a Mesh KISS board:
`meshtasticd` can be the modem. Its **raw modem mode** takes meshtasticd's own mesh node off the
air and serves the radio over TCP with the same protocol Mesh KISS speaks (version 2, so the sync
word and preamble can be set). RepeaterTastic connects to it with `device: tcp://host:port` and gets
every frame it hears, with RSSI and SNR. Any radio meshtasticd drives works: SX126x, SX127x, LR11x0,
SX128x and LR2021 through RadioLib, on SPI or the CH341 bridge.

> **Needs a patched meshtasticd.** Raw modem mode is not in Meshtastic's releases yet. Until it is
> merged upstream, build meshtasticd from the `raw-modem` branch of
> [A13xB0/firmware](https://github.com/A13xB0/firmware/tree/raw-modem). A stock meshtasticd
> ignores `RawModemPort` and keeps running as a normal node.

## 1. Build meshtasticd with raw modem mode

On the machine with the radio (a Raspberry Pi or other Linux board), with the build dependencies
from Meshtastic's [Linux native build instructions](https://meshtastic.org/docs/software/linux/installation/):

```sh
git clone --recurse-submodules -b raw-modem https://github.com/A13xB0/firmware
cd firmware
pio run -e native
sudo systemctl stop meshtasticd
sudo install -m 755 .pio/build/native/meshtasticd /usr/bin/meshtasticd   # over the packaged binary
```

Or build the container image: `docker build -t meshtasticd-raw .` in the same checkout.

Keep the packaged `/etc/meshtasticd/config.yaml` and `config.d/` hardware file for your HAT or
stick; raw modem mode only adds one setting.

## 2. Turn it on

In `/etc/meshtasticd/config.yaml`:

```yaml
General:
  RawModemPort: 4405
```

or start meshtasticd with `--raw-modem 4405`. Then `sudo systemctl start meshtasticd`; the log says
`Raw modem mode: mesh stack detached from the radio, KISS modem on TCP port 4405`.
`meshtasticd --check` reports a port outside 1024-65535 or equal to the API port, and meshtasticd
refuses to start with one rather than fall back to meshing.

## 3. Point RepeaterTastic at it

The option is behind an experimental switch until raw modem mode is merged upstream: turn on
**Configuration → Experimental → meshtasticd as the modem**, or set it in the config file.

```yaml
experimental:
    meshtasticd_raw_modem: true
radio:
    driver: kiss
    device: tcp://127.0.0.1:4405
```

or `REPEATERTASTIC_RADIO_DEVICE=tcp://127.0.0.1:4405`. The web GUI's setup **Test modem** button
takes the same address. Check it from the command line with
`kisstool info --dev tcp://127.0.0.1:4405`: the name reads `meshtasticd` plus the radio module, and
the firmware `Mesh KISS v2`.

RepeaterTastic sets frequency, bandwidth, SF, CR, power, sync word and preamble on connect, exactly
as with a Mesh KISS board, and reconnects by itself when meshtasticd restarts.

## Things to know

- **The port has no authentication** and listens on all interfaces. Run RepeaterTastic on the same
  machine and firewall the port, or keep it on a trusted network. If RepeaterTastic runs in Docker
  with bridge networking, `127.0.0.1` is the container: use host networking or the host's address.
- **One client at a time.** A new connection replaces the current one, so two RepeaterTastic radios
  can't share a meshtasticd (the config check refuses the same `tcp://` device twice).
- **meshtasticd still runs** (its API, web server and MQTT), but its node neither transmits nor
  receives on the radio. Its LoRa settings in the Meshtastic app don't affect the radio while
  RepeaterTastic is connected; RepeaterTastic's config does.
- **Transmit rules still apply.** meshtasticd's `lora.tx_enabled` must be on (the default), and its
  region's power limit and the `TX_GAIN_LORA` PA gain from the hardware config still cap the power
  RepeaterTastic asks for. Set meshtasticd's region to yours (an unset region caps at 30 dBm).
- With no client connected, received frames are dropped.
