# Real Meshtastic nodes (in progress)

[← README](../README.md) · [Architecture](architecture.md) · [Hardware](hardware.md)

RepeaterTastic is moving from its own Go implementation of a Meshtastic node to real Meshtastic
firmware: the relay persona and each identity run as a radio-less `meshtasticd`
(`Lora: Module: sim`) started by RepeaterTastic, driven through the client API (the protocol the
apps use). Their transmissions go out through a bridge to RepeaterTastic's radio (a KISS modem or
the `spi` driver), and everything the radio hears is fed back to them, the way Meshtasticator
connects simulated nodes.

**Status:** the relay persona can run on meshtasticd (`hosted.persona`, see
[Configuration](configuration.md#hosted-nodes-on-meshtasticd-experimental)); identities follow.

This page records what the firmware does, as measured, so the design rests on facts. The work is
tracked in the epic pull request, [ScotMesh/RepeaterTastic#5](https://github.com/ScotMesh/RepeaterTastic/pull/5).

## The client API (`internal/mtclient`)

- Stream framing `0x94 0xC3 len16 protobuf` on TCP and serial; console text between frames is
  skipped. The client sends 32 × `0xC3` before its first frame, as the Python client does.
- The `want_config` handshake mirrors my_info, metadata, the node database, channels, config and
  module config. A completion with another request's nonce is ignored.
- Admin to the node itself: `hop_limit` 0, `want_ack` for sets. A get request is acknowledged
  first and answered afterwards, so the client waits for the admin response itself.
- A client packet with `hop_limit` 0 is transmitted with hop limit 0: the client fills in the node's
  configured limit.
- The client reconnects by itself (backoff 1 s to 30 s) and re-runs the handshake after a reboot.
  `Reconnect()` refreshes the mirror after a change the node applies without rebooting.

Live tests against real meshtasticd containers:

```bash
cd tests/interop
UDP=0 MESHTASTICD_IMAGE=meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian ./run_meshtasticd.sh up 2
cd ../.. && RT_TEST_MESHTASTICD=127.0.0.1:4410,127.0.0.1:4411 go test ./internal/mtclient -run Live -v
```

## Measured on meshtasticd 2.7.26 and 2.8.0

| | 2.7.26 | 2.8.0 |
| --- | --- | --- |
| Handshake and admin over TCP | works | works |
| Node number | from `-h` hwid | crc32 of the public key (the owner id still shows the hwid) |
| PKI keys | generated when the region is first set | present from first boot |
| `set_config lora` (region) | reboots after 7 s | applied without a reboot |
| `set_owner` | reboots after 7 s | — |
| Channel text through the sim envelope | works, RSSI/SNR honoured | works, RSSI/SNR honoured |
| PKI DM out of an instance | handed to the client as a bare encrypted packet | `SIMULATOR_APP` envelope, `Compressed{UNKNOWN_APP, ciphertext}` |
| PKI DM into an instance | fails: ciphertext is taken as a port-0 payload | decrypted and delivered; ACK returned |
| Private memory per idle instance | about 3 MB (18 MB RSS, mostly the shared binary) | about 3 MB |

Consequences:

- **Hosted nodes need meshtasticd 2.8 or newer.** 2.7 can't take a PKI DM back in.
- **Provision in one edit transaction** (`begin_edit_settings` … `commit_edit_settings`) so a node
  reboots at most once.
- **Identities must be `CLIENT_MUTE`:** a co-located `CLIENT` instance rebroadcasts every packet
  another instance sends, as a real client would.
- **Drop frames addressed to node 0.** The firmware transmits the routing error for a failed
  client DM to `!00000000`.
- **An instance only decrypts a DM once it knows the sender's key,** so instances must hear each
  other's NodeInfo (the bridge's loopback) before DMs between them work.
- Memory is not the limit it was feared to be: a Pi Zero 2 W can host dozens of identities.

## What an instance hands the client

A transmission arrives as a `FromRadio.packet` whose `decoded.portnum` is `SIMULATOR_APP` and whose
payload is a `Compressed` message:

- **Channel packets:** `Compressed.portnum` is the real port and `Compressed.data` the plaintext
  payload. The outer `Data` keeps the other fields (`request_id`, `want_response`, `bitfield`, …),
  and the outer `MeshPacket.channel` is the channel *index*. The bridge re-encrypts with that
  channel's key.
- **PKI DMs** (and channel packets the instance couldn't decrypt): `Compressed.portnum` is
  `UNKNOWN_APP` and `Compressed.data` is the ciphertext.
- Header fields (`from`, `to`, `id`, `hop_limit`, `hop_start`, `want_ack`, `relay_node`,
  `next_hop`, `pki_encrypted`) are on the outer packet.

Injecting the same shape with `rx_rssi`, `rx_snr` and `rx_time` set makes the instance treat it as a
frame heard off air.

## Remote identities in the host

- An identity a hosted node stands for is a *remote* identity: `mesh.Identity` with a `mesh.Remote`
  and no private key. Its user and channels are mirrored from the node after every handshake.
- `mesh.Host` hands a remote identity's sends to its node (`from` 0, the node fills it in) and takes
  what the node delivers through `Host.RemoteReceived`: messages, ACK/NAK results, node DB updates,
  traceroute results and app clients. It never answers NodeInfo, position or admin requests for
  the node.
- App clients on a remote identity's port have admin messages forwarded to the node, and name and
  channel edits in the GUI are written to it.

## Airs

A hosted meshtasticd has no radio. RepeaterTastic gives it a mesh interface, an **air**
(`nodes.Air`): nodes join an air, and one rule holds on every air: nodes on the same air hear each
other at hop limit 0, so none of them repeats a frame that went out from the same place.

- **LoRa air** (`nodes.LoRaAir`), for a radio RepeaterTastic drives (a KISS modem or the `spi`
  driver): every frame the radio hears is injected into every joined node with its RSSI and SNR; a
  joined node's transmission becomes a frame on the host's transmit queue (channel-busy check, duty
  cycle, site turns); what the host transmits is played to the other joined nodes at hop 0. It
  brings no relay (`Relay()` is nil), so the host joins a hosted node with the configured relay
  role as the persona.
- Out of an envelope: PKI ciphertext goes out as it came; channel payloads are re-encrypted with
  the node's channel key, except a relay, which sends the ciphertext first heard.
- A later **board air** could carry nodes through a board running stock Meshtastic firmware (its
  MQTT client proxy or UDP multicast). The board would be that air's relay, and joined nodes would
  sit one hop behind it.

## Supervisor

- `nodes.StartHosted` writes the instance's `config.yaml` (`Lora: Module: sim`, no UDP, no MQTT),
  runs meshtasticd with `-c`, `-d`, `-h` and `-p`, and restarts it with backoff. The last 200 lines
  it printed are kept for the GUI.
- Launchers: `ExecLauncher` runs the installed program; `DockerLauncher` runs a meshtasticd image
  with the API published on 127.0.0.1 and the state directory mounted. Both report the version,
  which must be 2.8.0 or newer.
- On every handshake the host's settings are pushed to the node (one edit transaction), and the
  node's user, channels and node number are mirrored into its identity. A node that isn't up at
  start is stood in for by its saved state, or a placeholder that is swapped for the real node
  when it answers.
