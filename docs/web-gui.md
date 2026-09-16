# Using the web GUI

[← README](../README.md) · [Hardware](hardware.md) · [Configuration](configuration.md) · [Several radios](radios.md) · [MQTT](mqtt.md) · [Plugins](plugins.md) · [HTTP API](api.md) · [Architecture](architecture.md)

Open `http://<host>:8080`. The first visit runs the setup wizard: the modem, region and preset,
the relay role (a Meshtastic role such as client, router or client mute) and the admin password. The **account menu** (top right)
changes the password, signs out, or signs every browser out.

Pages update live. A tab left in the background gives up its live connection after 15 seconds, so
many open tabs don't use up the browser's connections to the host, and catches up when shown again.

## Top bar

- **Radio switcher** (with more than one radio): every page and the Relay, Airtime, Position and
  MQTT settings follow the radio picked here.
- **Modem state, frequency and preset**, and the **airtime gauge**: this hour's transmit time against
  the duty-cycle budget.
- **Relay switch:** the Meshtastic role of this radio's relay persona (client, client base, client
  mute, router or router late), then **Monitor** (the
  radio only listens and transmits nothing) and **Off** (the radio is ignored). Both ask before
  switching. In either, sends from identities, the relay persona and plugins fail with "the radio
  isn't transmitting". The same switch is under Configuration → Relay, with the rebroadcast mode.
- A **restart banner** appears under the bar when saved changes need a restart, listing them.

## Pages

| Page | What it's for |
| --- | --- |
| **Dashboard** | Radio health, noise floor, airtime, traffic and recent activity at a glance |
| **Identities** | Create, import, edit, move and delete virtual nodes. Each shows its app port, connected apps, airtime and channels. The relay persona is created for you and can't be deleted |
| **Chat** | Channel conversations and DMs for any identity, the relay persona included (**Speaking as**), with delivery ticks |
| **Channels** | Every identity's eight channel slots: add, edit and remove channels, or add one to several identities at once |
| **Nodes & map** | Nodes heard, with signal, hops and position on a map. A node's drawer sends a traceroute, NodeInfo request or message from the identity picked in **Send from** |
| **Packets** | Live packet log with decoded summaries |
| **Statistics** | Airtime per identity, traffic and RF history |
| **Links** | UDP multicast and each MQTT connection's state and counters |
| **Configuration** | Radios, Relay, Airtime & duty, Position & hardware, MQTT, Web & API tokens, Experimental, Backup & restore |
| **Plugins** | Install, attach, enable and configure plugins; their status, log and panel; **Send limits** for every plugin ([Plugins](plugins.md)) |
| **Logs** | The daemon's log, live |

## Common jobs

### Create an identity and connect the app

1. **Identities → New identity**: a name, a short name and a role (`CLIENT_MUTE` by default, so it
   doesn't repeat). A port is suggested.
2. The home radio is where it lives. With several radios you can pick it here or move it later.
3. In the Meshtastic app: **Connect → Network → `<host>:<port>`**. The app sees a normal node with
   this identity's channels and nodes.

**Import key** brings an existing node's identity across. Turn the original device off first.

### Add a channel

**Channels** → **+** on an empty slot. Choose an existing channel (so identities hear each other) or a
new one with a random, default, pasted or no key, and whether it goes to MQTT. **Add channel to
identities** does the same for several identities, each in its first free slot. Click a slot to edit
it, or **×** to remove it. The QR button shares or imports a `meshtastic.org/e/#…` channel URL.

Slot 0 is the primary channel. It's shared by every identity on a radio because its name picks the
frequency; change it under Configuration → Radios → Edit.

### Move an identity to another radio

Identities → **Edit** → **Home radio**. Its key, node ID, app port and chats move with it; its
primary channel becomes the new radio's. Connected apps reconnect, and unsent messages are marked failed.

### Add a radio, MQTT, a position

- **Configuration → Radios → Add radio** ([Several radios](radios.md)).
- **Configuration → MQTT → Add connection** ([MQTT](mqtt.md)).
- **Configuration → Position & hardware** for the site position, broadcast interval and advertised hardware.

### API tokens and backups

- **Configuration → Web & API tokens** creates tokens for scripts and Home Assistant
  (`Authorization: Bearer …`, see [the API](api.md)).
- **Configuration → Backup & restore** downloads everything, or restores a backup at the next restart.

## Experimental

**Configuration → Experimental → Identities on several radios** lets an identity use more than one
radio. With it on, the channel slot dialog gets a **Radio** choice, the identity editor gets a
**Default radio** and DM routing, and the Channels page shows each slot's radio. See
[Several radios](radios.md#experimental-identities-on-several-radios).
