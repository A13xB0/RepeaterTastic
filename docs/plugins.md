# Plugins

A plugin is a separate program that extends RepeaterTastic. Examples: an uploader that sends what
the site hears to a mapping service, a bot that answers commands, or a dashboard. A plugin talks to
RepeaterTastic over the **Plugin API**, which is gRPC over a local socket. It only sees and does what
the operator allows, and it can't take the daemon down with it.

- [Installing](#installing): GUI upload or URL, dropping a bundle into a folder, the command line,
  or Docker
- [Enabling and permissions](#enabling-and-permissions)
- [Settings](#settings), [status, log and panel](#status-log-and-panel)
- [Attached plugins](#attached-plugins) that run in another container or on another machine
- [Configuration](#configuration): the `plugins:` section
- [Writing a plugin](#writing-a-plugin): the bundle, `plugin.yaml`, the API, the Go SDK and the
  example

## Installing

A plugin comes as a **bundle**: a `.zip` holding a `plugin.yaml`, its program (usually one
binary per CPU type), and optionally a logo and a web panel. A new plugin is installed **switched
off**.

| How | What to do |
| --- | --- |
| GUI | **Plugins → Install plugin**, then drop the .zip or paste a URL |
| Folder | Copy the .zip into the plugins folder's `inbox/` (`<state_dir>/plugins/inbox/`, or `<plugins.dir>/inbox/` when `plugins.dir` is set). It installs within a few seconds; a bundle that can't be used moves to `inbox/.rejected/` next to a `.error.txt` giving the reason |
| Command line | `sudo -u repeatertastic repeatertastic plugin install hello-plugin.zip` (a file or an `http(s)://` URL) |
| Docker | `docker cp hello-plugin.zip repeatertastic:/data/plugins/inbox/` |

Installing a bundle with the same `id` upgrades the plugin. The upgrade keeps the plugin's
switch, settings and granted permissions and restarts it. If the new version asks for permissions
you haven't reviewed, it waits in **needs review** until you look at them.

Bundles are checked before anything is written. RepeaterTastic refuses a bundle that:

- has paths outside the bundle, links, or device files;
- is more than 100 MB zipped or 250 MB unpacked;
- has a `plugin.yaml` that doesn't validate;
- has no program for this machine's OS and CPU.

## Enabling and permissions

Turning a plugin on shows what it asks for. Untick anything you don't want: a plugin that calls
for a permission it wasn't granted gets a "permission denied" error.

| Permission | Lets the plugin |
| --- | --- |
| `packets.read` | See every packet the radios hear and send, decoded where RepeaterTastic can read it |
| `nodes.read` | See the node database (and traceroute results) |
| `messages.read` | Read text messages to and from each radio's relay persona |
| `messages.send` | Send text messages from each radio's relay persona |
| `traceroute.send` | Send traceroutes from the identity chosen in the plugin's settings on that radio, or the radio's relay persona when none is chosen |

Plugins act as the **relay persona** of each radio: the node the site already is on the mesh.
Transmissions go through the normal transmit queue and duty cycle. Each plugin also has a budget,
30 messages and 12 traceroutes an hour by default. A plugin may send a sixth of its hourly budget
at once (at least one), then the budget refills evenly: with 12 traceroutes an hour, that's 2
straight away and then one every 5 minutes. Change them under **Plugins → Send limits**, which
applies at once and saves them to the config file as `plugins.messages_per_hour` and
`traceroutes_per_hour`. The limits are 0-600 messages and 0-120 traceroutes an hour; `0` stops
plugins sending at all.
Every send is written to the plugin's log. The radio's own limits still apply too, such as one
traceroute per identity every 30 seconds.

A plugin's `network` list (shown before you enable it) names the services it talks to. It is a
declaration, not a firewall: a plugin is a program running as the RepeaterTastic user, so only
install plugins you trust.

## Settings

The **Settings** tab is a form built from the plugin's `plugin.yaml`. Secrets are write-only:
the API and GUI show that one is saved, never its value. Changes reach a running plugin at once.
Settings and grants are kept in `<state_dir>/plugins/state.json` (mode 0600).

## Status, log and panel

- **Status**: a one-line summary on the plugin's card, for example "Uploading · 1,204 packets
  today", plus up to 40 label/value fields on its page (a status with more is ignored).
- **Log**: what the plugin printed or reported, and what RepeaterTastic did with it (started,
  crashed, sent a message). The last 1000 lines are kept.
- **Panel**: a plugin may ship its own page. It runs in a sandboxed frame on the plugin's page. It
  has no access to the GUI, your login or the API, and exchanges messages with the GUI only through
  `postMessage` (see [Panels](#panels)).

A managed plugin that exits is restarted with backoff (1 s up to a minute). After five exits
within 15 seconds of starting, it is left **crashed** until you press **Try again**.

## Attached plugins

A plugin can also run somewhere else and connect in over TCP, for example a Python bot in another
container.

1. Set `plugins.listen` (for example `127.0.0.1:4450`, or a LAN address) and restart.
2. **Plugins → Attach**: give its id, name and permissions. The token is shown **once**.
3. Start the plugin with `RT_PLUGIN_ID` (exactly the id you attached), `RT_PLUGIN_ADDR=<host>:4450`
   and `RT_PLUGIN_TOKEN`.

What to expect:

- The id in `Hello`, and in the `plugin.yaml` the plugin sends, must match the attached id, or
  the session is refused.
- The first time it connects, the plugin sends its `plugin.yaml`, and its settings form and
  permissions appear. RepeaterTastic then **refuses the session** until required settings are
  filled in and any permissions it asks for beyond the ones granted at attach are reviewed. An
  attached plugin should keep retrying with a backoff.
- Attached plugins get no data folder (`Welcome.data_dir` is empty); they keep their own state.
- The TCP connection isn't encrypted. Keep it on localhost, a private network or a VPN.
- **New token** on the plugin's page replaces the token and disconnects the old one.

## Configuration

```yaml
plugins:
    enabled: true                 # the plugin system
    dir: ""                       # "" = <state_dir>/plugins
    listen: ""                    # TCP address for attached plugins, e.g. 127.0.0.1:4450 ("" = off)
    allow_url_install: true       # the GUI may download bundles from a URL
    messages_per_hour: 30         # per plugin, 0-600; 0 = plugins may not send messages (also Plugins → Send limits)
    traceroutes_per_hour: 12      # per plugin, 0-120; 0 = plugins may not send traceroutes
    entries:                      # pin plugins: the GUI shows these as "config file" and won't change them
        - id: my-plugin
          enabled: true
          permissions: [packets.read, nodes.read, traceroute.send]
          settings:
              api_key: ${MY_PLUGIN_API_KEY}  # ${VAR} is read from the environment
```

Pinned entries still need the plugin installed. Changes to the `plugins:` section need a restart
(the GUI's restart banner lists them). The folder looks like this:

```
plugins/
  host.sock            the Plugin API socket for managed plugins
  state.json           switches, grants and settings
  inbox/               drop bundles here (.rejected/ holds refused ones)
  installed/<id>/      unpacked bundles
  data/<id>/           each plugin's own folder (its HOME and RT_PLUGIN_DATA)
```

### Command line

```
repeatertastic plugin [-config <file>] <command>     -config defaults to /etc/repeatertastic/repeatertastic.yaml ($REPEATERTASTIC_CONFIG)
repeatertastic plugin list                          installed plugins, on or off, granted permissions
repeatertastic plugin install <bundle.zip | http(s) URL>
repeatertastic plugin enable <id> [permission ...]  "all" grants everything the plugin asks for
repeatertastic plugin disable <id>
repeatertastic plugin remove <id> [-keep-data]
repeatertastic plugin permissions
```

Run the command as the user RepeaterTastic runs as. A running daemon picks up changes within a few
seconds; `install` hands the bundle to it through the inbox.

## Writing a plugin

### The bundle

```
plugin.yaml
logo.svg                 PNG, SVG or WebP
bin/hello-linux-arm64    one static binary per target
bin/hello-linux-arm
bin/hello-linux-amd64
panel/index.html         optional
```

`scripts/bundle-plugin.sh <dir> <go package> <out.zip>` cross-compiles and zips a Go plugin;
`make plugin-example` builds [the example](../examples/plugins/hello).

### `plugin.yaml`

```yaml
id: hello                  # 2-40 lowercase letters, digits, dashes; never change it
name: Hello Mesh
version: 1.0.0
api: 1                     # Plugin API version
description: Counts what each radio hears.
author: ScotMesh
homepage: https://github.com/…
license: GPL-3.0-or-later
logo: logo.svg
permissions: [packets.read, nodes.read]
network: [api.example.org] # services it talks to, shown before enabling
settings:
  - key: api_key           # lowercase, digits, underscores
    label: API key
    type: secret           # string, secret, url, bool, int, number, select, multiselect, radios
    required: true
    help: From your account page.
  - key: region
    type: select
    options: [scotland, england]
    default: scotland
  - key: radios
    label: Radios
    type: radios           # tick boxes of the site's radios; the value is a list of radio IDs
    placeholder: All radios  # what an empty list shows as
  - key: ports
    type: multiselect      # tick boxes of options; the value is a list
    options: [TEXT_MESSAGE_APP, POSITION_APP]
  - key: report_as
    type: identities       # tick boxes of the site's identities; the value is a list of node IDs
run:
  managed:
    exec: bin/hello-{os}-{arch}   # {os} and {arch} are Go's GOOS and GOARCH (arm for 32-bit Pis)
    args: []
ui:
  panel: panel/index.html
```

### Running

RepeaterTastic starts `exec` from the bundle folder in its own process group, with a small
environment:

| Variable | |
| --- | --- |
| `RT_PLUGIN_ID` | the plugin id |
| `RT_PLUGIN_SOCKET` | Unix socket to connect to |
| `RT_PLUGIN_TOKEN` | the token for this run |
| `RT_PLUGIN_DATA`, `HOME` | the plugin's data folder |
| `PATH`, `TZ`, `LANG`, `SSL_CERT_*`, `*_PROXY` | passed through when set |

Anything the program prints goes to its log. To stop it, RepeaterTastic sends `Stop`, waits 5
seconds, sends SIGTERM, waits another 5, then sends SIGKILL.

### The API

The API is [`proto/plugin/v1/plugin.proto`](../proto/plugin/v1/plugin.proto); the generated Go code
is `pluginapi/v1`. Every call carries `authorization: Bearer <token>` metadata.

1. Open the `Session` stream and send `Hello {plugin_id, api_version: 1, plugin_version}`. An
   attached plugin may add `manifest_yaml` so the GUI shows its details and settings form.
2. Receive `Welcome`, which carries the granted permissions, settings as JSON, the radios (each
   with its relay persona) and the data folder.
3. Events follow, filtered by what was granted:
   - `PacketEvent` (see [Packet events](#packet-events));
   - `NodeEvent`: a node in that radio's node database changed;
   - `TextMessageEvent`: relay persona messages;
   - `TracerouteEvent`;
   - `SettingsChanged`;
   - `PanelAction`;
   - `Stop`.
4. On the stream, send `Status`, `LogLine`, `PanelData` and a `Heartbeat` now and then.
5. While the session is open, call `ListRadios`, `ListNodes`, `SendText` and `Traceroute`.

A slow plugin loses events rather than holding up the radio. The dropped count shows on its page.

### Packet events

A `PacketEvent` is one packet a radio heard or sent.

| Field | |
| --- | --- |
| `direction` | `rx` or `tx` |
| `kind` | rx: `heard` (decoded, not for our identities), `delivered` (to one of our identities), `relayed`, `dup` (seen before), `echo` (our own packet repeated back), `legacy` (pre-2.3 firmware, ignored), `undecryptable`, `bad`. tx: `ours`, `relayed` |
| `mesh_packet` | A `meshtastic.MeshPacket` protobuf. `decoded` when any channel or key on that radio could read it, otherwise encrypted as heard. Received packets have `rx_time` set. `pki_encrypted` marks a DM |
| `decoded` | Whether the payload is decoded |
| `channel_hash`, `channel_name` | The on-air channel hash (0 for DMs); the channel it decoded on, `PKI` for a DM, empty when undecoded |
| `relay_channel_index` | The channel's index (0–7) on the radio's relay persona, or `-1`. Set when the relay persona holds the channel, or for a DM addressed to the relay persona. When set, `mesh_packet.channel` is that index; otherwise it's the on-air hash |
| `holders` | Every identity on the radio that would hear the packet as a node does, with the channel's index on it: those holding the channel, or the recipient of a DM. Use it to report as an identity other than the relay persona |
| `reporter_node_num` | The radio's relay persona |

Decoding uses every identity's channels and keys, not just the relay persona's. A plugin that
reports **as an identity** should keep only what that node would hear itself: the identity is
in `holders` (use its `channel_index` as the channel), and `to` is either broadcast or that
identity. For the relay persona, `relay_channel_index >= 0` says the same thing. `ListRadios`
lists each radio's identities. `Traceroute` always sends from the identity chosen in the plugin's
`identities` settings on that radio, or its relay persona when none is chosen there. A `from`
naming any other identity is refused, so the operator decides who transmits.

The node database (`ListNodes`, `NodeEvent`) is shared by all of a radio's identities. It can hold
names, positions and metrics learned on channels or in DMs the relay persona can't read.

### Go SDK

```go
c, err := pluginsdk.Connect(ctx, pluginsdk.Options{Version: "1.0.0"}) // reads RT_PLUGIN_* from the environment
if err != nil { log.Fatal(err) }
defer c.Close()
_ = c.Status("connected", "ok", nil)
for msg := range c.Events() {
    if t := msg.GetText(); t != nil && t.Direction == "in" && t.Text == "ping" {
        req := &pluginv1.SendTextRequest{RadioId: t.RadioId, Channel: t.Channel, Text: "pong"}
        if t.Direct {
            req.To, req.Channel = fmt.Sprintf("!%08x", t.From), 0 // answer a DM with a DM
        }
        c.Host.SendText(c.Context(), req)
    }
}
```

Other languages can generate a client from the proto.

### Panels

The panel is served with `Content-Security-Policy: sandbox allow-scripts allow-popups` and
`connect-src 'none'`. It can't make network requests or read the GUI's storage. Messages:

| Direction | Message |
| --- | --- |
| GUI → panel | `{type: "data", data, theme: "light" \| "dark"}`: the plugin's latest `PanelData`, sent when it changes and when the panel loads |
| panel → GUI | `{type: "ready"}`: ask for the data now |
| panel → GUI | `{type: "action", name, payload}`: delivered to the plugin as `PanelAction` |
| panel → GUI | `{type: "resize", height}`: set the frame height in pixels |

Build the panel from `data` with DOM methods, not `innerHTML`: packet contents come from the mesh.
