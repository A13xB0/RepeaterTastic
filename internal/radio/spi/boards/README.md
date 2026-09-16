# Built-in board files

These are meshtasticd's LoRa board files, copied unchanged from
[meshtastic/firmware](https://github.com/meshtastic/firmware) `bin/config.d/lora-*.yaml`
(GPL-3.0, the same licence as RepeaterTastic) at commit
`3468af94aa0f79e93c9bf041244bf230161fc704`.

One file is ours, not meshtasticd's: `lora-usb-xiao-sx1262-ch341.yaml`, for a XIAO nRF52840 +
Wio-SX1262 running the CH341 bridge firmware (its USB product string `XIAO-SX1262-CH341` makes
`auto` pick it).

They let `radio.driver: spi` find a board by file name, or by detection (`radio.device: auto`),
without meshtasticd installed. Files in `/etc/meshtasticd/config.d` and `available.d` take
precedence. To refresh, copy the directory again from a newer firmware checkout and update the
commit above.
