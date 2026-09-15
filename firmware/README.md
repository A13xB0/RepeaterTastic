# RepeaterTastic modem firmware

RepeaterTastic uses a stock LoRa board running the MeshCore **KISS modem** firmware
(`examples/kiss_modem`) as a dumb radio. Stock MeshCore hardcodes the LoRa sync
word to `0x12` and picks the preamble length from the spreading factor, so it
cannot hear Meshtastic (sync `0x2B`, preamble 16). `0001-kiss-modem-sync-word-preamble.patch`
adds runtime control of both.

## What the patch does

* **KISS modem** (`examples/kiss_modem/KissModem.{h,cpp}`, `main.cpp`)
  * New SetHardware sub-commands `0x1B` SetSyncWord, `0x1C` SetPreamble, `0x1D` GetPhyExtra
    (wired via callbacks like SetRadio).
  * `KISS_FIRMWARE_VERSION` bumped `1` -> `2` (GetVersion reply `0x91 02 00`).
  * Fixes a pre-existing compile error on `Heltec_v3_kiss_modem` (and other ESP32-S3
    boards with a USB-UART bridge): `Serial.setTxTimeoutMs()` is now only called when
    `ARDUINO_USB_CDC_ON_BOOT` makes `Serial` a HWCDC.
* **RadioLib wrappers** (`src/helpers/radiolib/RadioLibWrappers.{h,cpp}`, `Custom*Wrapper.h`)
  * `RadioLibWrapper::setLoRaSyncWord(uint8_t)` / `getLoRaSyncWord()`: uses the chip's
    RadioLib `setSyncWord()` (SX1262/SX1268/LLCC68/STM32WLx via `SX126x`, SX1276 via
    `SX127x`, LR1110 via `LR11x0`, LR2021 via `LR2021`; LR2021 side detectors also use it).
  * `RadioLibWrapper::setPreambleOverride(uint16_t)`: `0` = MeshCore default
    (32 symbols for SF<=8, 16 otherwise). A non-zero override is used by `begin()`,
    `updatePreamble()` (so it survives every `setParams()`/SetRadio), the LR1110/LR2021
    post-TX preamble reset, and the header/payload RX timeout calculation
    (`calcMaxPacketMillis`, now `uint16_t` symbols with 64-bit maths). RadioLib's
    `getTimeOnAir()` reads the chip's configured preamble, so GetAirtime and TX timeouts
    follow automatically.
  * Overrides are re-applied after the periodic 30 s AGC reset (warm sleep + recalibrate),
    in case the chip does not retain them. With no overrides set, nothing changes.
* **Docs**: `docs/kiss_modem_protocol.md` documents the new commands and version table.

If the new commands are never sent the modem behaves exactly as before (sync `0x12`,
SF-derived preamble).

## New commands

All frames are KISS type `0x06` (SetHardware), port 0. Multi-byte values little-endian.

| Command     | Sub-cmd | Request data                      | Reply |
|-------------|---------|-----------------------------------|-------|
| SetSyncWord | `0x1B`  | sync (1)                          | `0xF0` OK, or `0xF1` Error + code |
| SetPreamble | `0x1C`  | symbols u16 LE: `0` or `6..65535` | `0xF0` OK, or `0xF1` Error + code |
| GetPhyExtra | `0x1D`  | -                                 | `0x9D` sync (1) + preamble u16 LE (effective value) |

Error codes: `0x01` InvalidLength (data too short), `0x02` InvalidParam (preamble 1-5,
or the radio rejected the value), `0x03` NoCallback (not wired on this build),
`0x05` UnknownCmd (firmware version 1, i.e. unpatched), `0x07` TxBusy (a packet is on air
right now; retry after TxDone).

Example wire bytes:

```
SetSyncWord 0x2B   C0 06 1B 2B C0         -> C0 06 F0 C0
SetPreamble 16     C0 06 1C 10 00 C0      -> C0 06 F0 C0
GetPhyExtra        C0 06 1D C0            -> C0 06 9D 2B 10 00 C0
GetVersion         C0 06 11 C0            -> C0 06 91 02 00 C0
```

Changing either setting puts the radio in standby and restarts RX (a packet arriving
in that instant can be lost). Settings live in RAM only: re-send them after every
reboot or reconnect.

## Building

```
./build.sh                                   # Heltec_v3_kiss_modem
./build.sh Xiao_nrf52_kiss_modem RAK_4631_kiss_modem   # any envs listed in boards.txt
PARALLEL=3 PIO_JOBS=3 ./build.sh all         # every board in boards.txt (~92 envs)
CLEAN=1 ./build.sh all                       # delete per-env build dirs/libdeps after each success
MESHCORE_DIR=~/src/MeshCore ./build.sh       # build in an existing checkout (patch applied if needed)
```

Without `MESHCORE_DIR` the script clones upstream MeshCore into `firmware/work/MeshCore`
(gitignored), checks out the base commit (`e0031870`, meshcore-dev/MeshCore `dev`), applies the
patch and runs PlatformIO (`pip install platformio`, or `~/.local/bin/pio`).

`boards.txt` is the target list (`env | friendly name | MCU | radio | flash format`), and
`boards.md` is the per-board table: what to flash, how, and the last build status. A full
`all` build needs about 20 GB for `.pio` unless `CLEAN=1` is set.

**Where the binaries go (not in git)**: build products are written to `firmware/out/`:

```
out/<env>/<env>-factory.bin   ESP32: full image, flash at 0x0
out/<env>/<env>-app.bin       ESP32: app only, flash at 0x10000 (keeps identity)
out/<env>/<env>.uf2           nRF52840 / RP2040: drag & drop onto the bootloader drive
out/<env>/<env>-dfu.zip       nRF52840: adafruit-nrfutil serial DFU package
out/<env>/<env>.bin|.hex      STM32WL (and extra copies for nRF52/RP2040)
out/<env>/SHA256SUMS
out/logs/<env>.log            build log
out/status.tsv                env, OK/FAILED, artifacts or first error line
```

A fresh checkout has no images: run `./build.sh` first. The repo `.gitignore` currently
covers only `/firmware/out/*.bin`, so ignore the whole `/firmware/out/` directory.

`.github-workflow-example.yml` is a ready-to-copy GitHub Actions workflow (not enabled) that
builds the `boards.txt` matrix and attaches per-board zips to a `fw-v*` tag release.

## Flashing a Heltec V3

See `boards.md` for other boards (esptool / UF2 / nrfutil / STM32 SWD).

The V3 has a CP2102 USB-UART bridge, usually `/dev/ttyUSB0` on Linux. If the port does
not respond, hold **PRG**, tap **RST**, release **PRG** to enter the ROM bootloader.

Fresh install (erases identity and any other firmware):

```
esptool.py --chip esp32s3 --port /dev/ttyUSB0 erase_flash
esptool.py --chip esp32s3 --port /dev/ttyUSB0 --baud 921600 write_flash 0x0 out/Heltec_v3_kiss_modem/Heltec_v3_kiss_modem-factory.bin
```

Update over an existing MeshCore ESP32 install (keeps partition table and SPIFFS identity):

```
esptool.py --chip esp32s3 --port /dev/ttyUSB0 --baud 921600 write_flash 0x10000 out/Heltec_v3_kiss_modem/Heltec_v3_kiss_modem-app.bin
```

Use `esptool.py` from `pip install esptool` (newer releases name the command `esptool`),
or PlatformIO's copy: `python3 ~/.platformio/packages/tool-esptoolpy/esptool.py`.
Serial link afterwards: 115200 8N1.

## Flashing a Seeed XIAO nRF52840 + Wio-SX1262 kit

Double-tap the tiny RESET button on the XIAO; a `XIAO-SENSE` USB drive appears. Copy
`out/Xiao_nrf52_kiss_modem/Xiao_nrf52_kiss_modem.uf2` onto it. It reboots into the modem and
shows up as `/dev/ttyACM0` (115200 8N1). Alternatively:
`adafruit-nrfutil dfu serial --package out/Xiao_nrf52_kiss_modem/Xiao_nrf52_kiss_modem-dfu.zip -p /dev/ttyACM0 -b 115200 --singlebank --touch 1200`.

## Host session init: Meshtastic LongFast, EU_868

Send in this order, waiting for each reply (`F0` OK unless noted):

| Step | Frame (hex) | Expected reply |
|------|-------------|----------------|
| Ping | `C0 06 17 C0` | `C0 06 97 C0` |
| GetVersion (require >= 2) | `C0 06 11 C0` | `C0 06 91 02 00 C0` |
| SetRadio 869525000 Hz / 250000 Hz / SF11 / CR5 | `C0 06 09 08 E6 D3 33 90 D0 03 00 0B 05 C0` | `C0 06 F0 C0` |
| SetSyncWord 0x2B | `C0 06 1B 2B C0` | `C0 06 F0 C0` |
| SetPreamble 16 | `C0 06 1C 10 00 C0` | `C0 06 F0 C0` |
| TXDELAY 0 | `C0 01 00 C0` | none (standard KISS, no reply) |
| Persistence 255 | `C0 02 FF C0` | none |
| (optional) SetTxPower dBm | `C0 06 0A <dBm> C0` | `C0 06 F0 C0` |
| (optional) GetPhyExtra check | `C0 06 1D C0` | `C0 06 9D 2B 10 00 C0` |
| (optional) SignalReport on (default already on) | `C0 06 19 01 C0` | `C0 06 9A 01 C0` |

SetSyncWord/SetPreamble may be sent before or after SetRadio; SetRadio no longer resets
the preamble once overridden. LongFast SF11 would get 16 symbols by default anyway, but
sending SetPreamble 16 makes SF7/SF8 presets (ShortFast etc.) correct too.

## Behaviour the host must handle

* **TXDELAY/Persistence defaults**: TXDELAY defaults to 50 (x10 ms = 500 ms) and
  Persistence to 63, SlotTime 10 (100 ms). Both are plain RAM settings the host can set
  to 0 / 255. With P=255 the random draw (`rand <= P`) always passes, but the modem still
  waits while the radio reports RX activity (preamble/header detected), for at most
  1.5x the airtime of a 255-byte packet, then transmits anyway. FullDuplex (`C0 05 01 C0`)
  skips that wait entirely. CAD and RSSI-threshold busy checks are off in the KISS modem.
* **One TX at a time**: a Data frame while one is pending returns `F1 07` (TxBusy) and is
  discarded. Wait for `F8` TxDone (`01` ok / `00` failed or timed out). Data frames with
  0 or >255 bytes are silently ignored (no error, no TxDone).
* `F1 07` is also sent when the modem's 2-frame serial output queue is full; that
  error is not tied to a request.
* **RX**: each received packet is a type `0x00` frame with the raw LoRa payload (1-255
  bytes, untouched, no MeshCore validation), followed by an `F9` RxMeta frame
  (SNR x4 signed, RSSI dBm signed). RxMeta is on by default. Pair it with the
  preceding data frame, but tolerate a missing RxMeta (dropped if the output queue fills
  between the two frames). While host output is backed up the modem stops pulling
  packets from the radio, so a slow reader loses packets.
* **CRC**: CRC is enabled on TX. Packets with payload CRC or header errors are dropped on
  the modem and only counted in GetStats `errors`; they never reach the host.
* Radio settings, sync word, preamble and TX power are not persisted: re-init on every
  connect. GetRadio returns zeros until SetRadio has been sent.
* Unknown sub-commands return `F1 05`, so an unpatched modem is detectable either by
  GetVersion = 1 or by that error on SetSyncWord.
