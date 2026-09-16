# Configuration

[← README](../README.md) · [Hardware](hardware.md) · [Web GUI](web-gui.md) · [Several radios](radios.md) · [MQTT](mqtt.md) · [Architecture](architecture.md)

Almost everything here can be set in the web GUI under **Configuration**, which writes the same
file. You only need to edit YAML by hand for first-time automation or settings the GUI doesn't show.

- **Where it lives:** `/etc/repeatertastic/repeatertastic.yaml` (systemd), `/data/repeatertastic.yaml`
  (Docker), or whatever `-config` / `REPEATERTASTIC_CONFIG` says. A missing file starts with defaults
  and the setup wizard.
- **Reference:** [`deploy/repeatertastic.example.yaml`](../deploy/repeatertastic.example.yaml) has
  every setting with a comment.
- **Format:** YAML. When the GUI saves, it rewrites the file with 4-space indentation and drops comments.
- **Validation:** the daemon refuses to start with an invalid file and says which setting is wrong;
  the GUI refuses to save one.

## Environment variables

These override the file, which suits containers:

| Variable | Overrides | Default |
| --- | --- | --- |
| `REPEATERTASTIC_CONFIG` | config file path | `/etc/repeatertastic/repeatertastic.yaml` (`/data/repeatertastic.yaml` in the image) |
| `REPEATERTASTIC_STATE_DIR` | `state_dir` | `/var/lib/repeatertastic` (`/data` in the image) |
| `REPEATERTASTIC_RADIO_DEVICE` | the main radio's `radio.device` | from the file |
| `REPEATERTASTIC_WEB_PORT` | `web.port` (also used by `repeatertastic healthcheck`) | `8080` |
| `REPEATERTASTIC_MAP_API_KEY` | the map tile key (see below) | the key built into release builds |

## Live or restart?

Most changes apply the moment you save. These need a restart; the GUI shows a banner listing them,
with a **Restart now** button:

- the modem connection (`radio.driver`, `radio.device`, `radio.baud`), once the modem has opened;
  before that, a new device is used at once
- adding or removing a radio
- MQTT connections and UDP multicast
- `web.bind`, `web.port` and `mdns`
- turning a site airtime cap on for a single radio
- anything in `plugins:` except the send limits (enabled, dir, listen, URL installs, entries); the
  send limits apply live from **Plugins → Send limits**
- a restored backup

The GUI's restart shuts down cleanly and exits with status 75, so the service manager starts it again
(`Restart=on-failure` in the systemd unit, `restart: unless-stopped` in Docker).

## Sections

### `radio`: the modem

```yaml
radio:
    driver: kiss          # kiss (a Mesh KISS modem), spi (experimental), meshtastic (a board on Meshtastic firmware), sim (tests), none
    device: /dev/serial/by-id/usb-…-if00-port0
    baud: 115200
```

See [Hardware and modems](hardware.md) for device paths and permissions.

`driver: spi` (experimental) drives the LoRa chip on meshtasticd hardware directly, with no
meshtasticd. It covers SX1262/SX1268/LLCC68, SX1276 (RF95), SX1280 and LR1110/LR1120/LR1121, on
a Linux SPI bus or a CH341 USB stick. `device` is the board:
- a built-in meshtasticd board name, e.g. `MeshAdv-900M30S` (`kisstool boards` lists them);
- a board file path, e.g. `/etc/meshtasticd/config.d/lora-MeshAdv-900M30S.yaml`;
- `auto`, to detect a CH341 stick, a Pi HAT+ or a RAK board EEPROM.

The setup wizard and Configuration → Radios offer the same choices under "Board". See
[LoRa HATs and USB sticks](spi-radio-testing.md).

`driver: meshtastic` uses a board running stock Meshtastic firmware, over USB or the network
(`device: /dev/ttyACM0` or `device: 192.168.1.20`, port 4403 unless given). See
[Boards on Meshtastic firmware](hardware.md#boards-on-meshtastic-firmware). The wizard and
Configuration → Radios offer it under "Meshtastic firmware".

### `mesh`: how the radio joins the mesh

| Setting | Meaning |
| --- | --- |
| `region` | `EU_868`, `US`, `ANZ`, … Sets the frequency plan, power limit and duty cycle |
| `preset` | `LONG_FAST`, `MEDIUM_FAST`, … Must match the mesh you want to join |
| `primary_channel` | Primary channel name; `""` = the preset name (`LongFast`). The name picks the frequency slot, so every identity on the radio shares it |
| `channel_num` | Frequency slot; `0` = derived from the name, as the firmware does |
| `override_frequency_mhz` / `frequency_offset_mhz` | Manual frequency; an override takes the radio off the normal mesh |
| `tx_power_dbm` | Transmit power; `0` = region limit. SX1262 boards max out at 22 dBm |
| `hop_limit` | Hops our packets may travel (1–7, default 3); identities can be capped lower |
| `hw_model` | Hardware identities advertise: `auto` = the modem's board (Heltec V3 → `HELTEC_V3`) |

GUI: **Configuration → Radios → Edit**.

### `relay`: the relay persona

Each radio has one relay persona, the only identity that repeats other nodes' packets. Its role is
a Meshtastic device role, and meshtasticd applies it as-is when the relay runs there
([hosted relay](#hosted-nodes-on-meshtasticd-experimental)).

```yaml
relay:
    role: client          # client · client_base · client_mute · router · router_late · monitor · off
    rebroadcast: all      # all · all_skip_decoding · local_only · known_only · none · core_portnums_only
    long_name: RepeaterTastic Relay
    short_name: RPTR
```

| Role | The relay persona | Identities |
| --- | --- | --- |
| `client` | Repeats like a normal node: after routers, and cancels if another node relays first | Send and receive |
| `client_base` | A client that repeats for its favourited nodes with router priority | Send and receive |
| `client_mute` | Never repeats. `mute`, the old name, still loads and is saved as `client_mute` | Send and receive |
| `router` | Always repeats, with priority; for a well-placed site the mesh relies on | Send and receive |
| `router_late` | Always repeats, but only after other nodes had their chance | Send and receive |
| `monitor` | Never repeats | Receive only: **nothing is transmitted** (no messages, ACKs, NodeInfo or telemetry); sends fail |
| `off` | The radio is ignored: nothing received or sent (the modem stays powered) | Local DMs, links and apps still work |

Switching to monitor or off fails anything still queued. GUI: **Configuration → Relay**, or the
switch in the top bar.

`rebroadcast` is Meshtastic's rebroadcast mode (`device.rebroadcast_mode`) and defaults to `all`.
A relay on meshtasticd honours every mode. The built-in relay only honours `none`; it treats
`client_base` as `client` and `router_late` as `router`. Repeater, tracker, sensor and TAK roles
aren't offered: they make no sense for a relay persona.

![Configuration → Relay: Meshtastic roles and the rebroadcast mode](images/relay-roles.png)

### `airtime`: duty cycle and background traffic

| Setting | Meaning |
| --- | --- |
| `duty_cycle_percent` | Hourly transmit budget; `0` = the region's (EU_868: 10%) |
| `override_duty_cycle` | Ignore the budget entirely. You're responsible for the law |
| `identity_share_percent` | Share of the budget one identity may use before it's flagged "over share" |
| `nodeinfo_interval` | How often each identity announces itself (at least 10m; default 3h) |
| `telemetry_interval` | Relay device telemetry (uptime, channel use, airtime); `0s` = off, at least `30m` |

Periodic broadcasts are skipped while the channel is busy or the radio is past half its budget.

### `position`: fixed site position

```yaml
position:
    latitude: 56.2055
    longitude: -3.1618
    altitude: 90          # metres
    precision_bits: 32    # 32 exact, 16 ≈ 360 m, 13 ≈ 3 km
    interval: 3h          # at least 30m
    identities: relay     # relay (the relay persona broadcasts it) or all
```

Identities can also have their own fixed position (identity editor or the Meshtastic app). GUI:
**Configuration → Position & hardware**.

### `identities`: first start only

```yaml
identities:
    - {long_name: Base Camp, short_name: BASE, api_port: 4403}
```

Created once when the state folder is empty. After that, identities (keys, channels, settings) live in
`state_dir/identities.json` and are managed in the GUI; this list is ignored.

### `links`

| Setting | Meaning |
| --- | --- |
| `local_dm_over_rf` | DMs between identities on this host also go out on air (default: delivered locally) |
| `udp_multicast.enabled` / `group` | Join the LAN mesh of `meshtasticd` nodes (`224.0.0.69` and `239.0.0.69:4403`) |
| `mqtt` | A list of broker connections. See [MQTT](mqtt.md) |

GUI: **Links** (UDP) and **Configuration → MQTT**.

### `web` and map tiles

| Setting | Meaning |
| --- | --- |
| `bind` / `port` | Where the GUI and API listen (default `0.0.0.0:8080`) |
| `session_ttl` | How long a browser login lasts (default 168h) |
| `map_tile_url` | Leaflet tile template for Nodes & map; `""` = CARTO Positron |

The default CARTO basemap needs an API key. Release builds carry one, baked in from the
`CARTO_API_KEY` repository secret. To use your own, set `REPEATERTASTIC_MAP_API_KEY`; `{api_key}` in
the URL is replaced with it. The key is visible to browsers in tile requests, so restrict it on the
provider's side. `tile.openstreetmap.org` refuses browsers on LAN addresses, so don't use it here.

The admin password and API tokens are set in the GUI (**Configuration → Web & API tokens**, and the
account menu) and stored in the state folder, not in this file.

### `mdns`, `log_level`, `state_dir`

- `mdns.enabled` advertises each identity as `_meshtastic._tcp` so the apps can find it (host
  networking in Docker).
- `log_level`: `debug`, `info`, `warn` or `error`; applies live from the GUI.
- `state_dir` holds identity keys, chats, the node database and login data: back it up.

### `hosted`: nodes on meshtasticd (experimental)

```yaml
hosted:
    persona: true                    # the relay of each modem or HAT radio runs on meshtasticd
    identities: true                 # and every identity too (with persona; behind a board, on its own)
    meshtasticd: /usr/bin/meshtasticd   # "" = meshtasticd on PATH; must be 2.8.0 or newer
    docker_image: ""                 # or run it in Docker, e.g. meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian
    port_base: 4500                  # client API ports: radio n (0 = main) uses 100 ports from port_base + 100·n
```

- **Real nodes.** The relay, and with `identities` every identity, becomes a meshtasticd on a
  simulated radio that RepeaterTastic starts, sets up and restarts. They still transmit on this
  radio, at zero hops, and the relay hears the identities without repeating them.
- **Same node numbers.** RepeaterTastic keeps each identity's key and gives it to its meshtasticd,
  so node numbers, chats and app pairings stay. A meshtasticd that loses or changes its key gets it
  back at the next connect; one that won't keep it after three tries stays off air.
- **What RepeaterTastic sets on each node:** region, preset, frequency, power and the primary
  channel name from the radio; hop limit, transmitter (on unless the identity is disabled, or the
  radio is in monitor or off), fixed position and position interval from the identity; the role
  and rebroadcast mode from `relay` for the persona; NodeInfo interval; device telemetry for the
  persona only. A fresh node also gets the identity's names and channels. After that the node
  keeps them, and edits in the GUI are written to it.
- **Roles.** A hosted identity never repeats: its role is `CLIENT_MUTE`, `TRACKER`, `SENSOR` or
  `TAK_TRACKER` (the last three with rebroadcasting off). Any other role becomes `CLIENT_MUTE`.
- **One radio each.** Identities routed across radios (experimental) stay in RepeaterTastic, and a
  hosted identity can't be routed across radios.
- **Apps connect as before**, to the identity's own app port. The meshtasticd API ports are
  RepeaterTastic's.
- **Moving and deleting.** Moving an identity to another radio starts it there, on meshtasticd if
  that radio hosts identities. Deleting one stops its meshtasticd and removes its state.
- **Fallback.** If meshtasticd can't run or is too old, everything stays in RepeaterTastic and
  the log says why.
- **Ports.** An installed meshtasticd listens on every interface: firewall the ports on a shared
  network, or use `docker_image`, which publishes them on 127.0.0.1 only. Each node takes about
  3 MB of memory.
- Takes effect at the next restart. Configuration → Experimental has the same settings.

The setup wizard offers it as an optional step. It shows whether meshtasticd is installed and new
enough, and whether Docker answers and already has the image, then picks the one that works.
Configuration → Experimental shows the same and lists every
instance, and Edit on a radio shows where the relay and each identity run:

![Setup: run nodes on meshtasticd](images/setup-runtimes.png)

![Configuration → Experimental with hosted identities](images/hosted-identities.png)

![Radio settings with hosted nodes](images/radio-settings-hosted-relay.png)

### `radios`, `site` and `experimental`

Extra radios, the site-wide airtime cap and the experimental identities on several radios are
covered in [Several radios](radios.md).

### `plugins`

The plugin system: its folder, the TCP address for attached plugins, URL installs, send budgets and
plugins pinned by the config file. See [Plugins](plugins.md#configuration).

## Backups

**Configuration → Backup & restore → Download** saves one JSON file with the config (including MQTT
passwords) and every radio's identities with their private keys. Store it like a password.

Restoring stages the file and applies it on the next restart, so the running daemon can't save over it.
