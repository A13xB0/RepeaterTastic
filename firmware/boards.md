# RepeaterTastic KISS modem: supported boards

MeshCore KISS modem firmware with `0001-kiss-modem-sync-word-preamble.patch` (upstream `e0031870` + patch). Built with `PARALLEL=3 ./build.sh all`; artifacts land in `firmware/out/<env>/` (build products, not committed). Re-run `./build.sh <env>` to regenerate.

**Build status: 85 OK, 7 FAILED** (of 92 envs).

File size is the file you flash (factory image for ESP32; UF2 for nRF52/RP2040, which is about 2x the binary). App flash used is PlatformIO's figure for the application image.

| Board | Env | MCU / radio | File to flash | File size | App flash used | Flashing method | Build |
|---|---|---|---|---|---|---|---|
| Heltec WiFi LoRa 32 V3 | `Heltec_v3_kiss_modem` | ESP32-S3 / SX1262 | `Heltec_v3_kiss_modem-factory.bin` | 630 KB | 565 KB (17%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec WiFi LoRa 32 V4 | `heltec_v4_kiss_modem` | ESP32-S3 / SX1262 | `heltec_v4_kiss_modem-factory.bin` | 628 KB | 563 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec WiFi LoRa 32 V4 (R8) | `heltec_v4_r8_kiss_modem` | ESP32-S3 / SX1262 | `heltec_v4_r8_kiss_modem-factory.bin` | 631 KB | 567 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec WiFi LoRa 32 V4 (R8, TFT) | `heltec_v4_r8_tft_kiss_modem` | ESP32-S3 / SX1262 | `heltec_v4_r8_tft_kiss_modem-factory.bin` | 631 KB | 567 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec WiFi LoRa 32 V2 | `Heltec_v2_kiss_modem` | ESP32 / SX1276 | `Heltec_v2_kiss_modem-factory.bin` | 558 KB | 493 KB (15%) | esptool `--chip esp32` `write_flash 0x0` | OK |
| Heltec Mesh Node T114 | `Heltec_t114_kiss_modem` | nRF52840 / SX1262 | `Heltec_t114_kiss_modem.uf2` | 607 KB | 303 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Heltec Wireless Tracker | `Heltec_Wireless_Tracker_kiss_modem` | ESP32-S3 / SX1262 | `Heltec_Wireless_Tracker_kiss_modem-factory.bin` | 577 KB | 512 KB (16%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec Wireless Tracker V2 | `heltec_tracker_v2_kiss_modem` | ESP32-S3 / SX1262 | `heltec_tracker_v2_kiss_modem-factory.bin` | 655 KB | 590 KB (18%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec Wireless Paper | `Heltec_Wireless_Paper_kiss_modem` | ESP32-S3 / SX1262 | `Heltec_Wireless_Paper_kiss_modem-factory.bin` | 569 KB | 504 KB (16%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec Vision Master E213 | `Heltec_E213_kiss_modem` | ESP32-S3 / SX1262 | `Heltec_E213_kiss_modem-factory.bin` | 578 KB | 514 KB (8%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec Vision Master E290 | `Heltec_E290_kiss_modem` | ESP32-S3 / SX1262 | `Heltec_E290_kiss_modem-factory.bin` | 578 KB | 514 KB (8%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec Vision Master T190 | `Heltec_T190_kiss_modem` | ESP32-S3 / SX1262 | `Heltec_T190_kiss_modem-factory.bin` | 605 KB | 540 KB (8%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec HT-CT62 | `Heltec_ct62_kiss_modem` | ESP32-C3 / SX1262 | `Heltec_ct62_kiss_modem-factory.bin` | 587 KB | 485 KB (38%) | esptool `--chip esp32c3` `write_flash 0x0` | OK |
| Heltec RC32 | `heltec_rc32_kiss_modem` | ESP32-S3 / SX1262 | `heltec_rc32_kiss_modem-factory.bin` | 631 KB | 566 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Heltec MeshSolar | `Heltec_mesh_solar_kiss_modem` | nRF52840 / SX1262 | `Heltec_mesh_solar_kiss_modem.uf2` | 600 KB | 299 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Heltec Mesh Tower V2 | `Heltec_tower_v2_kiss_modem` | nRF52840 / SX1262 | `Heltec_tower_v2_kiss_modem.uf2` | 488 KB | 244 KB (31%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Heltec T1 | `Heltec_t1_kiss_modem` | nRF52840 / SX1262 | `Heltec_t1_kiss_modem.uf2` | 662 KB | 330 KB (42%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Heltec T096 | `Heltec_t096_kiss_modem` | nRF52840 / SX1262 | `Heltec_t096_kiss_modem.uf2` | 663 KB | 331 KB (42%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Heltec MeshPocket | `Mesh_pocket_kiss_modem` | nRF52840 / SX1262 | `Mesh_pocket_kiss_modem.uf2` | 523 KB | 261 KB (33%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed XIAO nRF52840 + Wio-SX1262 kit | `Xiao_nrf52_kiss_modem` | nRF52840 / SX1262 | `Xiao_nrf52_kiss_modem.uf2` | 598 KB | 298 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed XIAO ESP32-S3 + Wio-SX1262 kit | `Xiao_S3_WIO_kiss_modem` | ESP32-S3 / SX1262 | `Xiao_S3_WIO_kiss_modem-factory.bin` | 600 KB | 536 KB (16%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Seeed XIAO ESP32-S3 (generic SX1262 wiring) | `Xiao_S3_kiss_modem` | ESP32-S3 / SX1262 | `Xiao_S3_kiss_modem-factory.bin` | 610 KB | 545 KB (17%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Seeed XIAO ESP32-C3 + SX1262 | `Xiao_C3_kiss_modem` | ESP32-C3 / SX1262 | `Xiao_C3_kiss_modem-factory.bin` | 564 KB | 462 KB (36%) | esptool `--chip esp32c3` `write_flash 0x0` | OK |
| Seeed XIAO ESP32-C6 + Wio-SX1262 | `Xiao_C6_kiss_modem` | ESP32-C6 / SX1262 | `Xiao_C6_kiss_modem-factory.bin` | 557 KB | 461 KB (24%) | esptool `--chip esp32c6` `write_flash 0x0` | OK |
| Seeed XIAO RP2040 + SX1262 | `Xiao_rp2040_kiss_modem` | RP2040 / SX1262 | `Xiao_rp2040_kiss_modem.uf2` | 381 KB | 178 KB (12%) | UF2: hold BOOTSEL + plug in, copy to RPI-RP2 | OK |
| Seeed SenseCAP T1000-E | `t1000e_kiss_modem` | nRF52840 / LR1110 | `t1000e_kiss_modem.uf2` | 456 KB | 228 KB (29%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed Wio Tracker L1 | `WioTrackerL1_kiss_modem` | nRF52840 / SX1262 | `WioTrackerL1_kiss_modem.uf2` | 604 KB | 302 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed Wio Tracker L1 E-Ink | `WioTrackerL1Eink_kiss_modem` | nRF52840 / SX1262 | `WioTrackerL1Eink_kiss_modem.uf2` | 604 KB | 302 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed Wio-WM1110 dev kit | `wio_wm1110_kiss_modem` | nRF52840 / LR1110 | `wio_wm1110_kiss_modem.uf2` | 565 KB | 282 KB (36%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed SenseCAP Solar Node P1 | `SenseCap_Solar_kiss_modem` | nRF52840 / SX1262 | `SenseCap_Solar_kiss_modem.uf2` | 606 KB | 303 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Seeed Mesh Tracker X1 | `MeshTracker_X1_kiss_modem` | nRF52840 / LR2021 | - | - | - | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | FAILED: upstream env config: variant target.cpp needs MicroNMEA but the kiss env does not add the GPS lib_deps |
| Seeed LoRa-E5 dev board | `wio-e5_kiss_modem` | STM32WLE5 / STM32WL (SX126x) | - | - | - | STM32CubeProgrammer (SWD) at 0x08000000 | FAILED: upstream/toolchain: unpinned ststm32 platform now ships framework-arduinoststm32 3.0.0 without ltoa() (TxtDataHelpers.cpp) |
| Seeed LoRa-E5 mini | `wio-e5-mini_kiss_modem` | STM32WLE5 / STM32WL (SX126x) | - | - | - | STM32CubeProgrammer (SWD) at 0x08000000 | FAILED: upstream/toolchain: unpinned ststm32 platform now ships framework-arduinoststm32 3.0.0 without ltoa() (TxtDataHelpers.cpp) |
| RAK WisBlock RAK4631 | `RAK_4631_kiss_modem` | nRF52840 / SX1262 | `RAK_4631_kiss_modem.uf2` | 725 KB | 362 KB (46%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| RAK WisBlock RAK3401 (1W) | `RAK_3401_kiss_modem` | nRF52840 / SX1262 | `RAK_3401_kiss_modem.uf2` | 606 KB | 302 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| RAK WisBlock RAK3112 | `RAK_3112_kiss_modem` | ESP32-S3 / SX1262 | `RAK_3112_kiss_modem-factory.bin` | 635 KB | 570 KB (18%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| RAK WisMesh Tag | `RAK_WisMesh_Tag_kiss_modem` | nRF52840 / SX1262 | `RAK_WisMesh_Tag_kiss_modem.uf2` | 606 KB | 303 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| RAK WisBlock RAK11310 | `RAK_11310_kiss_modem` | RP2040 / SX1262 | `RAK_11310_kiss_modem.uf2` | 382 KB | 179 KB (12%) | UF2: hold BOOTSEL + plug in, copy to RPI-RP2 | OK |
| RAK3172 / RAK3272 | `RAK_3x72_kiss_modem` | STM32WLE5 / STM32WL (SX126x) | - | - | - | STM32CubeProgrammer (SWD) at 0x08000000 | FAILED: upstream/toolchain: unpinned ststm32 platform now ships framework-arduinoststm32 3.0.0 without ltoa() (TxtDataHelpers.cpp) |
| LilyGo T-Beam (SX1262) | `Tbeam_SX1262_kiss_modem` | ESP32 / SX1262 | `Tbeam_SX1262_kiss_modem-factory.bin` | 617 KB | 552 KB (29%) | esptool `--chip esp32` `write_flash 0x0` | OK |
| LilyGo T-Beam (SX1276) | `Tbeam_SX1276_kiss_modem` | ESP32 / SX1276 | `Tbeam_SX1276_kiss_modem-factory.bin` | 613 KB | 548 KB (29%) | esptool `--chip esp32` `write_flash 0x0` | OK |
| LilyGo T-Beam Supreme (SX1262) | `T_Beam_S3_Supreme_SX1262_kiss_modem` | ESP32-S3 / SX1262 | `T_Beam_S3_Supreme_SX1262_kiss_modem-factory.bin` | 619 KB | 555 KB (17%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| LilyGo T-Beam 1W | `LilyGo_TBeam_1W_kiss_modem` | ESP32-S3 / SX1262 | `LilyGo_TBeam_1W_kiss_modem-factory.bin` | 612 KB | 548 KB (43%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| LilyGo T3-S3 (SX1262) | `LilyGo_T3S3_sx1262_kiss_modem` | ESP32-S3 / SX1262 | `LilyGo_T3S3_sx1262_kiss_modem-factory.bin` | 559 KB | 495 KB (39%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| LilyGo T3-S3 (SX1276) | `LilyGo_T3S3_sx1276_kiss_modem` | ESP32-S3 / SX1276 | `LilyGo_T3S3_sx1276_kiss_modem-factory.bin` | 557 KB | 493 KB (38%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| LilyGo T-LoRa V2.1-1.6 | `LilyGo_TLora_V2_1_1_6_kiss_modem` | ESP32 / SX1276 | `LilyGo_TLora_V2_1_1_6_kiss_modem-factory.bin` | 906 KB | 842 KB (44%) | esptool `--chip esp32` `write_flash 0x0` | OK |
| LilyGo T-LoRa C6 | `LilyGo_Tlora_C6_kiss_modem` | ESP32-C6 / SX1262 | `LilyGo_Tlora_C6_kiss_modem-factory.bin` | 557 KB | 461 KB (24%) | esptool `--chip esp32c6` `write_flash 0x0` | OK |
| LilyGo T-Echo | `LilyGo_T-Echo_kiss_modem` | nRF52840 / SX1262 | `LilyGo_T-Echo_kiss_modem.uf2` | 565 KB | 282 KB (36%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| LilyGo T-Echo Lite | `LilyGo_T-Echo-Lite_kiss_modem` | nRF52840 / SX1262 | `LilyGo_T-Echo-Lite_kiss_modem.uf2` | 537 KB | 268 KB (34%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| LilyGo T-Echo Card | `LilyGo_T-Echo_Card_kiss_modem` | nRF52840 / SX1262 | `LilyGo_T-Echo_Card_kiss_modem.uf2` | 507 KB | 253 KB (32%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| LilyGo T-Deck | `LilyGo_TDeck_kiss_modem` | ESP32-S3 / SX1262 | `LilyGo_TDeck_kiss_modem-factory.bin` | 613 KB | 548 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| LilyGo T-ETH Elite (SX1262) | `LilyGo_TETH_Elite_sx1262_kiss_modem` | ESP32-S3 / SX1262 | `LilyGo_TETH_Elite_sx1262_kiss_modem-factory.bin` | 541 KB | 477 KB (8%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| LilyGo T-Impulse Plus | `LilyGo_T_Impulse_Plus_kiss_modem` | nRF52840 / SX1262 | `LilyGo_T_Impulse_Plus_kiss_modem.uf2` | 604 KB | 302 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Elecrow ThinkNode M1 | `ThinkNode_M1_kiss_modem` | nRF52840 / SX1262 | `ThinkNode_M1_kiss_modem.uf2` | 483 KB | 241 KB (30%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Elecrow ThinkNode M2 | `ThinkNode_M2_kiss_modem` | ESP32-S3 / SX1262 | `ThinkNode_M2_kiss_modem-factory.bin` | 572 KB | 507 KB (40%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Elecrow ThinkNode M3 | `ThinkNode_M3_kiss_modem` | nRF52840 / LR1110 | `ThinkNode_M3_kiss_modem.uf2` | 474 KB | 237 KB (30%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Elecrow ThinkNode M5 | `ThinkNode_M5_kiss_modem` | ESP32-S3 / SX1262 | `ThinkNode_M5_kiss_modem-factory.bin` | 588 KB | 524 KB (41%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Elecrow ThinkNode M6 | `ThinkNode_M6_kiss_modem` | nRF52840 / SX1262 | `ThinkNode_M6_kiss_modem.uf2` | 604 KB | 301 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Elecrow ThinkNode M7 | `ThinkNode_M7_kiss_modem` | ESP32-S3 / LR1110 | `ThinkNode_M7_kiss_modem-factory.bin` | 554 KB | 489 KB (15%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Elecrow ThinkNode M8 | `ThinkNode_M8_kiss_modem` | nRF52840 / SX1262 | - | - | - | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | FAILED: upstream env config: link error, EnvironmentSensorManager not in the kiss env's build_src_filter |
| Elecrow ThinkNode M9 | `ThinkNode_M9_kiss_modem` | ESP32-S3 / LR1110 | `ThinkNode_M9_kiss_modem-factory.bin` | 649 KB | 585 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Waveshare RP2040-LoRa | `waveshare_rp2040_lora_kiss_modem` | RP2040 / SX1262 | `waveshare_rp2040_lora_kiss_modem.uf2` | 383 KB | 179 KB (12%) | UF2: hold BOOTSEL + plug in, copy to RPI-RP2 | OK |
| Raspberry Pi Pico W + SX1262 | `PicoW_kiss_modem` | RP2040 / SX1262 | `PicoW_kiss_modem.uf2` | 883 KB | 429 KB (28%) | UF2: hold BOOTSEL + plug in, copy to RPI-RP2 | OK |
| ProMicro nRF52840 (Faketec) + SX1262 | `ProMicro_kiss_modem` | nRF52840 / SX1262 | `ProMicro_kiss_modem.uf2` | 528 KB | 263 KB (33%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| B&Q Nano G2 Ultra | `Nano_G2_Ultra_kiss_modem` | nRF52840 / SX1262 | `Nano_G2_Ultra_kiss_modem.uf2` | 483 KB | 241 KB (30%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Meshtiny | `Meshtiny_kiss_modem` | nRF52840 / SX1262 | `Meshtiny_kiss_modem.uf2` | 474 KB | 236 KB (30%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| muzi works R1 Neo | `R1Neo_kiss_modem` | nRF52840 / SX1262 | `R1Neo_kiss_modem.uf2` | 608 KB | 304 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Minewsemi ME25LS01 | `Minewsemi_me25ls01_kiss_modem` | nRF52840 / LR1110 | `Minewsemi_me25ls01_kiss_modem.uf2` | 477 KB | 238 KB (30%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| Keepteen LT1 | `KeepteenLT1_kiss_modem` | nRF52840 / SX1262 | `KeepteenLT1_kiss_modem.uf2` | 483 KB | 241 KB (30%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Stick nRF (22 dBm) | `ikoka_stick_nrf_22dbm_kiss_modem` | nRF52840 / SX1262 | `ikoka_stick_nrf_22dbm_kiss_modem.uf2` | 620 KB | 309 KB (39%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Stick nRF (30 dBm) | `ikoka_stick_nrf_30dbm_kiss_modem` | nRF52840 / SX1262 | `ikoka_stick_nrf_30dbm_kiss_modem.uf2` | 620 KB | 309 KB (39%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Stick nRF (33 dBm) | `ikoka_stick_nrf_33dbm_kiss_modem` | nRF52840 / SX1262 | `ikoka_stick_nrf_33dbm_kiss_modem.uf2` | 620 KB | 309 KB (39%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Nano nRF (22 dBm) | `ikoka_nano_nrf_22dbm_kiss_modem` | nRF52840 / SX1262 | `ikoka_nano_nrf_22dbm_kiss_modem.uf2` | 594 KB | 297 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Nano nRF (30 dBm) | `ikoka_nano_nrf_30dbm_kiss_modem` | nRF52840 / SX1262 | `ikoka_nano_nrf_30dbm_kiss_modem.uf2` | 594 KB | 297 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Nano nRF (33 dBm) | `ikoka_nano_nrf_33dbm_kiss_modem` | nRF52840 / SX1262 | `ikoka_nano_nrf_33dbm_kiss_modem.uf2` | 594 KB | 297 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| ikoka Handheld nRF | `ikoka_handheld_nrf_kiss_modem` | nRF52840 / SX1262 | `ikoka_handheld_nrf_kiss_modem.uf2` | 593 KB | 296 KB (37%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| GAT562 30S Mesh Kit | `GAT562_30S_Mesh_Kit_kiss_modem` | nRF52840 / SX1262 | `GAT562_30S_Mesh_Kit_kiss_modem.uf2` | 665 KB | 332 KB (42%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| GAT562 Mesh EVB Pro | `GAT562_Mesh_EVB_Pro_kiss_modem` | nRF52840 / SX1262 | `GAT562_Mesh_EVB_Pro_kiss_modem.uf2` | 606 KB | 302 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| GAT562 Mesh Tracker Pro | `GAT562_Mesh_Tracker_Pro_kiss_modem` | nRF52840 / SX1262 | `GAT562_Mesh_Tracker_Pro_kiss_modem.uf2` | 606 KB | 302 KB (38%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| GAT562 Mesh Watch13 | `GAT562_Mesh_Watch13_kiss_modem` | nRF52840 / SX1262 | `GAT562_Mesh_Watch13_kiss_modem.uf2` | 594 KB | 297 KB (37%) | UF2: double-tap reset, copy to USB drive (or nrfutil DFU `-dfu.zip`) | OK |
| B&Q Station G2 | `Station_G2_kiss_modem` | ESP32-S3 / SX1262 | `Station_G2_kiss_modem-factory.bin` | 647 KB | 582 KB (46%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| B&Q Station G3 (ESP32) | `Station_G3_ESP32_kiss_modem` | ESP32-S3 / SX1262 | `Station_G3_ESP32_kiss_modem-factory.bin` | 677 KB | 613 KB (48%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Tenstar ESP32-C3 (SX1262) | `Tenstar_C3_sx1262_kiss_modem` | ESP32-C3 / SX1262 | `Tenstar_C3_sx1262_kiss_modem-factory.bin` | 588 KB | 486 KB (38%) | esptool `--chip esp32c3` `write_flash 0x0` | OK |
| Tenstar ESP32-C3 (SX1268) | `Tenstar_C3_sx1268_kiss_modem` | ESP32-C3 / SX1268 | `Tenstar_C3_sx1268_kiss_modem-factory.bin` | 588 KB | 486 KB (38%) | esptool `--chip esp32c3` `write_flash 0x0` | OK |
| MeshAdventurer (SX1262) | `Meshadventurer_sx1262_kiss_modem` | ESP32 / SX1262 | `Meshadventurer_sx1262_kiss_modem-factory.bin` | 580 KB | 516 KB (27%) | esptool `--chip esp32` `write_flash 0x0` | OK |
| MeshAdventurer (SX1268) | `Meshadventurer_sx1268_kiss_modem` | ESP32 / SX1268 | `Meshadventurer_sx1268_kiss_modem-factory.bin` | 580 KB | 515 KB (27%) | esptool `--chip esp32` `write_flash 0x0` | OK |
| Generic ESP32 + Ebyte E22 | `Generic_E22_kiss_modem` | ESP32 / SX1262 | - | - | - | esptool `--chip esp32` `write_flash 0x0` | FAILED: upstream env config: base Generic_E22 env defines no RADIO_CLASS/LORA_TX_POWER (only the _sx1262/_sx1268 role envs do) |
| Ebyte EoRa-S3 | `Ebyte_EoRa-S3_kiss_modem` | ESP32-S3 / SX1262 | `Ebyte_EoRa-S3_kiss_modem-factory.bin` | 559 KB | 495 KB (39%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Meshnology W12 | `meshnology_w12_kiss_modem` | ESP32-S3 / LR2021 | `meshnology_w12_kiss_modem-factory.bin` | 638 KB | 573 KB (9%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| Nibble Screen Connect | `nibble_screen_connect_kiss_modem_` | ESP32-S3 / SX1262 | `nibble_screen_connect_kiss_modem_-factory.bin` | 547 KB | 483 KB (38%) | esptool `--chip esp32s3` `write_flash 0x0` | OK |
| M5Stack Unit C6L | `M5Stack_Unit_C6L_kiss_modem` | ESP32-C6 / SX1262 | `M5Stack_Unit_C6L_kiss_modem-factory.bin` | 564 KB | 468 KB (24%) | esptool `--chip esp32c6` `write_flash 0x0` | OK |
| Tiny Relay | `Tiny_Relay_kiss_modem` | STM32WLE5 / STM32WL (SX126x) | - | - | - | STM32CubeProgrammer (SWD) at 0x08000000 | FAILED: upstream/toolchain: unpinned ststm32 platform now ships framework-arduinoststm32 3.0.0 without ltoa() (TxtDataHelpers.cpp) |

Notes: `-dfu.zip` and `.hex` are also produced for nRF52 boards, `<env>-app.bin` for ESP32.
The three ESP32-C6 envs build with a separate PlatformIO core dir (`core=pioarduino` in
`boards.txt`). Every upstream `*_kiss_modem` env is listed. Build status is compile-only:
none of these images has been tested on hardware yet. Failed envs fail identically without the
patch (upstream env/toolchain issues, not the sync word / preamble changes).

## Flashing methods

Replace `PORT` with the board's serial port (`/dev/ttyUSB0` for CP210x/CH340 bridges,
`/dev/ttyACM0` for native USB; `COMx` on Windows). Commands assume you are in `firmware/out/<env>/`.

### ESP32 family (esptool)

Fresh install, whole flash (bootloader + partitions + app), erases the stored identity:

```
esptool.py --chip esp32s3 --port PORT erase_flash
esptool.py --chip esp32s3 --port PORT --baud 921600 write_flash 0x0 <env>-factory.bin
```

Use `--chip esp32` for ESP32 boards (Heltec V2, T-Beam, T-LoRa V2.1, MeshAdventurer, E22),
`esp32c3` / `esp32c6` for the C3 / C6 boards. To update an existing MeshCore install
without touching the partition table / identity, flash only the app:
`esptool.py --chip <chip> --port PORT write_flash 0x10000 <env>-app.bin`.
If the port does not answer, hold BOOT/PRG, tap RESET, release BOOT.

### nRF52840 (UF2 or DFU)

Boards with the Adafruit nRF52 bootloader (RAK, Heltec T114, XIAO nRF52840, T-Echo, T1000-E,
ThinkNode, ProMicro, ...): double-tap RESET (T1000-E: hold the button while plugging in USB), a USB
drive appears (e.g. `RAK4631`, `XIAO-SENSE`, `T1000-E`, `NICENANO`), copy `<env>.uf2` onto it; the
board reboots when the copy finishes.

Serial DFU alternative (same bootloader, no drive needed):

```
pip install adafruit-nrfutil
adafruit-nrfutil --verbose dfu serial --package <env>-dfu.zip -p PORT -b 115200 --singlebank --touch 1200
```

### RP2040 (UF2)

Hold BOOTSEL (BOOT on RAK11310 / XIAO RP2040) while connecting USB or pressing RESET; a drive
`RPI-RP2` appears; copy `<env>.uf2` onto it.

### STM32WL (Wio-E5, RAK3172, Tiny Relay)

No USB bootloader. Flash `<env>.bin` over SWD with an ST-Link:

```
STM32_Programmer_CLI -c port=SWD -w <env>.bin 0x08000000 -v -rst
```

or use the UART ROM bootloader (BOOT0 high) with `stm32flash -w <env>.bin -v -g 0x0 PORT`.
(These four envs currently fail to build; see the table.)
