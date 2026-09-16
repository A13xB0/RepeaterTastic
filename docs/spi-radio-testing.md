# LoRa HATs and USB sticks (the spi driver)

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Bench test](bench-test.md)

RepeaterTastic normally drives a LoRa board flashed as a USB KISS modem. The `spi` driver instead
talks directly to the LoRa chip on the hardware `meshtasticd` runs on, with no meshtasticd:

| Chip | Examples |
|---|---|
| SX1262, SX1268, LLCC68 | MeshAdv Pi Hat and Mini, Waveshare SX126x, RAK6421 (RAK13300/13302), Nebra and Zebra hats, PiMesh, PiTastic, Femtofox, Luckfox, Station G3 |
| SX1262/SX1268 on a CH341 USB stick | MeshStick, Meshtoad, RAK19714, uMesh 30 dBm, FrameTastic, PiNedio USB, XIAO + Wio-SX1262 with the CH341 bridge firmware |
| LR1121 | Femtofox E80, PiggyStick (USB) |
| SX1276/SX1278 (RF95) | Adafruit RFM9x |
| SX1280 (2.4 GHz) | any SX1280 wired like meshtasticd's `Module: sx1280` |

It reads meshtasticd's own board files, and has a copy of all 61 of them built in, so you don't
need meshtasticd installed.

**Status:** tested on air with an SX1262 over a CH341 USB adapter (receive and transmit against a
live EU_868 LongFast mesh, and RepeaterTastic running on it). SPI HATs, LR1121, RF95 and SX1280
follow the Semtech datasheets and RadioLib's command sequences but haven't been run on hardware
yet. If you have one, please work through the steps in order and send back the output of each;
a failure at one step tells us which layer is wrong.

## What you need

- A Pi (or similar) with the radio HAT, or a CH341 USB radio, **antenna fitted**.
- Your board's name. `./kisstool-linux-arm64 boards` lists the built-in ones (use the file name,
  e.g. `MeshAdv-900M30S`). A board file path works too, and so does `auto` for boards meshtasticd
  can detect: CH341 sticks, Pi HAT+ boards and RAK boards with an ID EEPROM.
- A second Meshtastic node (any stock node plus the phone app) on the same region and preset. The
  steps below use **EU_868 LongFast**; pass `--region`/`--preset` if yours differs.
- The two binaries from the draft release: `kisstool-linux-<arch>` and
  `repeatertastic-linux-<arch>`, where `<arch>` is `arm64` for a 64-bit Pi OS, `armv7` for 32-bit,
  or `armv6` for a Pi Zero / Pi 1.

## 1. Free the radio

Only one program can drive the chip. Stop meshtasticd and make sure SPI is on:

```bash
sudo systemctl stop meshtasticd
sudo systemctl disable meshtasticd          # optional, so it doesn't come back at boot
ls /dev/spidev* /dev/gpiochip*              # expect e.g. /dev/spidev0.0 and /dev/gpiochip0
```

If there's no `/dev/spidev0.0`, add `dtparam=spi=on` to `/boot/firmware/config.txt` and reboot.
Your user needs the `spi` and `gpio` groups (`sudo usermod -aG spi,gpio $USER`, then log in
again). Or run the tools with `sudo` for these tests.

**Pi 5:** the header pins are `gpiochip0` on current kernels (`gpiochip4` on some older ones). Check
with `gpioinfo | head`. If your board file doesn't say, add `gpiochip: 4` under `Lora:` if needed.

**CH341 USB sticks:** no SPI setup is needed, but the tools need write access to the USB device.
For these tests run them with `sudo`, or add a udev rule:

```bash
echo 'SUBSYSTEM=="usb", ATTRS{idVendor}=="1a86", ATTRS{idProduct}=="5512", MODE="0660", GROUP="plugdev"' | sudo tee /etc/udev/rules/99-ch341-lora.rules
sudo udevadm control --reload && sudo udevadm trigger   # then unplug and replug the stick
```

## 2. Does the chip answer?

```bash
chmod +x kisstool-linux-*
./kisstool-linux-arm64 info --board MeshAdv-900M30S     # or a board file path, or auto
```

Good output for an SX1262 HAT looks like this; other chips show their own version and error lines:

```
board:      MeshAdv-Pi E22-900M30S (sx1262 on spidev0.0, CS gpiochip0 line 21, IRQ gpiochip0 line 16, …)
            from built-in lora-MeshAdv-900M30S.yaml
device:     /dev/spidev0.0
chip:       sx1262 (answered: its ID check passed)
status:     0x2c (standby RC)
dev errors: 0x0000 (none)
configured EU_868 LONG_FAST: 869.5250 MHz, BW 250 kHz, SF11, CR4/5, sync 0x2b, preamble 16, 27 dBm
noise:      -112 dBm (RX on 869.5250 MHz; about -100 to -125 is normal)
```

What the errors mean:

| Error | Likely cause |
|---|---|
| `open /dev/spidev0.0: permission denied` | not in the `spi` group, or use `sudo` |
| `busy (gpiochip0 line 20): device or resource busy` | meshtasticd (or another program) still holds the pins |
| `after reset: the chip stayed BUSY` | wrong Busy or Reset pin, or the HAT has no power (check `Enable_Pins`) |
| `didn't answer as an SX126x` / `SX127x` / `SX1280` / `LR11x0` | wrong `spidev`, wrong CS pin, wrong `Module`, or bad wiring |
| `no CH341 USB radio 1a86:5512 found` | the stick isn't plugged in, or has a different USB ID (`lsusb`) |
| `claim CH341 interface: device or resource busy` | meshtasticd still has the stick open |
| `the LR11x0 is in its bootloader` | the LR1121 has no radio firmware yet: run meshtasticd once, which flashes it |
| `dev errors: … XOSC start` | the TCXO voltage (`DIO3_TCXO_VOLTAGE`) is wrong for this module |
| noise around 0 dBm or stuck at one value | RX isn't really running: send the full output |

## 3. Hear Meshtastic traffic

```bash
./kisstool-linux-arm64 listen --board MeshAdv-900M30S    # your board
```

Send a message from the phone app on the other node. Within a few seconds you should see:

```
14:02:11.412  52 bytes  RSSI -61 dBm  SNR 9.75 dB
  ffffffff…
  from !a1b2c3d4 to !ffffffff id 0x1234abcd hop 3/3 chan 0x08 …
  port TEXT_MESSAGE_APP, 13 byte payload: "hello from rt"
```

Leave it running for 10 to 15 minutes so it also catches NodeInfo and telemetry from nearby nodes.
Ctrl-C to stop. **If you see nothing:** confirm the other node is on the same region and preset,
check the antenna, then re-run with the IRQ pin removed from a copy of the board file (take it
from `/etc/meshtasticd/available.d` or the firmware repo's `bin/config.d`, and pass its path; the driver
then polls the chip). If that works, the IRQ pin is wrong.

## 4. Transmit

```bash
./kisstool-linux-arm64 send-text --board MeshAdv-900M30S --power 10 "spi test 1"
```

Expect `TxDone after …ms` (about 1 to 2 s on LongFast) and the message on the other node's phone.
Please try this once or twice only. Start with `--power 10`; the power is at the antenna, and
boards with a `TX_GAIN_LORA` table (1 W amps) have their chip set lower to allow for the amp.

If you get `no TxDone`, send the output. On boards with `TXen`/`RXen` pins, or with
`DIO2_AS_RF_SWITCH`, a wrong RF switch shows up here first.

## 5. Run RepeaterTastic on it

```bash
mkdir -p ~/rt-test && cd ~/rt-test
cat > config.yaml <<'EOF'
state_dir: state
radio:
    driver: spi
    device: MeshAdv-900M30S   # your board: a built-in name, a board file path, or auto
mesh:
    region: EU_868
    preset: LONG_FAST
    tx_power_dbm: 10
relay:
    role: client_mute # listen and answer as its own node, but don't repeat others yet
EOF
~/repeatertastic-linux-arm64 -config config.yaml      # wherever you downloaded it
```

Open `http://<pi>:8080` and go through setup. In the first step choose "LoRa board on SPI or a
USB stick" and pick your board (or leave it on "Detect automatically"); "Test modem" opens the
board and shows the chip's version and mode. Once set up, the same choice is under Configuration
→ Radios → Edit radio. Then check:

- the top bar shows `spi · MeshAdv-900M30S` (your board);
- nodes appear on the Nodes page within about 15 minutes;
- a DM or channel message from your phone arrives, and a reply from the GUI reaches the phone;
- the noise floor on the dashboard looks sensible.

If all of that works, try `relay.role: client` (repeats like a normal client) and leave it
running for a few hours.

## What to send back

- The model of Pi and HAT, the board file you used, and `uname -a`.
- The full output of steps 2 to 4.
- For step 5: the first 100 lines of the daemon log, plus any `radio`/`spi` warnings after that.
- Anything odd: packets heard by the other node but not by this one (or the reverse), RSSI that
  looks wrong compared with the other node, or TX that the other node never hears.

To go back to meshtasticd: stop RepeaterTastic and run `sudo systemctl enable --now meshtasticd`.
Nothing in `/etc/meshtasticd` is changed by these tests.
