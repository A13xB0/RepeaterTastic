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
| Folder | Copy the .zip into `<state_dir>/plugins/inbox/`. It installs within a few seconds; a bundle that can't be used moves to `inbox/.rejected/` next to a `.error.txt` giving the reason |
| Command line | `sudo -u repeatertastic repeatertastic plugin install hello-plugin.zip` (a file or an `https://` URL) |
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
| `traceroute.send` | Send traceroutes from each radio's relay persona |

Plugins act as the **relay persona** of each radio: the node the site already is on the mesh.
Transmissions go through the normal transmit queue and duty cycle. Each plugin also has a budget,
30 messages and 12 traceroutes an hour by default. `plugins.messages_per_hour` and
`traceroutes_per_hour` change the budget; `0` stops plugins sending at all. Every send is written
to the plugin's log.

A plugin's `network` list (shown before you enable it) names the services it talks to. It is a
declaration, not a firewall: a plugin is a program running as the RepeaterTastic user, so only
install plugins you trust.

## Settings

The **Settings** tab is a form built from the plugin's `plugin.yaml`. Secrets are write-only:
the API and GUI show that one is saved, never its value. Changes reach a running plugin at once.
Settings and grants are kept in `<state_dir>/plugins/state.json` (mode 0600).

## Status, log and panel

- **Status**: a one-line summary on the plugin's card, for example "Uploading · 1,204 packets
  today", plus label/value fields on its page.
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
3. Start the plugin with `RT_PLUGIN_ID`, `RT_PLUGIN_ADDR=<host>:4450` and `RT_PLUGIN_TOKEN`.

The TCP connection isn't encrypted. Keep it on localhost, a private network or a VPN.
**New token** on the plugin's page replaces the token and disconnects the old one.

## Configuration

```yaml
plugins:
    enabled: true                 # the plugin system
    dir: ""                       # "" = <state_dir>/plugins
    listen: ""                    # TCP address for attached plugins, e.g. 127.0.0.1:4450 ("" = off)
    allow_url_install: true       # the GUI may download bundles from a URL
    messages_per_hour: 30         # per plugin; 0 = plugins may not send messages
    traceroutes_per_hour: 12      # per plugin; 0 = plugins may not send traceroutes
    entries:                      # pin plugins: the GUI shows these as "config file" and won't change them
        - id: my-plugin
          enabled: true
          permissions: [packets.read, nodes.read, traceroute.send]
          settings:
              api_key: ${MY_PLUGIN_API_KEY}  # ${VAR} is read from the environment
```

Pinned entries still need the plugin installed. The folder looks like this:

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
repeatertastic plugin list                          installed plugins, on or off, granted permissions
repeatertastic plugin install <bundle.zip | URL>
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
    type: secret           # string, secret, url, bool, int, number, select
    required: true
    help: From your account page.
  - key: region
    type: select
    options: [scotland, england]
    default: scotland
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
   - `PacketEvent`: a `meshtastic.MeshPacket` protobuf, with the payload decoded when a channel
     or key on that radio could read it; `reporter_node_num` is the radio's relay persona;
   - `NodeEvent`;
   - `TextMessageEvent`: relay persona messages;
   - `TracerouteEvent`;
   - `SettingsChanged`;
   - `PanelAction`;
   - `Stop`.
4. On the stream, send `Status`, `LogLine`, `PanelData` and a `Heartbeat` now and then.
5. While the session is open, call `ListRadios`, `ListNodes`, `SendText` and `Traceroute`.

A slow plugin loses events rather than holding up the radio. The dropped count shows on its page.

### Go SDK

```go
c, err := pluginsdk.Connect(ctx, pluginsdk.Options{Version: "1.0.0"}) // reads RT_PLUGIN_* from the environment
if err != nil { log.Fatal(err) }
defer c.Close()
_ = c.Status("connected", "ok", nil)
for msg := range c.Events() {
    if t := msg.GetText(); t != nil && t.Text == "ping" {
        c.Host.SendText(c.Context(), &pluginv1.SendTextRequest{RadioId: t.RadioId, Channel: t.Channel, Text: "pong"})
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
